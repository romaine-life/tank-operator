package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

type AgyStep struct {
	StepIndex     int    `json:"step_index"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	Content       string `json:"content"`
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

	var hasConversation bool
	var wg sync.WaitGroup

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

				// Run agy subprocess
				args := []string{"--dangerously-skip-permissions"}
				if hasConversation {
					args = append(args, "--continue")
				}
				args = append(args, "-p", command.Prompt)

				runCmd := exec.CommandContext(ctx, "agy", args...)
				slog.Info("Running agy", "args", args)

				err := runCmd.Start()
				if err != nil {
					publishEvent(nc, sessionStorageKey, TankConversationEvent{
						EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
						SessionID:   sessionID,
						TurnID:      turnID,
						ClientNonce: clientNonce,
						Type:        "turn.failed",
						Source:      provider,
						Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
						Payload:     map[string]any{"reason": "failed_to_start"},
					})
					close(stopHeartbeat)
					msg.Ack()
					return
				}

				// Tail transcripts while agy runs
				doneTailing := make(chan struct{})
				go tailTranscripts(ctx, agyHome, nc, sessionStorageKey, sessionID, turnID, clientNonce, doneTailing)

				err = runCmd.Wait()
				close(doneTailing)
				
				hasConversation = true

				if err != nil {
					publishEvent(nc, sessionStorageKey, TankConversationEvent{
						EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
						SessionID:   sessionID,
						TurnID:      turnID,
						ClientNonce: clientNonce,
						Type:        "turn.failed",
						Source:      provider,
						Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
						Payload:     map[string]any{"reason": fmt.Sprintf("agy_exit_error: %v", err)},
					})
				} else {
					publishEvent(nc, sessionStorageKey, TankConversationEvent{
						EventID:     "evt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
						SessionID:   sessionID,
						TurnID:      turnID,
						ClientNonce: clientNonce,
						Type:        "turn.completed",
						Source:      provider,
						Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
						Payload:     map[string]any{},
					})
				}

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

func tailTranscripts(ctx context.Context, agyHome string, nc *nats.Conn, sessionStorageKey, sessionID, turnID, clientNonce string, done <-chan struct{}) {
	offsets := make(map[string]int64)
	
	// Pre-snapshot existing sizes
	brainDir := filepath.Join(agyHome, "brain")
	filepath.Walk(brainDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, "transcript_full.jsonl") {
			offsets[path] = info.Size()
		}
		return nil
	})

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			// One last sweep
			sweepTranscripts(brainDir, offsets, nc, sessionStorageKey, sessionID, turnID, clientNonce)
			return
		case <-ticker.C:
			sweepTranscripts(brainDir, offsets, nc, sessionStorageKey, sessionID, turnID, clientNonce)
		}
	}
}

func sweepTranscripts(brainDir string, offsets map[string]int64, nc *nats.Conn, sessionStorageKey, sessionID, turnID, clientNonce string) {
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
							"type": "text",
							"content": step.Content,
						},
					},
				}
				// If it's a planner response or something that has content
				if step.Type == "PLANNER_RESPONSE" || step.Source == "SYSTEM" {
					publishEvent(nc, sessionStorageKey, evt)
				}
			}
		}
		offsets[path] = size
		return nil
	})
}
