package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

type AgyStep struct {
	StepIndex      int    `json:"step_index"`
	Source         string `json:"source"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	Content        string `json:"content"`
	ToolCalls      []any  `json:"tool_calls"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type TankConversationEvent struct {
	EventID     string `json:"event_id"`
	SessionID   string `json:"session_id"`
	TurnID      string `json:"turn_id"`
	ClientNonce string `json:"client_nonce"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Timestamp   string `json:"timestamp"`

	Payload map[string]any `json:"payload"`
}

type TurnState struct {
	mu           sync.Mutex
	SessionID    string
	TurnID       string
	ClientNonce  string
	Active       bool
	TurnComplete chan struct{}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		slog.Info("Shutting down antigravity-runner...")
		cancel()
	}()

	slog.Info("Starting antigravity-cli-runner spike")

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://tank-nats.nats.svc.cluster.local:4222"
	}
	natsToken := os.Getenv("NATS_TOKEN")
	sessionStorageKey := os.Getenv("TANK_SESSION_STORAGE_KEY")
	if sessionStorageKey == "" {
		sessionStorageKey = os.Getenv("SESSION_STORAGE_KEY")
	}
	if sessionStorageKey == "" {
		slog.Warn("TANK_SESSION_STORAGE_KEY not set, using default")
		sessionStorageKey = "default:dev-session"
	}
	sessionID := os.Getenv("TANK_SESSION_ID")
	provider := "antigravity"

	opts := []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}
	if natsToken != "" {
		opts = append(opts, nats.Token(natsToken))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("Failed to create JetStream", "error", err)
		os.Exit(1)
	}

	agyHome := os.Getenv("AGY_HOME")
	if agyHome == "" {
		homeDir, _ := os.UserHomeDir()
		agyHome = filepath.Join(homeDir, ".gemini", "antigravity-cli")
	}

	var wg sync.WaitGroup

	// Start long-lived agy process
	runCmd := exec.CommandContext(ctx, "agy", "--dangerously-skip-permissions")
	ptmx, err := pty.Start(runCmd)
	if err != nil {
		slog.Error("Failed to start agy pty", "error", err)
		os.Exit(1)
	}
	defer ptmx.Close()

	// Discard pty output so it doesn't block
	go io.Copy(io.Discard, ptmx)

	turnState := &TurnState{}

	// Start tailing transcripts in the background continuously
	go tailTranscripts(ctx, agyHome, nc, sessionStorageKey, turnState)

	// Data-Plane Consumer
	go func() {
		commandSubject := sessionbus.CommandSubject(sessionStorageKey, provider)
		consumerName := "antigravity_data_" + sessionbus.StorageToken(sessionStorageKey)

		consumer, err := js.CreateOrUpdateConsumer(ctx, "TANK_SESSION_BUS", jetstream.ConsumerConfig{
			Durable:       consumerName,
			Name:          consumerName,
			FilterSubject: commandSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       120 * time.Second,
			MaxDeliver:    20,
			MaxAckPending: 1, // Serial dispatch
		})
		if err != nil {
			slog.Error("Failed to create data-plane consumer", "error", err)
			return
		}

		cctx, _ := consumer.Consume(func(msg jetstream.Msg) {
			var command sessionbus.Command
			if err := json.Unmarshal(msg.Data(), &command); err != nil {
				msg.TermWithReason("invalid json")
				return
			}
			slog.Info("Received data-plane command", "type", command.Type)

			if command.Type == sessionbus.CommandSubmitTurn {
				clientNonce := command.ClientNonce
				turnID := command.TurnID
				if turnID == "" {
					turnID = "turn_" + strings.ReplaceAll(uuid.New().String(), "-", "")
				}

				turnState.mu.Lock()
				turnState.SessionID = sessionID
				turnState.TurnID = turnID
				turnState.ClientNonce = clientNonce
				turnState.Active = true
				turnState.TurnComplete = make(chan struct{})
				turnCompChan := turnState.TurnComplete
				turnState.mu.Unlock()

				// Publish turn.claimed
				publishEvent(nc, sessionStorageKey, TankConversationEvent{
					EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
					SessionID:   sessionID,
					TurnID:      turnID,
					ClientNonce: clientNonce,
					Type:        "turn.claimed",
					Source:      provider,
					Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
					Payload:     map[string]any{},
				})

				// Start heartbeat
				stopHeartbeat := make(chan struct{})
				go func() {
					ticker := time.NewTicker(30 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							msg.InProgress()
						case <-stopHeartbeat:
							return
						}
					}
				}()

				// Send prompt to agy
				_, err := ptmx.WriteString(command.Prompt + "\n")
				if err != nil {
					publishEvent(nc, sessionStorageKey, TankConversationEvent{
						EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
						SessionID:   sessionID,
						TurnID:      turnID,
						ClientNonce: clientNonce,
						Type:        "turn.failed",
						Source:      provider,
						Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
						Payload:     map[string]any{"reason": "failed_to_write_to_agy"},
					})
					close(stopHeartbeat)
					msg.Ack()
					return
				}

				// Wait for turn completion
				<-turnCompChan
				close(stopHeartbeat)
				msg.Ack()
			} else {
				msg.Ack()
			}
		})
		wg.Add(1)
		<-ctx.Done()
		cctx.Stop()
		wg.Done()
	}()

	// Control-Plane Consumer
	go func() {
		controlSubject := sessionbus.ControlSubject(sessionStorageKey, provider)
		consumerName := "antigravity_control_" + sessionbus.StorageToken(sessionStorageKey)

		consumer, err := js.CreateOrUpdateConsumer(ctx, "TANK_SESSION_BUS", jetstream.ConsumerConfig{
			Durable:       consumerName,
			Name:          consumerName,
			FilterSubject: controlSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       15 * time.Second,
			MaxDeliver:    10,
			MaxAckPending: 16, // Burst control
		})
		if err != nil {
			slog.Error("Failed to create control-plane consumer", "error", err)
			return
		}

		cctx, _ := consumer.Consume(func(msg jetstream.Msg) {
			slog.Info("Received control-plane message")
			msg.Ack()
		})
		wg.Add(1)
		<-ctx.Done()
		cctx.Stop()
		wg.Done()
	}()

	wg.Wait()
	slog.Info("Antigravity-runner exited naturally")
}

func publishEvent(nc *nats.Conn, sessionStorageKey string, event TankConversationEvent) {
	b, _ := json.Marshal(event)
	eventSubject := sessionbus.SessionEventSubject(sessionStorageKey)
	nc.Publish(eventSubject, b)
}

func tailTranscripts(ctx context.Context, agyHome string, nc *nats.Conn, sessionStorageKey string, turnState *TurnState) {
	offsets := make(map[string]int64)

	brainDir := filepath.Join(agyHome, "brain")

	os.MkdirAll(brainDir, 0755)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Failed to create fsnotify watcher", "error", err)
		return
	}
	defer watcher.Close()

	watchAll := func(dir string) {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				watcher.Add(path)
			}
			return nil
		})
	}
	watchAll(brainDir)

	// Do an initial sweep to catch anything written before watcher started
	sweepTranscripts(brainDir, offsets, nc, sessionStorageKey, turnState)

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					watchAll(event.Name)
				}
			}
			sweepTranscripts(brainDir, offsets, nc, sessionStorageKey, turnState)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("fsnotify error", "error", err)
		}
	}
}

func sweepTranscripts(brainDir string, offsets map[string]int64, nc *nats.Conn, sessionStorageKey string, turnState *TurnState) {
	turnState.mu.Lock()
	if !turnState.Active {
		turnState.mu.Unlock()
		return
	}
	sessionID := turnState.SessionID
	turnID := turnState.TurnID
	clientNonce := turnState.ClientNonce
	turnCompChan := turnState.TurnComplete
	turnState.mu.Unlock()

	filepath.Walk(brainDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "transcript_full.jsonl") {
			return nil
		}

		size := info.Size()
		offset := offsets[path]

		if size < offset {
			offset = 0 // file truncated
		}
		if size <= offset {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		f.Seek(offset, io.SeekStart)
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}

			var step AgyStep
			if err := json.Unmarshal([]byte(line), &step); err == nil {
				// Convert to TankConversationEvent and publish
				evt := TankConversationEvent{
					EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
					SessionID:   sessionID,
					TurnID:      turnID,
					ClientNonce: clientNonce,
					Type:        "turn.provider_output",
					Source:      "antigravity",
					Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
					Payload: map[string]any{
						"type": "provider_output",
						"output": map[string]any{
							"type":    "text",
							"content": step.Content,
						},
					},
				}
				// If it's a planner response or something that has content
				if step.Type == "PLANNER_RESPONSE" || step.Source == "SYSTEM" {
					publishEvent(nc, sessionStorageKey, evt)
				}

				// Check if the turn is fully complete
				if step.Type == "PLANNER_RESPONSE" && step.Status == "DONE" && len(step.ToolCalls) == 0 {
					turnState.mu.Lock()
					if turnState.Active && turnState.TurnID == turnID {
						turnState.Active = false
						publishEvent(nc, sessionStorageKey, TankConversationEvent{
							EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
							SessionID:   sessionID,
							TurnID:      turnID,
							ClientNonce: clientNonce,
							Type:        "turn.completed",
							Source:      "antigravity",
							Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
							Payload:     map[string]any{},
						})
						close(turnCompChan)
					}
					turnState.mu.Unlock()
				}
			}
		}
		offsets[path] = size
		return nil
	})
}
