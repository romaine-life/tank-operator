package sessionbus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

type recordingStore struct {
	upserts []map[string]any
	err     error
}

func (r *recordingStore) Upsert(_ context.Context, event map[string]any) error {
	if r.err != nil {
		return r.err
	}
	r.upserts = append(r.upserts, event)
	return nil
}

type recordingMetrics struct {
	schemaRejected   int
	transientFailure int
}

func (m *recordingMetrics) RecordSchemaRejected()   { m.schemaRejected++ }
func (m *recordingMetrics) RecordTransientFailure() { m.transientFailure++ }

type stubMsg struct {
	subject  string
	data     []byte
	acked    int
	naked    int
	nakDelay time.Duration
	termed   int
	termRsn  string
}

func (s *stubMsg) Subject() string                    { return s.subject }
func (s *stubMsg) Data() []byte                       { return s.data }
func (s *stubMsg) Ack() error                         { s.acked++; return nil }
func (s *stubMsg) NakWithDelay(d time.Duration) error { s.naked++; s.nakDelay = d; return nil }
func (s *stubMsg) TermWithReason(reason string) error { s.termed++; s.termRsn = reason; return nil }

func TestPersistMessageAcksValidEvent(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":   "evt-1",
		"session_id": "63",
		"actor":      "runner",
		"source":     "tank",
		"type":       "turn.started",
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  "order-1",
		"visibility": "durable",
		"turn_id":    "turn-1",
	})
	msg := &stubMsg{subject: "tank.session.63.events", data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if msg.acked != 1 {
		t.Fatalf("acked = %d, want 1", msg.acked)
	}
	if msg.naked != 0 || msg.termed != 0 {
		t.Fatalf("unexpected nak/term: nak=%d term=%d", msg.naked, msg.termed)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	if metrics.schemaRejected != 0 || metrics.transientFailure != 0 {
		t.Fatalf("counters fired on success: schema=%d transient=%d",
			metrics.schemaRejected, metrics.transientFailure)
	}
}

func TestPersistMessageTerminatesSchemaRejected(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{
		err: &conversation.SchemaError{Cause: errors.New("event_id is required")},
	}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"type": "user_message.created",
		"id":   "broken",
	})
	msg := &stubMsg{subject: "tank.session.63.events", data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if msg.termed != 1 {
		t.Fatalf("termed = %d, want 1 for schema rejection", msg.termed)
	}
	if msg.naked != 0 {
		t.Fatalf("naked = %d, want 0 — schema rejection must not retry", msg.naked)
	}
	if msg.acked != 0 {
		t.Fatalf("acked = %d, want 0 on schema rejection", msg.acked)
	}
	if metrics.schemaRejected != 1 {
		t.Fatalf("schema_rejected counter = %d, want 1", metrics.schemaRejected)
	}
	if metrics.transientFailure != 0 {
		t.Fatalf("transient counter = %d, want 0", metrics.transientFailure)
	}
}

func TestPersistMessageRetriesTransientFailures(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{
		err: errors.New("postgres connection refused"),
	}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":   "evt-transient",
		"session_id": "63",
		"actor":      "runner",
		"source":     "tank",
		"type":       "turn.started",
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  "order-1",
		"visibility": "durable",
		"turn_id":    "turn-1",
	})
	msg := &stubMsg{subject: "tank.session.63.events", data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if msg.naked != 1 {
		t.Fatalf("naked = %d, want 1 for transient error", msg.naked)
	}
	if msg.nakDelay != 5*time.Second {
		t.Fatalf("nak delay = %v, want 5s", msg.nakDelay)
	}
	if msg.termed != 0 {
		t.Fatalf("termed = %d, want 0 for transient error", msg.termed)
	}
	if metrics.transientFailure != 1 {
		t.Fatalf("transient counter = %d, want 1", metrics.transientFailure)
	}
	if metrics.schemaRejected != 0 {
		t.Fatalf("schema_rejected counter = %d, want 0", metrics.schemaRejected)
	}
}

func TestPersistMessageTerminatesInvalidJSON(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	msg := &stubMsg{subject: "tank.session.63.events", data: []byte("not-json")}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if msg.termed != 1 {
		t.Fatalf("termed = %d, want 1 for invalid JSON", msg.termed)
	}
	if !strings.Contains(msg.termRsn, "invalid json") {
		t.Fatalf("term reason = %q, want substring %q", msg.termRsn, "invalid json")
	}
	if msg.acked != 0 {
		t.Fatalf("acked = %d, want 0 — invalid JSON terminates without ack", msg.acked)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0 for invalid JSON", len(store.upserts))
	}
	if metrics.schemaRejected != 1 {
		t.Fatalf("schema_rejected counter = %d, want 1 — invalid JSON is a producer regression", metrics.schemaRejected)
	}
	if metrics.transientFailure != 0 {
		t.Fatalf("transient counter = %d, want 0 — invalid JSON is permanent not transient",
			metrics.transientFailure)
	}
}

// TestSubjectForCommandRoutesInterruptToControlPlane pins the load-bearing
// fix for the "Stop doesn't interrupt deep tool-use loops" failure mode:
// interrupt_turn MUST publish to the control-plane subject, not the
// command subject, so that a runner-side max_ack_pending=1 on the command
// subject cannot hold the interrupt behind an in-flight submit_turn.
// If this test ever flips to expect CommandSubject, the migration guard
// in scripts/check-removed-chat-runtime.mjs is the second line of defense.
func TestSubjectForCommandRoutesInterruptToControlPlane(t *testing.T) {
	storage := "session-storage-key"
	provider := "claude"
	interrupt := Command{
		Type:              CommandInterrupt,
		SessionStorageKey: storage,
		Provider:          provider,
	}
	got := SubjectForCommand(interrupt)
	want := ControlSubject(storage, provider)
	if got != want {
		t.Fatalf("interrupt subject = %q, want %q (control-plane)", got, want)
	}
	if got == CommandSubject(storage, provider) {
		t.Fatalf("interrupt MUST NOT publish to the command subject %q", got)
	}
}

// TestSubjectForCommandRoutesDataPlane keeps the existing data-plane
// commands (submit_turn, anything not explicitly control) on the command
// subject, where the runner's per-session serial consumer processes them
// one at a time.
func TestSubjectForCommandRoutesDataPlane(t *testing.T) {
	storage := "session-storage-key"
	provider := "claude"
	for _, ty := range []string{CommandSubmitTurn, "unknown_future"} {
		cmd := Command{
			Type:              ty,
			SessionStorageKey: storage,
			Provider:          provider,
		}
		got := SubjectForCommand(cmd)
		want := CommandSubject(storage, provider)
		if got != want {
			t.Fatalf("%s subject = %q, want %q (data plane)", ty, got, want)
		}
	}
}

// TestSubjectForCommandRoutesInputReplyToControlPlane pins the routing
// decision for input_reply commands. They MUST publish to the control
// subject — not the data-plane command subject — because an input_reply
// only ever resolves an AskUserQuestion gate inside an already-running
// submit_turn that is, by construction, holding the data-plane consumer's
// single max_ack_pending slot. Routing input_reply to the data plane would
// deadlock: the runner is parked in canUseTool waiting for the input_reply,
// but the input_reply can't be delivered because the parked submit_turn
// hasn't acked. Same architectural shape as the original interrupt fix.
func TestSubjectForCommandRoutesInputReplyToControlPlane(t *testing.T) {
	storage := "session-storage-key"
	provider := "claude"
	reply := Command{
		Type:              CommandInputReply,
		SessionStorageKey: storage,
		Provider:          provider,
	}
	got := SubjectForCommand(reply)
	want := ControlSubject(storage, provider)
	if got != want {
		t.Fatalf("input_reply subject = %q, want %q (control-plane)", got, want)
	}
	if got == CommandSubject(storage, provider) {
		t.Fatalf("input_reply MUST NOT publish to the command subject %q", got)
	}
}

func TestSubjectForCommandRoutesBackgroundStopToControlPlane(t *testing.T) {
	storage := "session-storage-key"
	provider := "codex"
	stop := Command{
		Type:              CommandStopBackgroundTask,
		SessionStorageKey: storage,
		Provider:          provider,
	}
	got := SubjectForCommand(stop)
	want := ControlSubject(storage, provider)
	if got != want {
		t.Fatalf("stop_background_task subject = %q, want %q (control-plane)", got, want)
	}
	if got == CommandSubject(storage, provider) {
		t.Fatalf("stop_background_task MUST NOT publish to the command subject %q", got)
	}
}

// TestControlSubjectShape pins the wire format so the runner-shared JS
// helper (which builds the same shape independently) is mechanically
// comparable. The shape token order is: subjectRoot, storageToken,
// "control", sanitizedProvider.
func TestControlSubjectShape(t *testing.T) {
	got := ControlSubject("abc", "claude")
	wantPrefix := "tank.session."
	wantSuffix := ".control.claude"
	if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("control subject shape = %q, want prefix %q + %q", got, wantPrefix, wantSuffix)
	}
	if got == CommandSubject("abc", "claude") {
		t.Fatalf("control and command subjects collided: %q", got)
	}
}

func TestEventTypeForLogExtractsType(t *testing.T) {
	got := eventTypeForLog([]byte(`{"type":"turn.started","event_id":"x"}`))
	if got != "turn.started" {
		t.Fatalf("type = %q, want turn.started", got)
	}
	if got := eventTypeForLog([]byte("not-json")); got != "" {
		t.Fatalf("type = %q, want empty for invalid JSON", got)
	}
}
