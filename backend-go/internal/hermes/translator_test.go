package hermes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

// fixedNow gives the translator a deterministic clock so event_id /
// created_at strings are golden-stable. Stamp adds order_key + sequence
// + uuid which still vary, so tests check the contract fields directly
// rather than diffing full event bodies.
var fixedNow = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }

func newTranslator() *Translator {
	return NewTranslator(TranslatorConfig{
		SessionID:         "42",
		SessionStorageKey: "42",
		Email:             "user@example.com",
		TurnID:            "turn_abcdef0123456789",
		ClientNonce:       "n-deadbeef0000000000000000000000",
		Now:               fixedNow,
	})
}

func translateAll(t *testing.T, tr *Translator, events []RunEvent) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, evt := range events {
		out = append(out, tr.Translate(evt)...)
	}
	return out
}

func eventOfType(events []map[string]any, want string) map[string]any {
	for _, e := range events {
		if t, _ := e["type"].(string); t == want {
			return e
		}
	}
	return nil
}

// ─── happy-path translator contract ─────────────────────────────────────

func TestTranslator_HappyPath_TextOnlyResponse(t *testing.T) {
	tr := newTranslator()
	events := []RunEvent{
		{Type: "response.created"},
		{Type: "response.output_item.added", Data: rawJSON(`{"item":{"id":"msg_1","type":"message"}}`)},
		{Type: "response.output_text.delta", Data: rawJSON(`{"item_id":"msg_1","delta":"Hello, "}`)},
		{Type: "response.output_text.delta", Data: rawJSON(`{"item_id":"msg_1","delta":"world."}`)},
		{Type: "response.output_item.done", Data: rawJSON(`{"item":{"id":"msg_1","type":"message"}}`)},
		{Type: "response.completed"},
	}
	out := translateAll(t, tr, events)

	if got := tr.Terminal(); got != "completed" {
		t.Fatalf("Terminal() = %q, want completed", got)
	}
	if e := eventOfType(out, string(conversation.EventTurnStarted)); e == nil {
		t.Errorf("missing turn.started")
	}
	if e := eventOfType(out, string(conversation.EventTurnCompleted)); e == nil {
		t.Errorf("missing turn.completed")
	}
	itemDone := eventOfType(out, string(conversation.EventItemCompleted))
	if itemDone == nil {
		t.Fatalf("missing item.completed for message")
	}
	payload, _ := itemDone["payload"].(map[string]any)
	if got, _ := payload["text"].(string); got != "Hello, world." {
		t.Errorf("payload.text = %q, want %q", got, "Hello, world.")
	}
	if got, _ := payload["kind"].(string); got != "message" {
		t.Errorf("payload.kind = %q, want message", got)
	}
	if got, _ := itemDone["source"].(string); got != string(conversation.SourceHermes) {
		t.Errorf("source = %q, want hermes", got)
	}
}

func TestTranslator_TurnStartedOnlyOnce(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.created"}, // duplicate (Hermes shouldn't emit, but guard regardless)
		{Type: "run.started"},
	})
	count := 0
	for _, e := range out {
		if t, _ := e["type"].(string); t == string(conversation.EventTurnStarted) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("turn.started emitted %d times, want exactly 1", count)
	}
}

// ─── tool call lifecycle ────────────────────────────────────────────────

func TestTranslator_FunctionCall_AddedAndDone(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.output_item.added", Data: rawJSON(
			`{"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"list_tank_sessions","arguments":"{}"}}`,
		)},
		{Type: "response.output_item.done", Data: rawJSON(
			`{"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"list_tank_sessions","arguments":"{}"}}`,
		)},
		{Type: "response.output_item.done", Data: rawJSON(
			`{"item":{"id":"fco_1","type":"function_call_output","call_id":"call_abc","output":"[]"}}`,
		)},
		{Type: "response.completed"},
	})

	var (
		started, done, result map[string]any
	)
	for _, e := range out {
		switch e["type"] {
		case string(conversation.EventItemStarted):
			started = e
		case string(conversation.EventItemCompleted):
			payload, _ := e["payload"].(map[string]any)
			if kind, _ := payload["kind"].(string); kind == "tool" {
				done = e
			} else if kind == "tool_result" {
				result = e
			}
		}
	}
	if started == nil {
		t.Fatalf("missing item.started for tool call")
	}
	if done == nil {
		t.Fatalf("missing item.completed for tool call")
	}
	if result == nil {
		t.Fatalf("missing item.completed for tool result")
	}
	// The tool_use and tool_result must share a timeline_id so the SPA
	// folds them into one rendered card. This is the same convention
	// claude / codex adapters use.
	if startedTL, doneTL := started["timeline_id"], done["timeline_id"]; startedTL != doneTL {
		t.Errorf("tool.started timeline_id %v != tool.completed %v", startedTL, doneTL)
	}
	if startedTL, resultTL := started["timeline_id"], result["timeline_id"]; startedTL != resultTL {
		t.Errorf("tool.started timeline_id %v != tool_result %v", startedTL, resultTL)
	}
}

// ─── terminal contracts ─────────────────────────────────────────────────

func TestTranslator_ResponseFailed_EmitsTurnFailed(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.failed", Data: rawJSON(`{"error":"upstream timeout"}`)},
	})
	failed := eventOfType(out, string(conversation.EventTurnFailed))
	if failed == nil {
		t.Fatalf("missing turn.failed")
	}
	if got := tr.Terminal(); got != "failed" {
		t.Errorf("Terminal() = %q, want failed", got)
	}
	payload, _ := failed["payload"].(map[string]any)
	if reason, _ := payload["reason"].(string); reason != "provider_failure" {
		t.Errorf("payload.reason = %q, want provider_failure", reason)
	}
}

func TestTranslator_ResponseCancelled_EmitsTurnInterrupted(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.cancelled"},
	})
	if e := eventOfType(out, string(conversation.EventTurnInterrupted)); e == nil {
		t.Fatalf("missing turn.interrupted")
	}
	if got := tr.Terminal(); got != "interrupted" {
		t.Errorf("Terminal() = %q, want interrupted", got)
	}
}

// ─── schema-drift safety net ────────────────────────────────────────────

func TestTranslator_UnknownEventType_IsCountedNotFatal(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.future_unknown_event", Data: rawJSON(`{"foo":"bar"}`)},
		{Type: "response.completed"},
	})
	// turn.started + turn.completed; unknown event is silently counted.
	if got := tr.UnhandledCount; got != 1 {
		t.Errorf("UnhandledCount = %d, want 1", got)
	}
	if got := tr.UnhandledTypes["response.future_unknown_event"]; got != 1 {
		t.Errorf("UnhandledTypes[unknown] = %d, want 1", got)
	}
	// Translator continues; turn.completed still lands.
	if e := eventOfType(out, string(conversation.EventTurnCompleted)); e == nil {
		t.Fatalf("missing turn.completed after unknown event")
	}
}

func TestTranslator_HermesToolProgress_IsSilentlyIgnored(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "hermes.tool.progress", Data: rawJSON(`{"tool":"list"}`)},
		{Type: "response.completed"},
	})
	// hermes.tool.progress is intentionally absorbed; it doesn't bump
	// UnhandledCount (which tracks schema drift, not deliberate skips).
	if got := tr.UnhandledCount; got != 0 {
		t.Errorf("UnhandledCount = %d, want 0 (hermes.tool.progress is documented-skip)", got)
	}
	if len(out) < 2 {
		t.Errorf("expected turn.started + turn.completed; got %d events", len(out))
	}
}

// ─── envelope shape ─────────────────────────────────────────────────────

func TestTranslator_EmittedEventsPassSchemaValidation(t *testing.T) {
	tr := newTranslator()
	out := translateAll(t, tr, []RunEvent{
		{Type: "response.created"},
		{Type: "response.output_item.added", Data: rawJSON(`{"item":{"id":"msg_1","type":"message"}}`)},
		{Type: "response.output_text.delta", Data: rawJSON(`{"item_id":"msg_1","delta":"hi"}`)},
		{Type: "response.output_item.done", Data: rawJSON(`{"item":{"id":"msg_1","type":"message"}}`)},
		{Type: "response.completed"},
	})
	if len(out) == 0 {
		t.Fatalf("expected events, got none")
	}
	for i, e := range out {
		if err := conversation.ValidateEventMap(e); err != nil {
			t.Errorf("event %d (%v) failed schema validation: %v\nevent: %s", i, e["type"], err, dump(e))
		}
		// Source must be hermes for every translator-emitted event.
		// Tank-owned boundary events (user_message.created, turn.submitted)
		// are emitted by conversation.UserSubmissionEventMaps in the
		// bridge, not by the translator, so they don't appear here.
		if src, _ := e["source"].(string); src != string(conversation.SourceHermes) {
			t.Errorf("event %d source = %q, want hermes", i, src)
		}
	}
}

// ─── SSE parser ─────────────────────────────────────────────────────────

func TestParseSSE_HandlesMultilineDataAndCommentsAndEventNames(t *testing.T) {
	body := strings.Join([]string{
		": this is a comment line",
		"event: response.created",
		"data: {\"foo\":\"bar\"}",
		"",
		"event: response.output_text.delta",
		"data: {\"item_id\":\"msg_1\",",
		"data:  \"delta\":\"hello\"}",
		"",
		"event: response.completed",
		"data: {}",
		"",
	}, "\n")
	var got []RunEvent
	if err := parseSSE(strings.NewReader(body), func(e RunEvent) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Type != "response.created" || got[1].Type != "response.output_text.delta" || got[2].Type != "response.completed" {
		t.Errorf("event types mismatch: %v %v %v", got[0].Type, got[1].Type, got[2].Type)
	}
	// Multi-line data joins via newline; the parser preserves the inner
	// JSON shape across continuation lines.
	var delta outputTextDelta
	if err := json.Unmarshal(got[1].Data, &delta); err != nil {
		t.Errorf("data on multi-line event did not decode: %v\ndata: %s", err, string(got[1].Data))
	}
	if delta.ItemID != "msg_1" || delta.Delta != "hello" {
		t.Errorf("multi-line decode = %+v, want item_id=msg_1 delta=hello", delta)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
