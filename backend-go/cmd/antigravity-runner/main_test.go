package main

import (
	"encoding/json"
	"strings"
	"testing"

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
		builder.turnCompletedEvent("turn-1", "nonce-1", final),
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
	if events[1]["type"] != string(conversation.EventAssistantMessageCreated) {
		t.Fatalf("second event type = %v", events[1]["type"])
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
