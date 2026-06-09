package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/localharness"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"google.golang.org/protobuf/proto"
)

const provider = "antigravity"

type AgyStep struct {
	StepIndex      int               `json:"step_index"`
	Source         string            `json:"source"`
	Type           string            `json:"type"`
	Status         string            `json:"status"`
	Content        json.RawMessage   `json:"content"`
	ToolCalls      []json.RawMessage `json:"tool_calls"`
	ConversationID string            `json:"conversation_id,omitempty"`
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

type turnRun struct {
	builder      eventBuilder
	publish      func(map[string]any) error
	turnID       string
	clientNonce  string
	turnComplete chan struct{}

	mu             sync.Mutex
	started        bool
	seen           map[string]struct{}
	final          finalAnswer
	providerFailed string
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
	
	// Spawn agy without -p
	agyArgs := []string{"--dangerously-skip-permissions"}
	runCmd := exec.CommandContext(ctx, "agy", agyArgs...)
	runCmd.Dir = cfg.workspace
	runCmd.Env = os.Environ()

	stdin, err := runCmd.StdinPipe()
	if err != nil {
		slog.Error("Failed to create stdin pipe", "error", err)
		os.Exit(1)
	}
	stdout, err := runCmd.StdoutPipe()
	if err != nil {
		slog.Error("Failed to create stdout pipe", "error", err)
		os.Exit(1)
	}
	runCmd.Stderr = os.Stderr

	if err := runCmd.Start(); err != nil {
		slog.Error("Failed to start agy", "error", err)
		os.Exit(1)
	}
	active.set(runCmd, "")

	// 1. Write InputConfig to stdin
	inputConfig := &localharness.InputConfig{
		StorageDirectory: cfg.agyHome,
		ClientInfo: &localharness.ClientInfo{
			Language:        "go",
			Version:         "1.0.0",
			LanguageVersion: "1.22",
		},
	}
	b, err := proto.Marshal(inputConfig)
	if err != nil {
		slog.Error("Failed to marshal InputConfig", "error", err)
		os.Exit(1)
	}
	sizeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(sizeBuf, uint32(len(b)))
	if _, err := stdin.Write(sizeBuf); err != nil {
		slog.Error("Failed to write InputConfig size", "error", err)
		os.Exit(1)
	}
	if _, err := stdin.Write(b); err != nil {
		slog.Error("Failed to write InputConfig body", "error", err)
		os.Exit(1)
	}

	// 2. Read OutputConfig from stdout
	if _, err := io.ReadFull(stdout, sizeBuf); err != nil {
		slog.Error("Failed to read OutputConfig size", "error", err)
		os.Exit(1)
	}
	size := binary.LittleEndian.Uint32(sizeBuf)
	outBuf := make([]byte, size)
	if _, err := io.ReadFull(stdout, outBuf); err != nil {
		slog.Error("Failed to read OutputConfig body", "error", err)
		os.Exit(1)
	}
	outputConfig := &localharness.OutputConfig{}
	if err := proto.Unmarshal(outBuf, outputConfig); err != nil {
		slog.Error("Failed to unmarshal OutputConfig", "error", err)
		os.Exit(1)
	}

	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/", outputConfig.Port)
	slog.Info("Connecting to localharness", "url", wsURL)

	headers := map[string][]string{
		"x-goog-api-key": {outputConfig.ApiKey},
	}
	dialer := websocket.DefaultDialer
	ws, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		slog.Error("Failed to connect to localharness websocket", "error", err)
		os.Exit(1)
	}
	defer ws.Close()

	// 3. Send InitializeConversationEvent
	initMsg := map[string]any{
		"initialize_conversation": map[string]any{
			"config": map[string]any{
				"is_resume": false,
			},
		},
	}
	if err := ws.WriteJSON(initMsg); err != nil {
		slog.Error("Failed to send initialize_conversation", "error", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	
	// Create a global current run pointer so the websocket reader can emit to it
	var currentRunMu sync.Mutex
	var currentRun *turnRun

	// Consumer for the websocket
	go func() {
		defer wg.Done()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				slog.Error("websocket read error", "error", err)
				cancel()
				return
			}
			
			var outputEvent map[string]any
			if err := json.Unmarshal(msg, &outputEvent); err != nil {
				continue
			}

			currentRunMu.Lock()
			run := currentRun
			currentRunMu.Unlock()
			
			if run == nil {
				continue
			}

			var stepUpdate any
			var ok bool
			if stepUpdate, ok = outputEvent["step_update"]; !ok {
				stepUpdate, ok = outputEvent["stepUpdate"]
			}

			if ok {
				if suMap, isMap := stepUpdate.(map[string]any); isMap {
					// Parse it into an AgyStep
					b, _ := json.Marshal(suMap)
					var step AgyStep
					_ = json.Unmarshal(b, &step)
					_ = run.observeStep("ws", string(msg), step)
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		runDataConsumer(ctx, js, cfg, builder, publisher, active, ws, &currentRun, &currentRunMu)
	}()
	go func() {
		defer wg.Done()
		runControlConsumer(ctx, js, cfg, active)
	}()
	wg.Wait()
	slog.Info("antigravity cli runner exited")
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

func runDataConsumer(ctx context.Context, js jetstream.JetStream, cfg runnerConfig, builder eventBuilder, publisher func(map[string]any) error, active *activeProcess, ws *websocket.Conn, currentRun **turnRun, currentRunMu *sync.Mutex) {
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
		started, err := handleSubmitTurn(ctx, cfg, builder, publisher, active, msg, command, continueConversation, ws, currentRun, currentRunMu)
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

func handleSubmitTurn(ctx context.Context, cfg runnerConfig, builder eventBuilder, publisher func(map[string]any) error, active *activeProcess, msg jetstream.Msg, command sessionbus.Command, continueConversation bool, ws *websocket.Conn, currentRun **turnRun, currentRunMu *sync.Mutex) (bool, error) {
	turnID := command.TurnID
	if turnID == "" {
		turnID = "turn_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	clientNonce := command.ClientNonce
	if clientNonce == "" {
		clientNonce = turnID
	}

	run := newTurnRun(builder, publisher, turnID, clientNonce)
	if err := publisher(builder.turnEvent(turnID, clientNonce, string(conversation.EventTurnClaimed), "")); err != nil {
		return false, err
	}

	currentRunMu.Lock()
	*currentRun = run
	currentRunMu.Unlock()

	stopHeartbeat := startHeartbeat(ctx, msg)
	defer stopHeartbeat()

	promptMsg := map[string]any{
		"user_input": map[string]any{
			"text": command.Prompt,
		},
	}
	if err := ws.WriteJSON(promptMsg); err != nil {
		_ = publisher(builder.turnEvent(turnID, clientNonce, string(conversation.EventTurnFailed), "failed_to_start"))
		_ = msg.Ack()
		return false, nil
	}

	<-run.turnComplete
	interrupted := false

	var terminalErr error
	switch {
	case interrupted:
		terminalErr = run.finishInterrupted()
	case run.providerFailed != "":
		terminalErr = run.finishFailed("provider_error")
	default:
		terminalErr = run.finishCompleted()
	}
	
	currentRunMu.Lock()
	*currentRun = nil
	currentRunMu.Unlock()

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

	text := contentText(step.Content)
	if strings.EqualFold(step.Source, "SYSTEM") && strings.EqualFold(step.Type, "ERROR_MESSAGE") {
		r.mu.Lock()
		if text != "" {
			r.providerFailed = text
		} else {
			r.providerFailed = "system_error"
		}
		r.mu.Unlock()
		return r.publish(r.builder.itemEvent(r.turnID, providerID, string(conversation.EventItemFailed), string(conversation.ActorRunner), map[string]any{
			"kind": "system_error",
			"text": firstNonEmpty(text, "Antigravity reported an error."),
			"outcome": map[string]any{
				"kind":   "execution_failed",
				"reason": "provider_item_error",
			},
		}))
	}

	if !strings.EqualFold(step.Type, "PLANNER_RESPONSE") || !strings.EqualFold(step.Status, "DONE") || text == "" || len(step.ToolCalls) > 0 {
		return nil
	}

	timelineID := itemTimelineID(r.turnID, providerID)
	event := r.builder.assistantMessageEvent(r.turnID, providerID, timelineID, text)
	if err := r.publish(event); err != nil {
		return err
	}
	r.mu.Lock()
	r.final = finalAnswer{timelineID: timelineID, providerItemID: providerID}
	r.mu.Unlock()
	
	// signal completion, but don't panic if closed twice
	select {
	case <-r.turnComplete:
	default:
		close(r.turnComplete)
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
	r.mu.Unlock()
	if final.timelineID == "" {
		return r.finishFailed("provider_no_final_answer")
	}
	return r.publish(r.builder.turnCompletedEvent(r.turnID, r.clientNonce, final))
}

func (r *turnRun) finishFailed(reason string) error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	return r.publish(r.builder.turnEvent(r.turnID, r.clientNonce, string(conversation.EventTurnFailed), reason))
}

func (r *turnRun) finishInterrupted() error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	return r.publish(r.builder.turnEvent(r.turnID, r.clientNonce, string(conversation.EventTurnInterrupted), "user_interrupted"))
}

func isRelevantStep(step AgyStep) bool {
	if strings.EqualFold(step.Source, "USER_EXPLICIT") {
		return false
	}
	if strings.EqualFold(step.Source, "SYSTEM") && strings.EqualFold(step.Type, "ERROR_MESSAGE") {
		return true
	}
	return strings.EqualFold(step.Type, "PLANNER_RESPONSE")
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

func (b eventBuilder) turnCompletedEvent(turnID, clientNonce string, final finalAnswer) map[string]any {
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
		"payload": map[string]any{
			"final_answer": map[string]any{
				"timeline_ids":      []string{final.timelineID},
				"provider_item_ids": []string{final.providerItemID},
			},
		},
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
