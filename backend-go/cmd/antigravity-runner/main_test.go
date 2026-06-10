/*
WARNING: TestPTYRunnerArchitectureConstraint is an explicit structural constraint check.
DO NOT remove or disable this test, and DO NOT modify it to permit websocket, gRPC,
or localharness references/imports in main.go.

This test is designed to prevent agents and developers from short-circuiting the PTY wrapper
architecture (which is a hard production requirement due to agy's CLI-only nature and consumer OAuth).
*/

package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
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
		builder.turnCompletedEvent("turn-1", "nonce-1", final, nil),
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
		}
		return true
	})
}

