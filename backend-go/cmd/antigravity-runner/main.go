package main

/*
WARNING: DO NOT RIP OUT THE PTY RUNNER OR REPLACE IT WITH WEBSOCKET/PROTOBUF PROTOCOLS.

Although the Google Antigravity Python SDK communicates with `localharness` via a Protobuf/WebSocket protocol,
the `agy` CLI binary used in this container does NOT support that protocol (it is strictly a human-facing terminal UI).

We are forced to use the `agy` CLI binary because the closed-source `localharness` binary only supports GCP Service
Accounts and does not support consumer OAuth tokens (which we proxy-route).

Therefore, this runner MUST run `agy` inside a pseudo-terminal (PTY) and scrape/extract events by tailing the transcript
using fsnotify. Do not attempt to refactor this to use gRPC, Protobuf, or WebSockets directly with `agy`.
*/

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/creack/pty"
	"github.com/fsnotify/fsnotify"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"

	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

const provider = "antigravity"

type AgyToolCall struct {
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args"`
	ToolAction  string          `json:"toolAction,omitempty"`
	ToolSummary string          `json:"toolSummary,omitempty"`
}

type AgyUsage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}

type AgyStep struct {
	StepIndex      int               `json:"step_index"`
	Source         string            `json:"source"`
	Type           string            `json:"type"`
	Status         string            `json:"status"`
	Content        json.RawMessage   `json:"content"`
	ToolCalls      []json.RawMessage `json:"tool_calls"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Usage          *AgyUsage         `json:"usage,omitempty"`
}

type runnerConfig struct {
	sessionID         string
	sessionStorageKey string
	ownerEmail        string
	natsURL           string
	natsToken         string
	natsStream        string
	workspace         string
	agyHome           string
}

type eventBuilder struct {
	sessionID         string
	sessionStorageKey string
	ownerEmail        string
}

type finalAnswer struct {
	timelineID     string
	providerItemID string
}

type pendingTool struct {
	id   string
	name string
}

type runnerState struct {
	mu            sync.Mutex
	currentRun    *turnRun
	pendingSteps  []parsedStep
	wakeRequested bool
}

type parsedStep struct {
	path string
	line string
	step AgyStep
}

func (s *runnerState) handleStep(path, line string, step AgyStep, cfg runnerConfig) error {
	if !isRelevantStep(step) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentRun != nil {
		return s.currentRun.observeStep(path, line, step)
	}

	s.pendingSteps = append(s.pendingSteps, parsedStep{path: path, line: line, step: step})
	if !s.wakeRequested {
		s.wakeRequested = true
		taskID := providerStepID(path, line, step)
		go func() {
			err := registerBackgroundTaskWake(cfg, taskID, "Antigravity background activity detected")
			if err != nil {
				slog.Error("failed to register background task wake", "error", err)
			}
		}()
	}
	return nil
}

func (s *runnerState) attachTurn(run *turnRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentRun = run
	for _, ps := range s.pendingSteps {
		_ = run.observeStep(ps.path, ps.line, ps.step)
	}
	s.pendingSteps = nil
	s.wakeRequested = false
}

func (s *runnerState) detachTurn(run *turnRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentRun == run {
		s.currentRun = nil
	}
}

type turnRun struct {
	builder         eventBuilder
	publish         func(map[string]any) error
	turnID          string
	clientNonce     string
	turnComplete    chan struct{}

	mu              sync.Mutex
	started         bool
	seen            map[string]struct{}
	final           finalAnswer
	providerFailed  string
	pendingTools    []pendingTool
	cumulativeUsage *AgyUsage
}

type activeProcess struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	turnID      string
	interrupted bool
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		slog.Info("shutting down antigravity cli runner")
		cancel()
	}()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid antigravity runner config", "error", err)
		os.Exit(1)
	}
	slog.Info("starting antigravity cli runner", "session_id", cfg.sessionID, "storage_key", cfg.sessionStorageKey)

	nc, err := connectNATS(cfg)
	if err != nil {
		slog.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("failed to create JetStream client", "error", err)
		os.Exit(1)
	}

	builder := eventBuilder{
		sessionID:         cfg.sessionID,
		sessionStorageKey: cfg.sessionStorageKey,
		ownerEmail:        cfg.ownerEmail,
	}
	publisher := func(event map[string]any) error {
		return publishEvent(nc, cfg.sessionStorageKey, event)
	}
	active := &activeProcess{}
	agyArgs := []string{"--dangerously-skip-permissions"}
	runCmd := exec.Command("agy", agyArgs...)
	runCmd.Dir = cfg.workspace
	runCmd.Env = os.Environ()

	ptmx, err := pty.Start(runCmd)
	if err != nil {
		slog.Error("Failed to start agy pty", "error", err)
		os.Exit(1)
	}
	defer func() { _ = ptmx.Close() }()
	active.cmd = runCmd

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				break
			}
			os.Stdout.Write(buf[:n])
			if bytes.Contains(buf[:n], []byte("Terms of Service")) {
				slog.Info("Detected Terms of Service screen, auto-accepting...")
				// Press Next (\r), wait a beat, then press Right Arrow (\x1b[C) to select Done, then Enter (\r)
				go func() {
					time.Sleep(500 * time.Millisecond)
					ptmx.WriteString("\r")
					time.Sleep(500 * time.Millisecond)
					ptmx.WriteString("\x1b[C\r")
				}()
			}
		}
	}()

	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer readyCancel()
	if err := waitForCliReady(readyCtx, cfg.agyHome); err != nil {
		slog.Error("agy CLI failed to become ready", "error", err)
		os.Exit(1)
	}

	state := &runnerState{}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		runDataConsumer(ctx, js, cfg, builder, publisher, active, state, ptmx)
	}()
	go func() {
		defer wg.Done()
		runControlConsumer(ctx, js, cfg, active)
	}()
	go func() {
		defer wg.Done()
		tailTranscripts(ctx, cfg, state)
	}()
	wg.Wait()
	slog.Info("antigravity cli runner exited")
}

func waitForCliReady(ctx context.Context, agyHome string) error {
	logPath := filepath.Join(agyHome, "cli.log")
	slog.Info("waiting for agy CLI to complete initialization and authentication...", "log_path", logPath)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("agy CLI failed to initialize: %w", ctx.Err())
		case <-ticker.C:
			data, err := os.ReadFile(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				slog.Debug("failed to read agy log path", "error", err)
				continue
			}
			if bytes.Contains(data, []byte("Auth done received")) || bytes.Contains(data, []byte("Reloading system slash commands")) {
				slog.Info("agy CLI is fully authenticated and ready for user input")
				return nil
			}
		}
	}
}

func registerBackgroundTaskWake(cfg runnerConfig, taskID string, summary string) error {
	baseURL := strings.TrimRight(firstEnv("OPERATOR_INTERNAL_URL", "http://tank-operator.tank-operator.svc.cluster.local:8080"), "/")
	tokenPath := firstEnv("OPERATOR_TOKEN_PATH", "/var/run/secrets/kubernetes.io/serviceaccount/token")
	if baseURL == "" || tokenPath == "" || cfg.sessionID == "" {
		return nil
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	url := fmt.Sprintf("%s/api/internal/sessions/%s/background-task-wakes", baseURL, cfg.sessionID)

	payload := map[string]any{
		"task_id":        taskID,
		"status":         "completed",
		"description":    "Antigravity background process finished",
		"summary":        summary,
		"last_tool_name": "agy",
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("background task wake register failed: %d", resp.StatusCode)
	}
	return nil
}

func loadConfig() (runnerConfig, error) {
	storageKey := firstEnv("TANK_SESSION_STORAGE_KEY", "SESSION_STORAGE_KEY")
	sessionID := firstEnv("SESSION_ID", "TANK_SESSION_ID")
	if storageKey == "" {
		storageKey = sessionID
	}
	if sessionID == "" {
		_, sessionID = sessionbus.StorageScopeAndSessionID(storageKey)
	}
	if sessionID == "" {
		return runnerConfig{}, errors.New("SESSION_ID is required")
	}
	if storageKey == "" {
		return runnerConfig{}, errors.New("TANK_SESSION_STORAGE_KEY or SESSION_ID is required")
	}
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		home = "/home/node"
	}
	return runnerConfig{
		sessionID:         sessionID,
		sessionStorageKey: storageKey,
		ownerEmail:        strings.ToLower(strings.TrimSpace(os.Getenv("POD_OWNER_EMAIL"))),
		natsURL:           firstNonEmpty(os.Getenv("NATS_URL"), "nats://tank-nats.nats.svc.cluster.local:4222"),
		natsToken:         strings.TrimSpace(os.Getenv("NATS_TOKEN")),
		natsStream:        sessionbus.StreamName(os.Getenv("NATS_STREAM")),
		workspace:         firstNonEmpty(strings.TrimSpace(os.Getenv("WORKSPACE")), "/workspace"),
		agyHome:           firstNonEmpty(firstEnv("ANTIGRAVITY_HOME", "AGY_HOME"), filepath.Join(home, ".gemini", "antigravity-cli")),
	}, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func connectNATS(cfg runnerConfig) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}
	if cfg.natsToken != "" {
		opts = append(opts, nats.Token(cfg.natsToken))
	}
	return nats.Connect(cfg.natsURL, opts...)
}

func runDataConsumer(ctx context.Context, js jetstream.JetStream, cfg runnerConfig, builder eventBuilder, publisher func(map[string]any) error, active *activeProcess, state *runnerState, ptmx *os.File) {
	commandSubject := sessionbus.CommandSubject(cfg.sessionStorageKey, provider)
	consumerName := "antigravity_cli_data_" + sessionbus.StorageToken(cfg.sessionStorageKey)

	consumer, err := js.CreateOrUpdateConsumer(ctx, cfg.natsStream, jetstream.ConsumerConfig{
		Durable:       consumerName,
		Name:          consumerName,
		FilterSubject: commandSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       120 * time.Second,
		MaxDeliver:    20,
		MaxAckPending: 1,
	})
	if err != nil {
		slog.Error("failed to create data-plane consumer", "error", err)
		return
	}

	var conversationStarted bool
	var conversationMu sync.Mutex
	consumeCtx, err := consumer.Consume(func(msg jetstream.Msg) {
		var command sessionbus.Command
		if err := json.Unmarshal(msg.Data(), &command); err != nil {
			_ = msg.TermWithReason("invalid json")
			return
		}
		command = command.Normalize()
		if command.Type != sessionbus.CommandSubmitTurn {
			_ = msg.Ack()
			return
		}
		conversationMu.Lock()
		continueConversation := conversationStarted
		conversationMu.Unlock()
		started, err := handleSubmitTurn(ctx, cfg, builder, publisher, active, state, msg, command, continueConversation, ptmx)
		if started {
			conversationMu.Lock()
			conversationStarted = true
			conversationMu.Unlock()
		}
		if err != nil {
			slog.Error("submit_turn failed", "turn_id", command.TurnID, "error", err)
			_ = msg.NakWithDelay(5 * time.Second)
		}
	})
	if err != nil {
		slog.Error("failed to consume data-plane commands", "error", err)
		return
	}
	<-ctx.Done()
	consumeCtx.Stop()
}

func runControlConsumer(ctx context.Context, js jetstream.JetStream, cfg runnerConfig, active *activeProcess) {
	controlSubject := sessionbus.ControlSubject(cfg.sessionStorageKey, provider)
	consumerName := "antigravity_cli_control_" + sessionbus.StorageToken(cfg.sessionStorageKey)

	consumer, err := js.CreateOrUpdateConsumer(ctx, cfg.natsStream, jetstream.ConsumerConfig{
		Durable:       consumerName,
		Name:          consumerName,
		FilterSubject: controlSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       15 * time.Second,
		MaxDeliver:    10,
		MaxAckPending: 16,
	})
	if err != nil {
		slog.Error("failed to create control-plane consumer", "error", err)
		return
	}

	consumeCtx, err := consumer.Consume(func(msg jetstream.Msg) {
		var command sessionbus.Command
		if err := json.Unmarshal(msg.Data(), &command); err != nil {
			_ = msg.TermWithReason("invalid json")
			return
		}
		command = command.Normalize()
		if command.Type == sessionbus.CommandInterrupt {
			if err := active.interrupt(command.TargetTurnID); err != nil {
				slog.Warn("failed to interrupt antigravity process", "turn_id", command.TargetTurnID, "error", err)
			}
		}
		_ = msg.Ack()
	})
	if err != nil {
		slog.Error("failed to consume control-plane commands", "error", err)
		return
	}
	<-ctx.Done()
	consumeCtx.Stop()
}

func handleSubmitTurn(ctx context.Context, cfg runnerConfig, builder eventBuilder, publisher func(map[string]any) error, active *activeProcess, state *runnerState, msg jetstream.Msg, command sessionbus.Command, continueConversation bool, ptmx *os.File) (bool, error) {
	turnID := command.TurnID
	if turnID == "" {
		turnID = "turn_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	clientNonce := command.ClientNonce
	if clientNonce == "" {
		clientNonce = turnID
	}

	active.set(active.cmd, turnID)
	defer active.clear(active.cmd)

	run := newTurnRun(builder, publisher, turnID, clientNonce)
	if err := publisher(builder.turnEvent(turnID, clientNonce, string(conversation.EventTurnClaimed), "")); err != nil {
		return false, err
	}

	stopHeartbeat := startHeartbeat(ctx, msg)
	defer stopHeartbeat()

	_, err := ptmx.WriteString(command.Prompt + "\r")
	if err != nil {
		_ = publisher(builder.turnEvent(turnID, clientNonce, string(conversation.EventTurnFailed), "failed_to_start"))
		_ = msg.Ack()
		return false, nil
	}

	state.attachTurn(run)
	defer state.detachTurn(run)

	<-run.turnComplete
	interrupted := active.wasInterrupted(turnID)

	var terminalErr error
	switch {
	case interrupted:
		terminalErr = run.finishInterrupted()
	case run.providerFailed != "":
		terminalErr = run.finishFailed("provider_error")
	default:
		terminalErr = run.finishCompleted()
	}
	if terminalErr != nil {
		return true, terminalErr
	}
	if err := msg.Ack(); err != nil {
		return true, err
	}
	return true, nil
}

func startHeartbeat(ctx context.Context, msg jetstream.Msg) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				_ = msg.InProgress()
			}
		}
	}()
	return func() { close(stop) }
}

func logProcessOutput(ctx context.Context, name string, reader io.Reader) {
	if reader == nil {
		return
	}
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("agy output", "stream", name, "line", scanner.Text())
			}
		}
	}()
}

func (a *activeProcess) set(cmd *exec.Cmd, turnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd = cmd
	a.turnID = turnID
	a.interrupted = false
}

func (a *activeProcess) clear(cmd *exec.Cmd) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == cmd {
		a.cmd = nil
		a.turnID = ""
	}
}

func (a *activeProcess) interrupt(targetTurnID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return nil
	}
	if targetTurnID != "" && targetTurnID != a.turnID {
		return nil
	}
	a.interrupted = true
	return a.cmd.Process.Signal(os.Interrupt)
}

func (a *activeProcess) wasInterrupted(turnID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interrupted && (turnID == "" || turnID == a.turnID)
}

func newTurnRun(builder eventBuilder, publisher func(map[string]any) error, turnID, clientNonce string) *turnRun {
	return &turnRun{
		builder:      builder,
		publish:      publisher,
		turnID:       turnID,
		clientNonce:  clientNonce,
		turnComplete: make(chan struct{}),
		seen:         map[string]struct{}{},
	}
}

func (r *turnRun) observeStep(path, line string, step AgyStep) error {
	if !isRelevantStep(step) {
		return nil
	}
	providerID := providerStepID(path, line, step)
	seenKey := providerID + ":" + strings.ToUpper(step.Status)

	r.mu.Lock()
	if _, ok := r.seen[seenKey]; ok {
		r.mu.Unlock()
		return nil
	}
	r.seen[seenKey] = struct{}{}
	r.mu.Unlock()

	if err := r.ensureStarted(providerID); err != nil {
		return err
	}

	// 1. Process usage first if present
	if usage := extractUsage(step); usage != nil {
		r.mu.Lock()
		r.cumulativeUsage = usage
		r.mu.Unlock()

		usageEvent := r.builder.turnUsageEvent(r.turnID, r.clientNonce, providerID, usage)
		if err := r.publish(usageEvent); err != nil {
			return err
		}
	}

	text := contentText(step.Content)

	// 2. Process system error messages
	if strings.EqualFold(step.Source, "SYSTEM") && strings.EqualFold(step.Type, "ERROR_MESSAGE") {
		var targetToolID string
		r.mu.Lock()
		if len(r.pendingTools) > 0 {
			// Match and close the last pending tool call (LIFO)
			lastTool := r.pendingTools[len(r.pendingTools)-1]
			targetToolID = lastTool.id
			r.pendingTools = r.pendingTools[:len(r.pendingTools)-1]
		}
		r.mu.Unlock()

		if targetToolID == "" {
			targetToolID = providerID
		}

		return r.publish(r.builder.itemEvent(r.turnID, targetToolID, string(conversation.EventItemFailed), string(conversation.ActorRunner), map[string]any{
			"kind":     "system_error",
			"text":     firstNonEmpty(text, "Antigravity reported an error."),
			"is_error": true,
			"outcome": map[string]any{
				"kind":   "execution_failed",
				"reason": "provider_item_error",
			},
		}))
	}

	// 3. Process model steps
	if strings.EqualFold(step.Source, "MODEL") {
		// Tool call generation
		if len(step.ToolCalls) > 0 {
			if text != "" {
				msgID := providerID + ":text"
				timelineID := itemTimelineID(r.turnID, msgID)
				event := r.builder.itemEvent(r.turnID, msgID, string(conversation.EventItemCompleted), string(conversation.ActorAssistant), map[string]any{
					"kind": "message",
					"text": text,
				})
				if err := r.publish(event); err != nil {
					return err
				}
				r.mu.Lock()
				r.final = finalAnswer{timelineID: timelineID, providerItemID: msgID}
				r.mu.Unlock()
			}

			for i, rawCall := range step.ToolCalls {
				var tc AgyToolCall
				if err := json.Unmarshal(rawCall, &tc); err != nil {
					continue
				}
				toolID := fmt.Sprintf("%s:tool:%s", providerID, strconv.Itoa(i))
				title := tc.ToolSummary
				if title == "" {
					title = tc.ToolAction
				}
				if title == "" {
					title = tc.Name
				}
				event := r.builder.itemEvent(r.turnID, toolID, string(conversation.EventItemStarted), string(conversation.ActorTool), map[string]any{
					"kind":  "tool",
					"title": title,
					"name":  tc.Name,
					"input": tc.Args,
				})
				if err := r.publish(event); err != nil {
					return err
				}
				r.mu.Lock()
				r.pendingTools = append(r.pendingTools, pendingTool{id: toolID, name: tc.Name})
				r.mu.Unlock()
			}
			return nil
		}

		// Tool result steps
		if !strings.EqualFold(step.Type, "PLANNER_RESPONSE") {
			var targetToolID string
			r.mu.Lock()
			matchIdx := -1
			for idx, pt := range r.pendingTools {
				if strings.EqualFold(pt.name, step.Type) {
					matchIdx = idx
					break
				}
			}
			if matchIdx >= 0 {
				targetToolID = r.pendingTools[matchIdx].id
				r.pendingTools = append(r.pendingTools[:matchIdx], r.pendingTools[matchIdx+1:]...)
			} else if len(r.pendingTools) > 0 {
				targetToolID = r.pendingTools[0].id
				r.pendingTools = r.pendingTools[1:]
			}
			r.mu.Unlock()

			if targetToolID == "" {
				targetToolID = providerID
			}

			isError := strings.EqualFold(step.Status, "ERROR")
			eventType := string(conversation.EventItemCompleted)
			var outcome map[string]any
			if isError {
				eventType = string(conversation.EventItemFailed)
				outcome = map[string]any{
					"kind":   "execution_failed",
					"reason": "provider_item_error",
				}
			} else {
				outcome = map[string]any{
					"kind": "ok",
				}
			}

			event := r.builder.itemEvent(r.turnID, targetToolID, eventType, string(conversation.ActorTool), map[string]any{
				"kind":     "tool_result",
				"output":   text,
				"is_error": isError,
				"outcome":  outcome,
			})
			return r.publish(event)
		}

		// Assistant Prose
		if strings.EqualFold(step.Type, "PLANNER_RESPONSE") && len(step.ToolCalls) == 0 {
			timelineID := itemTimelineID(r.turnID, providerID)
			if text != "" {
				event := r.builder.itemEvent(r.turnID, providerID, string(conversation.EventItemCompleted), string(conversation.ActorAssistant), map[string]any{
					"kind": "message",
					"text": text,
				})
				if err := r.publish(event); err != nil {
					return err
				}
				r.mu.Lock()
				r.final = finalAnswer{timelineID: timelineID, providerItemID: providerID}
				r.mu.Unlock()
			}
			if strings.EqualFold(step.Status, "DONE") {
				close(r.turnComplete)
			}
			return nil
		}
	}

	return nil
}

func (r *turnRun) ensureStarted(providerID string) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	r.mu.Unlock()
	return r.publish(r.builder.turnEvent(r.turnID, r.clientNonce, string(conversation.EventTurnStarted), providerID))
}

func (r *turnRun) finishCompleted() error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	r.mu.Lock()
	final := r.final
	usage := r.cumulativeUsage
	r.mu.Unlock()
	if final.timelineID == "" {
		return r.finishFailed("provider_no_final_answer")
	}
	return r.publish(r.builder.turnCompletedEvent(r.turnID, r.clientNonce, final, usage))
}

func (r *turnRun) finishFailed(reason string) error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	r.mu.Lock()
	usage := r.cumulativeUsage
	r.mu.Unlock()
	return r.publish(r.builder.turnFailedEvent(r.turnID, r.clientNonce, reason, usage))
}

func (r *turnRun) finishInterrupted() error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	r.mu.Lock()
	usage := r.cumulativeUsage
	r.mu.Unlock()
	return r.publish(r.builder.turnInterruptedEvent(r.turnID, r.clientNonce, usage))
}

func isRelevantStep(step AgyStep) bool {
	if strings.EqualFold(step.Source, "USER_EXPLICIT") {
		return false
	}
	if extractUsage(step) != nil {
		return true
	}
	if strings.EqualFold(step.Source, "SYSTEM") {
		typ := strings.ToLower(step.Type)
		if typ == "error_message" || typ == "loadcodeassist" {
			return true
		}
		if strings.Contains(typ, "wake") || strings.Contains(typ, "timer") || strings.Contains(typ, "background") || strings.Contains(typ, "schedule") {
			return true
		}
		if typ == "system_message" {
			contentStr := strings.ToLower(string(step.Content))
			if strings.Contains(contentStr, "[message]") && (strings.Contains(contentStr, "task") || strings.Contains(contentStr, "timer")) {
				return true
			}
		}
		return false
	}
	if strings.EqualFold(step.Source, "MODEL") || strings.EqualFold(step.Source, "TOOL") || strings.EqualFold(step.Source, "ENVIRONMENT") || strings.EqualFold(step.Source, "BACKGROUND_TASK") {
		return true
	}
	return false
}

func extractUsage(step AgyStep) *AgyUsage {
	if step.Usage != nil {
		return step.Usage
	}
	if len(step.Content) > 0 && string(step.Content) != "null" {
		var contentMap map[string]any
		if err := json.Unmarshal(step.Content, &contentMap); err == nil {
			if u, ok := contentMap["usage"].(map[string]any); ok {
				var usage AgyUsage
				if it, ok := u["input_tokens"].(float64); ok {
					usage.InputTokens = int64(it)
				} else if it, ok := u["prompt_tokens"].(float64); ok {
					usage.InputTokens = int64(it)
				}
				if ot, ok := u["output_tokens"].(float64); ok {
					usage.OutputTokens = int64(ot)
				} else if ot, ok := u["completion_tokens"].(float64); ok {
					usage.OutputTokens = int64(ot)
				}
				if tt, ok := u["total_tokens"].(float64); ok {
					usage.TotalTokens = int64(tt)
				}
				if usage.TotalTokens == 0 {
					usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				}
				if usage.InputTokens > 0 || usage.OutputTokens > 0 {
					return &usage
				}
			}
		}
	}
	if strings.EqualFold(step.Type, "loadCodeAssist") && len(step.Content) > 0 {
		var usage AgyUsage
		if err := json.Unmarshal(step.Content, &usage); err == nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
			}
			return &usage
		}
	}
	return nil
}

func (b eventBuilder) turnEvent(turnID, clientNonce, eventType, reason string) map[string]any {
	payload := map[string]any{}
	if reason != "" {
		payload["reason"] = reason
	}
	event := map[string]any{
		"event_id":        turnID + ":" + eventType + ":" + stableIDPart(firstNonEmpty(reason, "runner")),
		"conversation_id": b.sessionID,
		"session_id":      b.sessionID,
		"turn_id":         turnID,
		"client_nonce":    clientNonce,
		"actor":           string(conversation.ActorRunner),
		"source":          provider,
		"type":            eventType,
		"producer": map[string]any{
			"name":    provider + "-runner",
			"runtime": provider,
		},
		"visibility": string(conversation.VisibilityDurable),
	}
	if len(payload) > 0 {
		event["payload"] = payload
	}
	return b.stamp(event)
}

func (b eventBuilder) turnCompletedEvent(turnID, clientNonce string, final finalAnswer, usage *AgyUsage) map[string]any {
	payload := map[string]any{
		"final_answer": map[string]any{
			"timeline_ids":      []string{final.timelineID},
			"provider_item_ids": []string{final.providerItemID},
		},
	}
	if usage != nil {
		payload["usage"] = map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  usage.TotalTokens,
		}
		payload["usage_observation"] = map[string]any{
			"usage_source":       "loadCodeAssist",
			"terminal_had_usage": true,
		}
	}
	return b.stamp(map[string]any{
		"event_id":        turnID + ":turn.completed:runner",
		"conversation_id": b.sessionID,
		"session_id":      b.sessionID,
		"turn_id":         turnID,
		"client_nonce":    clientNonce,
		"actor":           string(conversation.ActorRunner),
		"source":          provider,
		"type":            string(conversation.EventTurnCompleted),
		"producer": map[string]any{
			"name":    provider + "-runner",
			"runtime": provider,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload":    payload,
	})
}

func (b eventBuilder) turnUsageEvent(turnID, clientNonce, providerItemID string, usage *AgyUsage) map[string]any {
	return b.stamp(map[string]any{
		"event_id":        turnID + ":turn.usage:" + stableIDPart(providerItemID),
		"conversation_id": b.sessionID,
		"session_id":      b.sessionID,
		"turn_id":         turnID,
		"client_nonce":    clientNonce,
		"actor":           string(conversation.ActorRunner),
		"source":          provider,
		"type":            string(conversation.EventTurnUsage),
		"producer": map[string]any{
			"name":    provider + "-runner",
			"runtime": provider,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload": map[string]any{
			"usage": map[string]any{
				"input_tokens":  usage.InputTokens,
				"output_tokens": usage.OutputTokens,
				"total_tokens":  usage.TotalTokens,
			},
			"usage_observation": map[string]any{
				"usage_source":       "loadCodeAssist",
				"terminal_had_usage": false,
			},
		},
	})
}

func (b eventBuilder) turnFailedEvent(turnID, clientNonce, reason string, usage *AgyUsage) map[string]any {
	payload := map[string]any{
		"reason": reason,
	}
	if usage != nil {
		payload["usage"] = map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  usage.TotalTokens,
		}
		payload["usage_observation"] = map[string]any{
			"usage_source":       "loadCodeAssist",
			"terminal_had_usage": true,
		}
	}
	return b.stamp(map[string]any{
		"event_id":        turnID + ":turn.failed:runner",
		"conversation_id": b.sessionID,
		"session_id":      b.sessionID,
		"turn_id":         turnID,
		"client_nonce":    clientNonce,
		"actor":           string(conversation.ActorRunner),
		"source":          provider,
		"type":            string(conversation.EventTurnFailed),
		"producer": map[string]any{
			"name":    provider + "-runner",
			"runtime": provider,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload":    payload,
	})
}

func (b eventBuilder) turnInterruptedEvent(turnID, clientNonce string, usage *AgyUsage) map[string]any {
	payload := map[string]any{
		"reason": "user_interrupted",
	}
	if usage != nil {
		payload["usage"] = map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  usage.TotalTokens,
		}
		payload["usage_observation"] = map[string]any{
			"usage_source":       "loadCodeAssist",
			"terminal_had_usage": true,
		}
	}
	return b.stamp(map[string]any{
		"event_id":        turnID + ":turn.interrupted:runner",
		"conversation_id": b.sessionID,
		"session_id":      b.sessionID,
		"turn_id":         turnID,
		"client_nonce":    clientNonce,
		"actor":           string(conversation.ActorRunner),
		"source":          provider,
		"type":            string(conversation.EventTurnInterrupted),
		"producer": map[string]any{
			"name":    provider + "-runner",
			"runtime": provider,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload":    payload,
	})
}

func (b eventBuilder) assistantMessageEvent(turnID, providerItemID, timelineID, text string) map[string]any {
	return b.stamp(map[string]any{
		"event_id":         turnID + ":assistant_message.created:" + stableIDPart(providerItemID),
		"conversation_id":  b.sessionID,
		"session_id":       b.sessionID,
		"turn_id":          turnID,
		"timeline_id":      timelineID,
		"provider_item_id": providerItemID,
		"actor":            string(conversation.ActorAssistant),
		"source":           provider,
		"type":             string(conversation.EventAssistantMessageCreated),
		"producer": map[string]any{
			"name":              provider + "-runner",
			"runtime":           provider,
			"provider_event_id": providerItemID,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload": map[string]any{
			"text":    text,
			"message": map[string]any{"role": "assistant", "content": text},
			"display": map[string]any{"kind": "plain"},
		},
	})
}

func (b eventBuilder) itemEvent(turnID, providerItemID, eventType, actor string, payload map[string]any) map[string]any {
	return b.stamp(map[string]any{
		"event_id":         turnID + ":" + eventType + ":" + stableIDPart(providerItemID),
		"conversation_id":  b.sessionID,
		"session_id":       b.sessionID,
		"turn_id":          turnID,
		"timeline_id":      itemTimelineID(turnID, providerItemID),
		"provider_item_id": providerItemID,
		"parent_id":        turnID,
		"actor":            actor,
		"source":           provider,
		"type":             eventType,
		"producer": map[string]any{
			"name":              provider + "-runner",
			"runtime":           provider,
			"provider_event_id": providerItemID,
		},
		"visibility": string(conversation.VisibilityDurable),
		"payload":    payload,
	})
}

func (b eventBuilder) stamp(event map[string]any) map[string]any {
	event["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if b.sessionStorageKey != "" {
		event["tank_session_id"] = b.sessionStorageKey
	}
	if b.sessionID != "" {
		event["tank_public_session_id"] = b.sessionID
	}
	if b.ownerEmail != "" {
		event["email"] = b.ownerEmail
	}
	event["runtime"] = provider
	return conversation.StampEventMap(event)
}

func publishEvent(nc *nats.Conn, sessionStorageKey string, event map[string]any) error {
	if err := conversation.ValidateEventMap(event); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := nc.Publish(sessionbus.SessionEventSubject(sessionStorageKey), data); err != nil {
		return err
	}
	return nc.FlushTimeout(5 * time.Second)
}


func tailTranscripts(ctx context.Context, cfg runnerConfig, state *runnerState) {
	offsets := map[string]int64{}
	brainDir := filepath.Join(cfg.agyHome, "brain")

	os.MkdirAll(brainDir, 0755)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("failed to create fsnotify watcher", "error", err)
		return
	}
	defer watcher.Close()

	watchAll := func(dir string) {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				watcher.Add(path)
			}
			return nil
		})
	}
	watchAll(brainDir)

	for {
		select {
		case <-ctx.Done():
			_ = sweepTranscripts(brainDir, offsets, cfg, state)
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
			if err := sweepTranscripts(brainDir, offsets, cfg, state); err != nil {
				slog.Error("failed to sweep transcripts", "error", err)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("fsnotify error", "error", err)
		}
	}
}

func sweepTranscripts(brainDir string, offsets map[string]int64, cfg runnerConfig, state *runnerState) error {
	return filepath.Walk(brainDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "transcript_full.jsonl") {
			return nil
		}
		size := info.Size()
		offset := offsets[path]
		if size < offset {
			offset = 0
		}
		if size <= offset {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			var step AgyStep
			if err := json.Unmarshal([]byte(line), &step); err != nil {
				continue
			}
			if err := state.handleStep(path, line, step, cfg); err != nil {
				return err
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		offsets[path] = size
		return nil
	})
}

func providerStepID(path, line string, step AgyStep) string {
	if strings.TrimSpace(step.ConversationID) != "" || step.StepIndex != 0 || strings.TrimSpace(step.Type) != "" {
		return fmt.Sprintf("agy:%s:%d:%s:%s", stableIDPart(firstNonEmpty(step.ConversationID, path)), step.StepIndex, strings.ToLower(step.Source), strings.ToLower(step.Type))
	}
	return "agy:" + stableIDPart(path+":"+line)
}

func itemTimelineID(turnID, providerItemID string) string {
	return turnID + ":item:" + stableIDPart(providerItemID)
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var parts []any
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if text := textFromAny(part); text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err == nil {
		return strings.TrimSpace(textFromAny(record))
	}
	return ""
}

func textFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

var stableUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)
var stableDash = regexp.MustCompile(`-+`)

func stableIDPart(value string) string {
	trimmed := strings.TrimSpace(value)
	safe := stableUnsafe.ReplaceAllString(trimmed, "-")
	safe = stableDash.ReplaceAllString(safe, "-")
	safe = strings.Trim(safe, "-")
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])[:12]
	if len(safe) >= 6 && len(safe) <= 80 {
		return safe
	}
	if len(safe) > 80 {
		return safe[:64] + "-" + hash
	}
	return hash
}
