/*
WARNING: TestPTYRunnerArchitectureConstraint is an explicit structural constraint check.
DO NOT remove or disable this test, and DO NOT modify it to permit websocket, gRPC,
or localharness references/imports in main.go.

This test is designed to prevent agents and developers from short-circuiting the PTY wrapper
architecture (which is a hard production requirement due to agy's CLI-only nature and consumer OAuth).
*/

package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

func TestEventBuilderEmitsSchemaValidTurnEvents(t *testing.T) {
	builder := eventBuilder{
		sessionID:         "public-17",
		sessionStorageKey: "tank-operator-slot-3:17",
		ownerEmail:        "owner@example.com",
	}
	final := finalAnswer{
		timelineID:     itemTimelineID("turn-1", "agy:step:1"),
		providerItemID: "agy:step:1",
	}

	events := []map[string]any{
		builder.turnEvent("turn-1", "nonce-1", string(conversation.EventTurnClaimed), ""),
		builder.turnEvent("turn-1", "nonce-1", string(conversation.EventTurnStarted), "agy:step:1"),
		builder.assistantMessageEvent("turn-1", final.providerItemID, final.timelineID, "done"),
		builder.turnCompletedEvent("turn-1", "nonce-1", final, nil, false),
		builder.turnCompletedEvent("turn-1b", "nonce-1b", final, nil, true),
		builder.turnEvent("turn-2", "nonce-2", string(conversation.EventTurnFailed), "provider_no_final_answer"),
		builder.turnEvent("turn-3", "nonce-3", string(conversation.EventTurnInterrupted), "user_interrupted"),
		builder.itemEvent("turn-4", "agy:error:1", string(conversation.EventItemFailed), string(conversation.ActorRunner), map[string]any{
			"kind": "system_error",
			"text": "boom",
			"outcome": map[string]any{
				"kind":   "execution_failed",
				"reason": "provider_item_error",
			},
		}),
	}

	for _, event := range events {
		if err := conversation.ValidateEventMap(event); err != nil {
			t.Fatalf("event %s failed validation: %v\n%v", event["type"], err, event)
		}
		if got := event["uuid"]; got != event["event_id"] {
			t.Fatalf("uuid = %v, want event_id %v", got, event["event_id"])
		}
		if got := event["tank_session_id"]; got != "tank-operator-slot-3:17" {
			t.Fatalf("tank_session_id = %v", got)
		}
		if got := event["tank_public_session_id"]; got != "public-17" {
			t.Fatalf("tank_public_session_id = %v", got)
		}
		if got := event["email"]; got != "owner@example.com" {
			t.Fatalf("email = %v", got)
		}
	}
}

func TestTurnRunMapsPlannerResponseToAssistantAndFinalAnswer(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	var events []map[string]any
	run := newTurnRun(builder, func(event map[string]any) error {
		if err := conversation.ValidateEventMap(event); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	}, "turn-1", "nonce-1")

	line := `{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"hello from agy"}`
	var step AgyStep
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", line, step); err != nil {
		t.Fatal(err)
	}
	if err := run.finishCompleted(); err != nil {
		t.Fatal(err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %#v", len(events), events)
	}
	if events[0]["type"] != string(conversation.EventTurnStarted) {
		t.Fatalf("first event type = %v", events[0]["type"])
	}
	if events[1]["type"] != string(conversation.EventItemCompleted) {
		t.Fatalf("second event type = %v, want item.completed", events[1]["type"])
	}
	if events[1]["actor"] != "assistant" {
		t.Fatalf("second event actor = %v, want assistant", events[1]["actor"])
	}
	if events[2]["type"] != string(conversation.EventTurnCompleted) {
		t.Fatalf("third event type = %v", events[2]["type"])
	}
	payload := events[2]["payload"].(map[string]any)
	final := payload["final_answer"].(map[string]any)
	if got := final["timeline_ids"].([]string); len(got) != 1 || !strings.Contains(got[0], "turn-1:item:") {
		t.Fatalf("timeline_ids = %#v", got)
	}
}

func TestTurnRunToolAndTokenUsageParity(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	var events []map[string]any
	run := newTurnRun(builder, func(event map[string]any) error {
		if err := conversation.ValidateEventMap(event); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	}, "turn-1", "nonce-1")

	// Step 1: Model outputs prose and a tool call
	step1JSON := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"I need to run a command.","tool_calls":[{"name":"run_command","args":{"CommandLine":"echo hello","Cwd":"/workspace"},"toolAction":"Running echo hello","toolSummary":"Run echo command"}]}`
	var step1 AgyStep
	if err := json.Unmarshal([]byte(step1JSON), &step1); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step1JSON, step1); err != nil {
		t.Fatal(err)
	}

	// Step 2: System-level loadCodeAssist step carrying token usage
	step2JSON := `{"step_index":2,"source":"SYSTEM","type":"loadCodeAssist","status":"DONE","content":{"usage":{"input_tokens":150,"output_tokens":80,"total_tokens":230}}}`
	var step2 AgyStep
	if err := json.Unmarshal([]byte(step2JSON), &step2); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step2JSON, step2); err != nil {
		t.Fatal(err)
	}

	// Step 3: Tool result step (success)
	step3JSON := `{"step_index":3,"source":"MODEL","type":"run_command","status":"DONE","content":"hello\n"}`
	var step3 AgyStep
	if err := json.Unmarshal([]byte(step3JSON), &step3); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step3JSON, step3); err != nil {
		t.Fatal(err)
	}

	// Step 4: Another tool call generated in step 4
	step4JSON := `{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/workspace/test.txt"}}]}`
	var step4 AgyStep
	if err := json.Unmarshal([]byte(step4JSON), &step4); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step4JSON, step4); err != nil {
		t.Fatal(err)
	}

	// Step 5: System-level error message step (fails the last pending tool call: view_file)
	step5JSON := `{"step_index":5,"source":"SYSTEM","type":"ERROR_MESSAGE","status":"ERROR","content":"file not found"}`
	var step5 AgyStep
	if err := json.Unmarshal([]byte(step5JSON), &step5); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step5JSON, step5); err != nil {
		t.Fatal(err)
	}

	// Step 6: Final Planner Response (Done, turn completed)
	step6JSON := `{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"All done!"}`
	var step6 AgyStep
	if err := json.Unmarshal([]byte(step6JSON), &step6); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", step6JSON, step6); err != nil {
		t.Fatal(err)
	}

	if err := run.finishCompleted(); err != nil {
		t.Fatal(err)
	}

	var eventTypes []string
	for _, ev := range events {
		eventTypes = append(eventTypes, ev["type"].(string))
	}
	t.Logf("Observed event sequence: %v", eventTypes)

	expectedTypes := []string{
		"turn.started",
		"item.completed", // assistant prose 1
		"item.started",   // run_command tool started
		"turn.usage",     // token usage update
		"item.completed", // run_command tool result
		"item.started",   // view_file tool started
		"item.failed",    // view_file failed via system error
		"item.completed", // assistant prose 2
		"turn.completed",
	}

	if len(events) != len(expectedTypes) {
		t.Fatalf("got %d events, want %d", len(events), len(expectedTypes))
	}

	for i, expected := range expectedTypes {
		if events[i]["type"] != expected {
			t.Errorf("event %d: got type %q, want %q", i, events[i]["type"], expected)
		}
	}

	// Verify tool started payload
	tcStarted := events[2]
	if tcStarted["actor"] != "tool" {
		t.Errorf("tool started actor = %v, want tool", tcStarted["actor"])
	}
	tcStartedPayload := tcStarted["payload"].(map[string]any)
	if tcStartedPayload["name"] != "run_command" {
		t.Errorf("tool started name = %v", tcStartedPayload["name"])
	}
	if tcStartedPayload["title"] != "Run echo command" {
		t.Errorf("tool started title = %v", tcStartedPayload["title"])
	}

	// Verify tool result payload
	tcResult := events[4]
	if tcResult["actor"] != "tool" {
		t.Errorf("tool actor = %v, want tool", tcResult["actor"])
	}
	tcResultPayload := tcResult["payload"].(map[string]any)
	if tcResultPayload["kind"] != "tool_result" {
		t.Errorf("tool result kind = %v", tcResultPayload["kind"])
	}
	if tcResultPayload["output"] != "hello" {
		t.Errorf("tool result output = %q", tcResultPayload["output"])
	}

	// Verify view_file system failure event ID is matched
	viewFileStarted := events[5]
	viewFileFailed := events[6]
	if viewFileFailed["provider_item_id"] != viewFileStarted["provider_item_id"] {
		t.Errorf("view_file failed provider_item_id = %v, want matching started provider_item_id = %v", viewFileFailed["provider_item_id"], viewFileStarted["provider_item_id"])
	}

	// Verify turnCompleted has final answer and token usage
	completed := events[8]
	completedPayload := completed["payload"].(map[string]any)

	finalAnswer := completedPayload["final_answer"].(map[string]any)
	if len(finalAnswer["timeline_ids"].([]string)) != 1 {
		t.Errorf("timeline_ids length = %d, want 1", len(finalAnswer["timeline_ids"].([]string)))
	}

	// Token usage check
	usage := completedPayload["usage"].(map[string]any)
	if usage["input_tokens"].(int64) != 150 {
		t.Errorf("usage input_tokens = %v, want 150", usage["input_tokens"])
	}
	if usage["output_tokens"].(int64) != 80 {
		t.Errorf("usage output_tokens = %v, want 80", usage["output_tokens"])
	}
}

func TestTurnRunFailsWhenProviderProducesNoFinalAnswer(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	var terminal map[string]any
	run := newTurnRun(builder, func(event map[string]any) error {
		if err := conversation.ValidateEventMap(event); err != nil {
			return err
		}
		terminal = event
		return nil
	}, "turn-1", "nonce-1")

	if err := run.finishCompleted(); err != nil {
		t.Fatal(err)
	}
	if terminal["type"] != string(conversation.EventTurnFailed) {
		t.Fatalf("terminal type = %v, want turn.failed", terminal["type"])
	}
	payload := terminal["payload"].(map[string]any)
	if payload["reason"] != "provider_no_final_answer" {
		t.Fatalf("reason = %v", payload["reason"])
	}
}

func TestContentTextHandlesAntigravityShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"hello"`, want: "hello"},
		{name: "text part array", raw: `[{"text":"hello"},{"content":"world"}]`, want: "hello\nworld"},
		{name: "record", raw: `{"content":"hello"}`, want: "hello"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentText(json.RawMessage(tt.raw)); got != tt.want {
				t.Fatalf("contentText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPTYRunnerArchitectureConstraint(t *testing.T) {
	// Assert that we import github.com/creack/pty and use it (to prevent compile error on the import itself)
	_ = pty.Start

	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}
	code := string(content)

	// 1. Must import github.com/creack/pty
	if !strings.Contains(code, `"github.com/creack/pty"`) {
		t.Error("Architecture violation: main.go must import \"github.com/creack/pty\"")
	}

	// 2. Must call pty.Start
	if !strings.Contains(code, "pty.Start(") {
		t.Error("Architecture violation: main.go must call pty.Start(runCmd) to wrap the agy CLI in a PTY")
	}

	// 3. Must not call raw runCmd.Start() or runCmd.Run()
	if strings.Contains(code, "runCmd.Start(") || strings.Contains(code, "runCmd.Run(") {
		t.Error("Architecture violation: main.go must not run agy directly via runCmd.Start() or runCmd.Run(), it must be wrapped in a PTY")
	}

	// Parse main.go into AST to check for forbidden imports, identifiers, and literals (ignoring comments)
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("failed to parse main.go: %v", err)
	}

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			path := x.Path.Value
			if strings.Contains(path, "websocket") || strings.Contains(path, "grpc") {
				t.Errorf("Architecture violation: main.go imports forbidden package %s", path)
			}
			if strings.Contains(path, "localharness") {
				t.Errorf("Architecture violation: main.go imports localharness package %s", path)
			}
		case *ast.Ident:
			if strings.Contains(strings.ToLower(x.Name), "localharness") {
				t.Errorf("Architecture violation: main.go references localharness identifier: %s", x.Name)
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && strings.Contains(strings.ToLower(x.Value), "localharness") {
				t.Errorf("Architecture violation: main.go contains localharness in string literal: %s", x.Value)
			}
			// Retired ToS auto-accept: the runner must not sniff PTY
			// stdout for consent screens and replay keystrokes.
			// Onboarding state is seeded by
			// antigravity-container/antigravity-runner-launch.sh; extend
			// the seeded config files instead of scripting the TUI.
			if x.Kind == token.STRING && strings.Contains(strings.ToLower(x.Value), "terms of service") {
				t.Errorf("Architecture violation: main.go reintroduces ToS-screen sniffing in string literal: %s", x.Value)
			}
			if x.Kind == token.STRING && strings.Contains(x.Value, `\x1b[`) {
				t.Errorf("Architecture violation: main.go reintroduces TUI keystroke/escape scripting in string literal: %s", x.Value)
			}
		}
		return true
	})
}

// --- Liveness contract tests -------------------------------------------------
//
// Every wait in handleSubmitTurn that can resolve a turn must publish exactly
// one durable terminal and ack the command. These tests pin each select arm:
// agy process exit, the submit-ack watchdog, the interrupt grace window, and
// the inert post-exit drain. The originating gap: the old wait had a single
// exit (a DONE planner response) while the heartbeat kept the command pinned,
// so an agy crash, swallowed prompt, or unacknowledged Stop stranded the turn
// silently — the counted bug class.

type eventLog struct {
	mu     sync.Mutex
	events []map[string]any
}

func (l *eventLog) publisher(event map[string]any) error {
	if err := conversation.ValidateEventMap(event); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	return nil
}

func (l *eventLog) snapshot() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]any{}, l.events...)
}

type fakeJSMsg struct {
	mu    sync.Mutex
	acked bool
}

func (m *fakeJSMsg) Metadata() (*jetstream.MsgMetadata, error) { return &jetstream.MsgMetadata{}, nil }
func (m *fakeJSMsg) Data() []byte                              { return nil }
func (m *fakeJSMsg) Headers() nats.Header                      { return nil }
func (m *fakeJSMsg) Subject() string                           { return "" }
func (m *fakeJSMsg) Reply() string                             { return "" }
func (m *fakeJSMsg) DoubleAck(context.Context) error           { return nil }
func (m *fakeJSMsg) Nak() error                                { return nil }
func (m *fakeJSMsg) NakWithDelay(time.Duration) error          { return nil }
func (m *fakeJSMsg) InProgress() error                         { return nil }
func (m *fakeJSMsg) Term() error                               { return nil }
func (m *fakeJSMsg) TermWithReason(string) error               { return nil }

func (m *fakeJSMsg) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = true
	return nil
}

func (m *fakeJSMsg) wasAcked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked
}

func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAgyArgsForConfigRequireConcreteModel(t *testing.T) {
	if _, err := agyArgsForConfig(runnerConfig{}); err == nil {
		t.Fatal("agyArgsForConfig accepted an empty model")
	}
}

func TestAgyArgsForConfigPassesSessionModel(t *testing.T) {
	args, err := agyArgsForConfig(runnerConfig{model: "Gemini 3.1 Pro"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--dangerously-skip-permissions", "--model", "Gemini 3.1 Pro"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestReportRuntimeConfigPostsAppliedModel(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("session-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("TANK_OPERATOR_INTERNAL_URL", server.URL)
	t.Setenv("TANK_OPERATOR_TOKEN_PATH", tokenFile)

	err := reportRuntimeConfig(runnerConfig{
		sessionID: "791",
		model:     "Gemini 3.1 Pro",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer session-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotPath != "/api/internal/sessions/791/runtime-config" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["model"] != "Gemini 3.1 Pro" {
		t.Fatalf("body = %#v", gotBody)
	}
	if _, present := gotBody["effort"]; present {
		t.Fatalf("antigravity runtime report should not include empty effort: %#v", gotBody)
	}
}

func livenessTestConfig() runnerConfig {
	return runnerConfig{
		sessionID:         "17",
		sessionStorageKey: "17",
		submitAckTimeout:  time.Minute,
		interruptGrace:    time.Minute,
	}
}

func startLivenessTurn(t *testing.T, cfg runnerConfig, active *activeProcess) (*eventLog, *fakeJSMsg, chan error) {
	t.Helper()
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	state := &runnerState{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	msg := &fakeJSMsg{}
	command := sessionbus.Command{
		Type:        sessionbus.CommandSubmitTurn,
		TurnID:      "turn-1",
		ClientNonce: "nonce-1",
		Prompt:      "hello",
	}
	done := make(chan error, 1)
	go func() {
		_, err := handleSubmitTurn(context.Background(), cfg, builder, log.publisher, active, state, msg, command, false, w)
		done <- err
	}()
	return log, msg, done
}

func lastEvent(t *testing.T, log *eventLog) map[string]any {
	t.Helper()
	events := log.snapshot()
	if len(events) == 0 {
		t.Fatal("no events published")
	}
	return events[len(events)-1]
}

func TestHandleSubmitTurnFailsDurablyWhenAgyExits(t *testing.T) {
	active := newActiveProcess()
	log, msg, done := startLivenessTurn(t, livenessTestConfig(), active)

	waitUntil(t, "turn claimed", func() bool { return len(log.snapshot()) >= 1 })
	active.markExited(errors.New("agy crashed"))

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handleSubmitTurn did not resolve after process exit")
	}
	last := lastEvent(t, log)
	if last["type"] != string(conversation.EventTurnFailed) {
		t.Fatalf("terminal type = %v, want turn.failed", last["type"])
	}
	if reason := last["payload"].(map[string]any)["reason"]; reason != "provider_process_exited" {
		t.Fatalf("reason = %v, want provider_process_exited", reason)
	}
	if !msg.wasAcked() {
		t.Fatal("command was not acked after durable terminal")
	}
}

func TestHandleSubmitTurnDrainsCommandsAfterAgyExit(t *testing.T) {
	active := newActiveProcess()
	active.markExited(errors.New("agy crashed earlier"))
	log, msg, done := startLivenessTurn(t, livenessTestConfig(), active)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inert drain did not resolve")
	}
	last := lastEvent(t, log)
	if last["type"] != string(conversation.EventTurnFailed) {
		t.Fatalf("terminal type = %v, want turn.failed", last["type"])
	}
	if reason := last["payload"].(map[string]any)["reason"]; reason != "provider_process_unavailable" {
		t.Fatalf("reason = %v, want provider_process_unavailable", reason)
	}
	if !msg.wasAcked() {
		t.Fatal("queued command was not drained with an ack")
	}
}

func TestHandleSubmitTurnWatchdogFailsSwallowedPrompt(t *testing.T) {
	cfg := livenessTestConfig()
	cfg.submitAckTimeout = 25 * time.Millisecond
	active := newActiveProcess()
	log, msg, done := startLivenessTurn(t, cfg, active)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not resolve the turn")
	}
	last := lastEvent(t, log)
	if last["type"] != string(conversation.EventTurnFailed) {
		t.Fatalf("terminal type = %v, want turn.failed", last["type"])
	}
	if reason := last["payload"].(map[string]any)["reason"]; reason != "prompt_not_accepted" {
		t.Fatalf("reason = %v, want prompt_not_accepted", reason)
	}
	if !msg.wasAcked() {
		t.Fatal("command was not acked after watchdog terminal")
	}
}

func TestHandleSubmitTurnInterruptGraceForcesDurableStop(t *testing.T) {
	cfg := livenessTestConfig()
	cfg.interruptGrace = 25 * time.Millisecond
	active := newActiveProcess()
	log, msg, done := startLivenessTurn(t, cfg, active)

	waitUntil(t, "turn claimed", func() bool { return len(log.snapshot()) >= 1 })
	waitUntil(t, "interrupt accepted", func() bool {
		_ = active.interrupt("turn-1")
		return active.wasInterrupted("turn-1")
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt grace did not resolve the turn")
	}
	last := lastEvent(t, log)
	if last["type"] != string(conversation.EventTurnInterrupted) {
		t.Fatalf("terminal type = %v, want turn.interrupted", last["type"])
	}
	if !msg.wasAcked() {
		t.Fatal("command was not acked after forced Stop terminal")
	}
}

func TestTurnRunClassifiesExecutorErrorOnNoFinalAnswer(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	run := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")

	line := `{"step_index":3,"source":"SYSTEM","type":"ERROR_MESSAGE","status":"DONE","content":"agent executor error: UNKNOWN (code 500)"}`
	var step AgyStep
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/tmp/transcript_full.jsonl", line, step); err != nil {
		t.Fatal(err)
	}
	if err := run.finishCompleted(); err != nil {
		t.Fatal(err)
	}
	last := lastEvent(t, log)
	if last["type"] != string(conversation.EventTurnFailed) {
		t.Fatalf("terminal type = %v, want turn.failed", last["type"])
	}
	if reason := last["payload"].(map[string]any)["reason"]; reason != "provider_executor_error" {
		t.Fatalf("reason = %v, want provider_executor_error", reason)
	}
}

func TestInterruptWithoutActiveTurnDoesNotSignalIdleAgy(t *testing.T) {
	active := newActiveProcess()
	if err := active.interrupt(""); err != nil {
		t.Fatal(err)
	}
	if active.wasInterrupted("") {
		t.Fatal("idle interrupt must not set the interrupted flag (it would SIGINT idle agy)")
	}
}

// TestRunnerSelfContinuationContract pins the long-running-agent harness contract
// at the code level: the antigravity-runner must NOT own or fire a Tank clock for
// agy. agy self-continues (it fires its own timer/build task and emits the
// continuation); the runner OBSERVES that and RELAYS it via /agent-continuation.
// Reintroducing a scheduled-wakeup / background-task-wake registration from the
// runner is the puppeteer regression that cost ~20 prior attempts. AST-based so the
// contract's prose references in code comments don't trip it. See ARCHITECTURE.md.
func TestRunnerSelfContinuationContract(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}
	code := string(content)

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("failed to parse main.go: %v", err)
	}

	forbiddenIdents := map[string]string{
		"registerScheduledWakeup":     "Tank-owned scheduled-wakeup registration",
		"registerBackgroundTaskWake":  "Tank-owned background-task-wake registration",
		"maybeRegisterScheduleWakeup": "schedule-tool timer inject",
		"isNativeTimerEchoStep":       "native-timer-echo parking heuristic",
		"parseScheduleWakeup":         "schedule-tool duration parser (inject)",
		"warnIfWaitWithoutSchedule":   "wait-intent-without-schedule inject diagnostic",
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if why, bad := forbiddenIdents[x.Name]; bad {
				t.Errorf("self-continuation contract violation: identifier %q (%s) — agy self-continues; relay via /agent-continuation, never a Tank-owned wake", x.Name, why)
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				lit := strings.ToLower(x.Value)
				if strings.Contains(lit, "/scheduled-wakeups") || strings.Contains(lit, "/background-task-wakes") {
					t.Errorf("self-continuation contract violation: string literal %s posts a Tank wake endpoint — the runner must only POST /agent-continuation", x.Value)
				}
			}
		}
		return true
	})

	// Positive: the relay endpoint must be present (the observe-and-relay path).
	if !strings.Contains(code, "/agent-continuation") {
		t.Error("self-continuation contract: main.go must POST /agent-continuation to relay agy's idle self-continuation")
	}
}

// TestNoteTaskSignalTracksPendingSet pins the background-work pending-set: agy's
// uniform jsonl signal (a MODEL status=RUNNING "background task with task id: X"
// marker adds X; a SYSTEM_MESSAGE sender=X completion removes X). The set drives
// whether a turn.completed lands mid-work. See ARCHITECTURE.md.
func TestNoteTaskSignalTracksPendingSet(t *testing.T) {
	st := &runnerState{}
	note := func(step AgyStep) {
		st.mu.Lock()
		st.noteTaskSignalLocked(step)
		st.mu.Unlock()
	}
	running := func(id string) AgyStep {
		return AgyStep{Source: "MODEL", Type: "GENERIC", Status: "RUNNING",
			Content: json.RawMessage(`"Tool is running as a background task with task id: ` + id + `"`)}
	}
	done := func(id string) AgyStep {
		return AgyStep{Source: "SYSTEM", Type: "SYSTEM_MESSAGE",
			Content: json.RawMessage(`"[message] sender=` + id + ` finished with result: ok"`)}
	}

	note(running("conv/task-1"))
	if !st.backgroundWorkPending() {
		t.Fatal("task-1 should be pending after its RUNNING marker")
	}
	note(running("conv/task-2"))
	note(done("conv/task-1"))
	if !st.backgroundWorkPending() {
		t.Fatal("task-2 should still be pending after only task-1 completed")
	}
	note(done("conv/task-2"))
	if st.backgroundWorkPending() {
		t.Fatal("no work should be pending after both tasks completed")
	}
	st.mu.Lock()
	last := st.lastCompletedTask
	st.mu.Unlock()
	if last != "conv/task-2" {
		t.Fatalf("lastCompletedTask = %q, want conv/task-2", last)
	}

	// Ordinary MODEL prose (not a RUNNING marker) must not add a phantom task.
	note(AgyStep{Source: "MODEL", Type: "PLANNER_RESPONSE", Status: "DONE",
		Content: json.RawMessage(`"just talking, no task here"`)})
	if st.backgroundWorkPending() {
		t.Fatal("ordinary prose must not add a background task")
	}
}

// TestTurnCompletedStampsBackgroundWorkPending proves the runner stamps
// turn.completed.background_work_pending from the pending-set at the terminal: a
// turn whose work is still in flight folds to the non-summoning "scheduled" status;
// an idle terminal summons.
func TestTurnCompletedStampsBackgroundWorkPending(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending bool
	}{
		{"work pending -> stamped true", true},
		{"no work pending -> stamped false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &runnerState{}
			if tc.pending {
				st.mu.Lock()
				st.noteTaskSignalLocked(AgyStep{Source: "MODEL", Status: "RUNNING",
					Content: json.RawMessage(`"Tool is running as a background task with task id: t1"`)})
				st.mu.Unlock()
			}
			var published []map[string]any
			publish := func(e map[string]any) error {
				published = append(published, e)
				return nil
			}
			run := newTurnRun(eventBuilder{sessionID: "63"}, publish, "turn_x", "nonce_x")
			run.state = st

			step := AgyStep{Source: "MODEL", Type: "PLANNER_RESPONSE", Status: "DONE",
				Content: json.RawMessage(`"all done, reporting back"`)}
			if err := run.observeStep("p", "l", step); err != nil {
				t.Fatalf("observeStep: %v", err)
			}
			<-run.turnComplete
			if err := run.finishCompleted(); err != nil {
				t.Fatalf("finishCompleted: %v", err)
			}

			var completed map[string]any
			for _, e := range published {
				if e["type"] == string(conversation.EventTurnCompleted) {
					completed = e
				}
			}
			if completed == nil {
				t.Fatal("no turn.completed event was published")
			}
			payload, _ := completed["payload"].(map[string]any)
			if payload == nil {
				t.Fatal("turn.completed has no payload")
			}
			if got, _ := payload["background_work_pending"].(bool); got != tc.pending {
				t.Fatalf("background_work_pending = %v, want %v", got, tc.pending)
			}
		})
	}
}

// --- Durable task-lifecycle tests (tank-operator#1035) ----------------------
//
// agy's RUNNING / sender= task markers must publish durable shell_task.*
// events carrying the originating turn id. That single event family feeds the
// transcript projection's fold parent-map (turn_bgtask-<task> relay turns fold
// into the originating turn), the continuation-turn handling, and the
// Background-activity tab. Keeping the signal in runner memory only was the
// root cause of session 790's standalone "I woke up" turn.

func TestTaskLifecycleEventsPublishDurableFoldEdge(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	state := &runnerState{builder: builder, publish: log.publisher}
	run := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")
	state.attachTurn(run)
	cfg := runnerConfig{sessionID: "17"}

	startLine := `{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","status":"RUNNING","content":"Tool is running as a background task with task id: T-9"}`
	var startStep AgyStep
	if err := json.Unmarshal([]byte(startLine), &startStep); err != nil {
		t.Fatal(err)
	}
	if err := state.handleStep("/tmp/t.jsonl", startLine, startStep, cfg); err != nil {
		t.Fatal(err)
	}
	// The same marker re-read from agy's cumulative jsonl must not
	// double-publish the lifecycle edge.
	if err := state.handleStep("/tmp/t.jsonl", startLine, startStep, cfg); err != nil {
		t.Fatal(err)
	}
	state.detachTurn(run)

	doneLine := `{"step_index":3,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","content":"background task update sender=T-9 state=done"}`
	var doneStep AgyStep
	if err := json.Unmarshal([]byte(doneLine), &doneStep); err != nil {
		t.Fatal(err)
	}
	if err := state.handleStep("/tmp/t.jsonl", doneLine, doneStep, cfg); err != nil {
		t.Fatal(err)
	}

	var taskEvents []map[string]any
	for _, event := range log.snapshot() {
		typ, _ := event["type"].(string)
		if strings.HasPrefix(typ, "shell_task.") {
			taskEvents = append(taskEvents, event)
		}
	}
	if len(taskEvents) != 2 {
		t.Fatalf("shell_task events = %d, want 2 (started + exited): %#v", len(taskEvents), taskEvents)
	}
	wantTask := stableIDPart("T-9")
	started, exited := taskEvents[0], taskEvents[1]
	if started["type"] != string(conversation.EventShellTaskStarted) {
		t.Fatalf("first task event type = %v", started["type"])
	}
	if exited["type"] != string(conversation.EventShellTaskExited) {
		t.Fatalf("second task event type = %v", exited["type"])
	}
	for _, event := range taskEvents {
		if event["turn_id"] != "turn-1" {
			t.Fatalf("task event turn_id = %v, want originating turn-1 (the fold edge): %#v", event["turn_id"], event)
		}
		if event["task_id"] != wantTask {
			t.Fatalf("task event task_id = %v, want %q (must match relay turn id suffix)", event["task_id"], wantTask)
		}
	}
	if started["timeline_id"] != exited["timeline_id"] {
		t.Fatalf("timeline ids differ: %v vs %v (must upsert one background row)", started["timeline_id"], exited["timeline_id"])
	}
	if status := started["payload"].(map[string]any)["status"]; status != "running" {
		t.Fatalf("started status = %v", status)
	}
	if status := exited["payload"].(map[string]any)["status"]; status != "completed" {
		t.Fatalf("exited status = %v", status)
	}
}

func TestTaskLifecycleStartWhileIdleDefersToAttachingTurn(t *testing.T) {
	// agy's conversational planner DONE can close the user turn seconds
	// before the tool call that starts the task, so the RUNNING marker
	// lands in the idle gap (observed live: slot-1 session 159). The edge
	// must not be orphaned: it defers and publishes when the next turn
	// attaches — the same turn that replays the buffered steps — and the
	// projection's wake-of-a-wake chain walker collapses the rest.
	t.Setenv("TANK_OPERATOR_INTERNAL_URL", "http://127.0.0.1:1")
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	state := &runnerState{builder: builder, publish: log.publisher}
	cfg := runnerConfig{sessionID: "17"}

	// turn-1 is the answer-first asking turn: it attaches, completes
	// ("I'll set a timer"), and detaches BEFORE the tool call runs.
	asking := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")
	state.attachTurn(asking)
	state.detachTurn(asking)

	startLine := `{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","status":"RUNNING","content":"Tool is running as a background task with task id: T-9"}`
	var startStep AgyStep
	if err := json.Unmarshal([]byte(startLine), &startStep); err != nil {
		t.Fatal(err)
	}
	if err := state.handleStep("/tmp/t.jsonl", startLine, startStep, cfg); err != nil {
		t.Fatal(err)
	}
	for _, event := range log.snapshot() {
		typ, _ := event["type"].(string)
		if strings.HasPrefix(typ, "shell_task.") {
			t.Fatalf("idle task start must defer, not publish immediately: %#v", event)
		}
	}
	if !state.backgroundWorkPending() {
		t.Fatal("pending-set must still track the task for background_work_pending")
	}

	relay := newTurnRun(builder, log.publisher, "turn_bgtask-T-9", "nonce-relay")
	state.attachTurn(relay)
	defer state.detachTurn(relay)

	var started map[string]any
	for _, event := range log.snapshot() {
		if event["type"] == string(conversation.EventShellTaskStarted) {
			started = event
		}
	}
	if started == nil {
		t.Fatal("attachTurn must flush the deferred shell_task.started edge")
	}
	// Causal-adjacency attribution: the idle-started task belongs to the
	// turn that had just closed (turn-1, the answer-first asking turn),
	// NOT the relay turn that happens to attach next — so the fire relay
	// folds directly to the user-visible turn.
	if started["turn_id"] != "turn-1" {
		t.Fatalf("deferred edge turn_id = %v, want last completed turn-1", started["turn_id"])
	}
	if started["task_id"] != stableIDPart("T-9") {
		t.Fatalf("deferred edge task_id = %v", started["task_id"])
	}
}

// --- Turn-settle tests (silence is the boundary) -----------------------------

func settleStep(t *testing.T, line string) AgyStep {
	t.Helper()
	var step AgyStep
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	return step
}

func waitComplete(t *testing.T, run *turnRun, within time.Duration) bool {
	t.Helper()
	select {
	case <-run.turnComplete:
		return true
	case <-time.After(within):
		return false
	}
}

// TestTurnSettleKeepsAnswerFirstBurstInOneTurn replays slot-1 round 1's exact
// burst shape (tank-operator#1035): ack prose closes nothing, the schedule
// tool call and RUNNING marker land inside the still-open turn, and the
// settled prose plus silence produces the one terminal. Pre-settle, the ack
// prose terminated the turn instantly and the work signal landed in the idle
// gap — the answer-first false-ready/false-ring pathology.
func TestTurnSettleKeepsAnswerFirstBurstInOneTurn(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	run := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")
	run.settleDur = 60 * time.Millisecond

	ack := `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"I will set a 2-minute timer now."}`
	if err := run.observeStep("/tmp/t.jsonl", ack, settleStep(t, ack)); err != nil {
		t.Fatal(err)
	}
	if waitComplete(t, run, 20*time.Millisecond) {
		t.Fatal("ack prose must not terminate the turn before the settle window elapses")
	}

	toolCall := `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"schedule","args":{"DurationSeconds":"120"}}]}`
	if err := run.observeStep("/tmp/t.jsonl", toolCall, settleStep(t, toolCall)); err != nil {
		t.Fatal(err)
	}
	marker := `{"step_index":4,"source":"MODEL","type":"GENERIC","status":"RUNNING","content":"Tool is running as a background task with task id: T-1"}`
	if err := run.observeStep("/tmp/t.jsonl", marker, settleStep(t, marker)); err != nil {
		t.Fatal(err)
	}
	if waitComplete(t, run, 90*time.Millisecond) {
		t.Fatal("tool activity must keep the turn open — no settled prose has re-armed the window")
	}

	settled := `{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"I have set a 2-minute timer and will now wait."}`
	if err := run.observeStep("/tmp/t.jsonl", settled, settleStep(t, settled)); err != nil {
		t.Fatal(err)
	}
	if !waitComplete(t, run, 300*time.Millisecond) {
		t.Fatal("settled prose plus silence must terminate the turn")
	}
	if err := run.finishCompleted(); err != nil {
		t.Fatal(err)
	}
	events := log.snapshot()
	last := events[len(events)-1]
	if last["type"] != string(conversation.EventTurnCompleted) {
		t.Fatalf("terminal type = %v, want turn.completed", last["type"])
	}
}

func TestTurnSettleQuietCompletesAfterWindow(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	run := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")
	run.settleDur = 40 * time.Millisecond

	prose := `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"All done."}`
	if err := run.observeStep("/tmp/t.jsonl", prose, settleStep(t, prose)); err != nil {
		t.Fatal(err)
	}
	if waitComplete(t, run, 10*time.Millisecond) {
		t.Fatal("turn must not complete before the silence window")
	}
	if !waitComplete(t, run, 300*time.Millisecond) {
		t.Fatal("turn must complete after the silence window")
	}
}

func TestTurnSettleZeroCompletesImmediately(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	log := &eventLog{}
	run := newTurnRun(builder, log.publisher, "turn-1", "nonce-1")

	prose := `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"All done."}`
	if err := run.observeStep("/tmp/t.jsonl", prose, settleStep(t, prose)); err != nil {
		t.Fatal(err)
	}
	if !waitComplete(t, run, 50*time.Millisecond) {
		t.Fatal("settleDur=0 must preserve immediate completion on settled prose")
	}
}
