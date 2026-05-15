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

func (s *stubMsg) Subject() string                          { return s.subject }
func (s *stubMsg) Data() []byte                             { return s.data }
func (s *stubMsg) Ack() error                               { s.acked++; return nil }
func (s *stubMsg) NakWithDelay(d time.Duration) error       { s.naked++; s.nakDelay = d; return nil }
func (s *stubMsg) TermWithReason(reason string) error       { s.termed++; s.termRsn = reason; return nil }

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
		err: errors.New("cosmos throttled"),
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

func TestEventTypeForLogExtractsType(t *testing.T) {
	got := eventTypeForLog([]byte(`{"type":"turn.started","event_id":"x"}`))
	if got != "turn.started" {
		t.Fatalf("type = %q, want turn.started", got)
	}
	if got := eventTypeForLog([]byte("not-json")); got != "" {
		t.Fatalf("type = %q, want empty for invalid JSON", got)
	}
}
