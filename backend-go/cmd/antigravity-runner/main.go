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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

const provider = "antigravity"

// Liveness metrics. Every wait in handleSubmitTurn that can resolve a turn
// has a counter, so "how often does X happen" never requires log archaeology.
// docs/observability.md carries the taxonomy entry for each name.
var (
	providerErrorTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_antigravity_runner_provider_error_total",
		Help: "Durable turn.failed terminals published by the antigravity runner, by reason.",
	}, []string{"reason"})
	interruptOutcomeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_antigravity_runner_interrupt_outcome_total",
		Help: "How Stop interrupts against agy resolved (graceful_done, grace_forced, process_exited).",
	}, []string{"outcome"})
	processExitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_antigravity_runner_process_exit_total",
		Help: "agy process exits observed by the runner, by phase (during_turn, idle).",
	}, []string{"phase"})
	submitWatchdogTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_antigravity_runner_submit_watchdog_total",
		Help: "Submit-ack watchdog resolutions (cleared, fired).",
	}, []string{"result"})
	providerFatalReportTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_antigravity_runner_provider_fatal_report_total",
		Help: "Provider-fatal reports posted to the orchestrator, by result (ok, error).",
	}, []string{"result"})
)

func serveMetrics(port string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go func() {
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			slog.Error("metrics server exited", "port", port, "error", err)
		}
	}()
}

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
	// submitAckTimeout bounds how long a submitted prompt may sit with no
	// transcript movement before the turn resolves as a durable
	// turn.failed{prompt_not_accepted}. Any new transcript record clears
	// it (the USER_EXPLICIT prompt echo is the usual first signal). There
	// is intentionally no auto-retry: re-writing the prompt to the PTY
	// risks double-execution if agy did receive the first write.
	submitAckTimeout time.Duration
	// interruptGrace bounds how long a Stop may wait for agy to settle
	// (DONE planner response or process exit) before the runner forces
	// the durable turn.interrupted terminal anyway, mirroring the
	// codex-runner's "continue with durable Stop terminal" behavior.
	interruptGrace time.Duration
	// metricsPort serves Prometheus /metrics (TANK_RUNNER_METRICS_PORT).
	metricsPort string
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
	builder      eventBuilder
	publish      func(map[string]any) error
	turnID       string
	clientNonce  string
	turnComplete chan struct{}
	completeOnce sync.Once
	// progress is closed on the first transcript record observed after
	// submit (any record, including the USER_EXPLICIT prompt echo — the
	// cleanest "agy received the prompt" signal). The submit-ack watchdog
	// waits on it.
	progress     chan struct{}
	progressOnce sync.Once
	// graceFired is closed when an interrupt's grace window elapses
	// without agy settling; the turn then resolves as turn.interrupted.
	graceFired    chan struct{}
	graceArmOnce  sync.Once
	graceFireOnce sync.Once

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
	// onInterrupt is armed per turn by handleSubmitTurn; firing it starts
	// the interrupt-grace countdown so a Stop that agy never acknowledges
	// (no DONE planner response, no process exit) still resolves in a
	// durable turn.interrupted instead of hanging the data plane.
	onInterrupt func()
	// exited is closed exactly once by the cmd.Wait supervisor when the
	// agy process is gone. Process death is session-terminal by design
	// (no revival architecture); exitErr carries the Wait error for the
	// provider-fatal report.
	exited   chan struct{}
	exitOnce sync.Once
	exitErr  error
}

func newActiveProcess() *activeProcess {
	return &activeProcess{exited: make(chan struct{})}
}

func (a *activeProcess) beginTurn(turnID string, onInterrupt func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.turnID = turnID
	a.interrupted = false
	a.onInterrupt = onInterrupt
}

func (a *activeProcess) endTurn(turnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turnID == turnID {
		a.turnID = ""
		a.onInterrupt = nil
	}
}

func (a *activeProcess) markExited(err error) {
	a.exitOnce.Do(func() {
		a.mu.Lock()
		a.exitErr = err
		a.mu.Unlock()
		close(a.exited)
	})
}

func (a *activeProcess) exitedChan() <-chan struct{} { return a.exited }

func (a *activeProcess) isDead() bool {
	select {
	case <-a.exited:
		return true
	default:
		return false
	}
}

// exitDetail reports the recorded Wait error as (exit code, message). A nil
// error (clean exit 0) returns (0, ""); a non-ExitError failure returns -1.
func (a *activeProcess) exitDetail() (int, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.exitErr == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	if errors.As(a.exitErr, &exitErr) {
		return exitErr.ExitCode(), exitErr.Error()
	}
	return -1, a.exitErr.Error()
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
	serveMetrics(cfg.metricsPort)

	active := newActiveProcess()
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

	// Process supervisor: agy death is session-terminal by design (see
	// ARCHITECTURE.md — there is no revival architecture). When agy
	// exits, an in-flight turn resolves through the exitedChan select arm
	// in handleSubmitTurn, the session row moves to Failed through the
	// orchestrator's provider-fatal endpoint, and this runner stays alive
	// but inert so queued/new submit_turns drain to durable failures
	// instead of stranding (a container exit would let kubelet restart
	// agy with amnesia, which is exactly the revival we do not do).
	go func() {
		waitErr := runCmd.Wait()
		phase := "idle"
		active.mu.Lock()
		if active.turnID != "" {
			phase = "during_turn"
		}
		active.mu.Unlock()
		active.markExited(waitErr)
		processExitTotal.WithLabelValues(phase).Inc()
		exitCode, detail := active.exitDetail()
		slog.Error("agy process exited; session is provider-fatal by design",
			"phase", phase, "exit_code", exitCode, "detail", detail)
		if err := reportProviderFatal(cfg, "provider_process_exited", exitCode, detail); err != nil {
			providerFatalReportTotal.WithLabelValues("error").Inc()
			slog.Error("failed to report provider-fatal to orchestrator", "error", err)
		} else {
			providerFatalReportTotal.WithLabelValues("ok").Inc()
		}
	}()

	// This loop's only job is to drain the PTY (agy blocks once the PTY
	// buffer fills) and mirror agy's output to pod logs. Onboarding/consent
	// screens are prevented up-front by the launcher seeding onboarding
	// state into both agy config dirs
	// (antigravity-container/antigravity-runner-launch.sh). Do not re-add
	// PTY-stdout sniffing that replays keystrokes (the retired
	// auto-accept): it races real turn input and breaks on any TUI copy
	// change. If a new interactive screen appears, extend the seeded
	// config files instead. Guarded by TestPTYRunnerArchitectureConstraint
	// and scripts/check-removed-chat-runtime.mjs.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				break
			}
			os.Stdout.Write(buf[:n])
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
	baseURL := strings.TrimRight(firstEnv("TANK_OPERATOR_INTERNAL_URL", "OPERATOR_INTERNAL_URL", "http://tank-operator.tank-operator.svc.cluster.local"), "/")
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

// reportProviderFatal tells the orchestrator the agy process is gone so the
// session row moves to Failed (the same terminal the K8s watch applies for
// pod death). Bounded retries because this single call is what separates
// "session visibly done" from "session looks alive but every turn fails";
// the per-turn durable terminals do not depend on it succeeding.
func reportProviderFatal(cfg runnerConfig, reason string, exitCode int, message string) error {
	baseURL := strings.TrimRight(firstNonEmpty(
		firstEnv("TANK_OPERATOR_INTERNAL_URL", "OPERATOR_INTERNAL_URL"),
		"http://tank-operator.tank-operator.svc.cluster.local"), "/")
	tokenPath := firstNonEmpty(firstEnv("OPERATOR_TOKEN_PATH"),
		"/var/run/secrets/kubernetes.io/serviceaccount/token")
	if cfg.sessionID == "" {
		return errors.New("provider-fatal report requires a session id")
	}
	payload := map[string]any{
		"provider":  provider,
		"reason":    reason,
		"exit_code": exitCode,
		"message":   message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/internal/sessions/%s/provider-fatal", baseURL, cfg.sessionID)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		tokenBytes, err := os.ReadFile(tokenPath)
		if err != nil {
			lastErr = err
			continue
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenBytes)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}
		lastErr = fmt.Errorf("provider-fatal report failed: %d", resp.StatusCode)
	}
	return lastErr
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
		submitAckTimeout:  envDurationMS("ANTIGRAVITY_SUBMIT_ACK_TIMEOUT_MS", 60*time.Second),
		interruptGrace:    envDurationMS("ANTIGRAVITY_INTERRUPT_GRACE_MS", 10*time.Second),
		metricsPort:       firstNonEmpty(strings.TrimSpace(os.Getenv("TANK_RUNNER_METRICS_PORT")), "9097"),
	}, nil
}

func envDurationMS(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		slog.Warn("invalid duration env, using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
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

	run := newTurnRun(builder, publisher, turnID, clientNonce)
	if err := publisher(builder.turnEvent(turnID, clientNonce, string(conversation.EventTurnClaimed), "")); err != nil {
		return false, err
	}

	// Inert mode: agy is gone and the session is provider-fatal (marked
	// Failed via the orchestrator). Drain the command to a durable
	// failure instead of stranding it un-acked — "provider failures must
	// become durable failure events instead of silent strandings."
	if active.isDead() {
		if err := run.finishFailed("provider_process_unavailable"); err != nil {
			return false, err
		}
		if err := msg.Ack(); err != nil {
			return false, err
		}
		return false, nil
	}

	grace := cfg.interruptGrace
	active.beginTurn(turnID, func() { run.armInterruptGrace(grace) })
	defer active.endTurn(turnID)

	stopHeartbeat := startHeartbeat(ctx, msg)
	defer stopHeartbeat()

	_, err := ptmx.WriteString(command.Prompt + "\r")
	if err != nil {
		_ = run.finishFailed("failed_to_start")
		_ = msg.Ack()
		return false, nil
	}

	state.attachTurn(run)
	defer state.detachTurn(run)

	// Submit-ack watchdog: the prompt write is fire-and-forget into the
	// PTY, so "no transcript movement at all" within the window means the
	// prompt was swallowed (TUI focus/redraw race). Resolve durably; no
	// auto-retry, because a re-written prompt double-executes if agy did
	// receive the first one.
	watchdogFired := make(chan struct{})
	turnDone := make(chan struct{})
	defer close(turnDone)
	go func() {
		select {
		case <-run.progress:
			submitWatchdogTotal.WithLabelValues("cleared").Inc()
		case <-turnDone:
		case <-time.After(cfg.submitAckTimeout):
			select {
			case <-run.progress:
				submitWatchdogTotal.WithLabelValues("cleared").Inc()
			default:
				submitWatchdogTotal.WithLabelValues("fired").Inc()
				close(watchdogFired)
			}
		}
	}()

	// Exactly one select arm resolves the turn, and every arm publishes
	// exactly one durable terminal before the command is acked. This is
	// the structural fix for the silent-stranding class: the old wait had
	// a single exit (a DONE planner response) that an agy crash, a
	// swallowed prompt, or an unacknowledged Stop could keep from ever
	// arriving while the heartbeat kept the command pinned forever.
	var terminalErr error
	select {
	case <-run.turnComplete:
		if active.wasInterrupted(turnID) {
			terminalErr = run.finishInterrupted("graceful_done")
		} else {
			terminalErr = run.finishCompleted()
		}
	case <-active.exitedChan():
		if active.wasInterrupted(turnID) {
			terminalErr = run.finishInterrupted("process_exited")
		} else {
			terminalErr = run.finishFailed("provider_process_exited")
		}
	case <-watchdogFired:
		terminalErr = run.finishFailed("prompt_not_accepted")
	case <-run.graceFired:
		terminalErr = run.finishInterrupted("grace_forced")
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

func (a *activeProcess) interrupt(targetTurnID string) error {
	a.mu.Lock()
	if a.turnID == "" {
		// No active turn: nothing to interrupt. Do not SIGINT an idle
		// agy — that would be a session-terminal event for no reason.
		a.mu.Unlock()
		return nil
	}
	if targetTurnID != "" && targetTurnID != a.turnID {
		a.mu.Unlock()
		return nil
	}
	a.interrupted = true
	notify := a.onInterrupt
	cmd := a.cmd
	a.mu.Unlock()
	// Arm the grace countdown before signaling: even if the SIGINT is
	// lost on a just-dead process, the turn still resolves durably.
	if notify != nil {
		notify()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
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
		progress:     make(chan struct{}),
		graceFired:   make(chan struct{}),
		seen:         map[string]struct{}{},
	}
}

func (r *turnRun) noteProgress() {
	r.progressOnce.Do(func() { close(r.progress) })
}

func (r *turnRun) markComplete() {
	r.completeOnce.Do(func() { close(r.turnComplete) })
}

// armInterruptGrace starts the bounded wait between a Stop and a forced
// durable turn.interrupted. Armed at most once per turn; firing after the
// turn already resolved another way is harmless (the select has returned).
func (r *turnRun) armInterruptGrace(d time.Duration) {
	r.graceArmOnce.Do(func() {
		time.AfterFunc(d, func() {
			r.graceFireOnce.Do(func() { close(r.graceFired) })
		})
	})
}

func (r *turnRun) observeStep(path, line string, step AgyStep) error {
	// Any transcript record — relevant or not, including the USER_EXPLICIT
	// prompt echo — proves agy is processing; clear the submit watchdog.
	r.noteProgress()
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
		// Remember that the provider surfaced an executor error. A turn
		// that still produces assistant prose completes normally; one
		// that ends with no final answer is then classified as
		// provider_executor_error instead of provider_no_final_answer
		// (the agent-runners capabilities ledger's distinction).
		r.providerFailed = "provider_executor_error"
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
				r.markComplete()
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
	executorError := r.providerFailed
	r.mu.Unlock()
	if final.timelineID == "" {
		// Tool activity alone is not a successful user answer. When the
		// provider also surfaced an executor error, classify the failure
		// as such; otherwise it is a plain no-final-answer exit.
		reason := "provider_no_final_answer"
		if executorError != "" {
			reason = executorError
		}
		return r.finishFailed(reason)
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
	providerErrorTotal.WithLabelValues(reason).Inc()
	return r.publish(r.builder.turnFailedEvent(r.turnID, r.clientNonce, reason, usage))
}

func (r *turnRun) finishInterrupted(outcome string) error {
	if err := r.ensureStarted("runner_terminal"); err != nil {
		return err
	}
	r.mu.Lock()
	usage := r.cumulativeUsage
	r.mu.Unlock()
	interruptOutcomeTotal.WithLabelValues(outcome).Inc()
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
