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
	schemaRejected       int
	transientFailure     int
	turnFailureRecorded  []turnFailureRecord
	turnLifecycle        map[string]int
	missingTerminalNonce []missingTerminalNonceRecord
}

type turnFailureRecord struct {
	source string
	reason string
}

func (m *recordingMetrics) RecordSchemaRejected()   { m.schemaRejected++ }
func (m *recordingMetrics) RecordTransientFailure() { m.transientFailure++ }
func (m *recordingMetrics) RecordTurnFailurePersisted(source string, reason string) {
	m.turnFailureRecorded = append(m.turnFailureRecorded, turnFailureRecord{source: source, reason: reason})
}
func (m *recordingMetrics) RecordTurnLifecyclePersisted(eventType string) {
	if m.turnLifecycle == nil {
		m.turnLifecycle = map[string]int{}
	}
	m.turnLifecycle[eventType]++
}
func (m *recordingMetrics) RecordTurnTerminalMissingClientNonce(source string, eventType string) {
	m.missingTerminalNonce = append(m.missingTerminalNonce, missingTerminalNonceRecord{
		source:    source,
		eventType: eventType,
	})
}

type missingTerminalNonceRecord struct {
	source    string
	eventType string
}

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
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

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
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

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
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

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
	msg := &stubMsg{subject: SessionEventSubject("63"), data: []byte("not-json")}

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

// TestPersistMessageRecordsTurnFailureCounter pins the user-trust observability
// surface that replaced the SPA run-status pill. With the pill removed, the
// tank_transcript_turn_failure_total counter is how Grafana / alert rules
// detect "every codex_gui session is failing" (e.g. the Codex auth
// refresh_token_reused storm that motivated the pill removal). The counter
// must label by source (claude/codex/tank) and reason (payload.reason) so
// per-provider alerts fire independently.
func TestPersistMessageRecordsTurnFailureCounter(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":   "evt-fail-1",
		"session_id": "63",
		"actor":      "runner",
		"source":     "codex",
		"type":       "turn.failed",
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  "order-fail-1",
		"visibility": "durable",
		"turn_id":    "turn-1",
		"payload": map[string]any{
			"reason": "provider_failure",
			"error":  "rate limit exceeded",
		},
	})
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if msg.acked != 1 {
		t.Fatalf("acked = %d, want 1", msg.acked)
	}
	if len(metrics.turnFailureRecorded) != 1 {
		t.Fatalf("turn-failure counter recorded %d times, want 1", len(metrics.turnFailureRecorded))
	}
	got := metrics.turnFailureRecorded[0]
	if got.source != "codex" {
		t.Fatalf("source label = %q, want %q", got.source, "codex")
	}
	if got.reason != "provider_failure" {
		t.Fatalf("reason label = %q, want %q", got.reason, "provider_failure")
	}
}

func TestPersistMessageRecordsCommandFailedCounter(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":   "evt-cmdfail-1",
		"session_id": "63",
		"actor":      "system",
		"source":     "tank",
		"type":       "turn.command_failed",
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  "order-cmdfail-1",
		"visibility": "durable",
		"turn_id":    "turn-1",
		"payload": map[string]any{
			"reason": "command_failed",
		},
	})
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if len(metrics.turnFailureRecorded) != 1 {
		t.Fatalf("turn-failure counter recorded %d times, want 1 for turn.command_failed", len(metrics.turnFailureRecorded))
	}
	got := metrics.turnFailureRecorded[0]
	if got.source != "tank" || got.reason != "command_failed" {
		t.Fatalf("label tuple = (%q, %q), want (tank, command_failed)", got.source, got.reason)
	}
}

func TestPersistMessageDoesNotRecordTurnFailureForSuccess(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":   "evt-ok-1",
		"session_id": "63",
		"actor":      "runner",
		"source":     "codex",
		"type":       "turn.completed",
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  "order-ok-1",
		"visibility": "durable",
		"turn_id":    "turn-1",
	})
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if len(metrics.turnFailureRecorded) != 0 {
		t.Fatalf("turn-failure counter fired on turn.completed (%d times); the counter is for failures only", len(metrics.turnFailureRecorded))
	}
}

// TestPersistMessageRecordsTurnLifecycleCounter pins the silent-stranding
// observability contract: each of the five lifecycle event types
// (turn.submitted + the four terminal types) MUST bump
// tank_turn_lifecycle_total{event_type=<type>} when the persister
// commits a durable row, and non-lifecycle types MUST NOT contribute
// (so the alert's expr stays comparing the right cardinalities). The
// silent-stranding alert in k8s/templates/observability.yaml reads this
// counter directly — a divergence here is the prototypical ea70777-
// shape failure surface per docs/features/agent-runners/contract.md.
func TestPersistMessageRecordsTurnLifecycleCounter(t *testing.T) {
	lifecycleTypes := []string{
		"turn.submitted",
		"turn.completed",
		"turn.failed",
		"turn.command_failed",
		"turn.interrupted",
	}
	for _, eventType := range lifecycleTypes {
		t.Run(eventType, func(t *testing.T) {
			bus := &Bus{scope: "default"}
			store := &recordingStore{}
			metrics := &recordingMetrics{}
			raw, _ := json.Marshal(map[string]any{
				"event_id":   "evt-" + eventType,
				"session_id": "63",
				"actor":      "runner",
				"source":     "codex",
				"type":       eventType,
				"created_at": "2026-05-12T00:00:00.000Z",
				"order_key":  "order-" + eventType,
				"visibility": "durable",
				"turn_id":    "turn-1",
			})
			msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

			bus.handlePersistMessage(context.Background(), store, metrics, msg)

			if metrics.turnLifecycle[eventType] != 1 {
				t.Fatalf("lifecycle counter for %q = %d, want 1", eventType, metrics.turnLifecycle[eventType])
			}
		})
	}
}

// TestPersistMessageOmitsNonLifecycleFromLifecycleCounter pins the bound
// on tank_turn_lifecycle_total's label set: only the five lifecycle
// types contribute. turn.started, item.*, tool.*, and session.* are
// either intermediate or non-turn signals and must not skew the
// submitted-vs-terminal divergence the silent-stranding alert reads.
func TestPersistMessageOmitsNonLifecycleFromLifecycleCounter(t *testing.T) {
	nonLifecycleTypes := []string{
		"turn.started",
		"turn.interrupt_requested",
		"item.completed",
		"tool.approval_requested",
		"session.status",
		"user_message.created",
	}
	for _, eventType := range nonLifecycleTypes {
		t.Run(eventType, func(t *testing.T) {
			bus := &Bus{scope: "default"}
			store := &recordingStore{}
			metrics := &recordingMetrics{}
			raw, _ := json.Marshal(map[string]any{
				"event_id":   "evt-" + eventType,
				"session_id": "63",
				"actor":      "runner",
				"source":     "codex",
				"type":       eventType,
				"created_at": "2026-05-12T00:00:00.000Z",
				"order_key":  "order-" + eventType,
				"visibility": "durable",
				"turn_id":    "turn-1",
			})
			msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

			bus.handlePersistMessage(context.Background(), store, metrics, msg)

			if got := metrics.turnLifecycle[eventType]; got != 0 {
				t.Fatalf("lifecycle counter unexpectedly bumped for %q (= %d); only the five lifecycle types contribute", eventType, got)
			}
			if len(metrics.turnLifecycle) != 0 {
				t.Fatalf("lifecycle counter has %d entries after non-lifecycle event %q; want 0", len(metrics.turnLifecycle), eventType)
			}
		})
	}
}

func TestPersistMessageRecordsTerminalMissingClientNonce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType string
		wantCount int
	}{
		{name: "completed missing nonce", eventType: "turn.completed", wantCount: 1},
		{name: "failed missing nonce", eventType: "turn.failed", wantCount: 1},
		{name: "interrupted missing nonce", eventType: "turn.interrupted", wantCount: 1},
		{name: "submitted missing nonce is not terminal", eventType: "turn.submitted", wantCount: 0},
		{name: "started missing nonce is not terminal", eventType: "turn.started", wantCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &Bus{scope: "default"}
			store := &recordingStore{}
			metrics := &recordingMetrics{}
			raw, _ := json.Marshal(map[string]any{
				"event_id":   "evt-" + tc.eventType,
				"session_id": "63",
				"actor":      "runner",
				"source":     "hermes",
				"type":       tc.eventType,
				"created_at": "2026-05-12T00:00:00.000Z",
				"order_key":  "order-" + tc.eventType,
				"visibility": "durable",
				"turn_id":    "turn-1",
			})
			msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

			bus.handlePersistMessage(context.Background(), store, metrics, msg)

			if got := len(metrics.missingTerminalNonce); got != tc.wantCount {
				t.Fatalf("missing terminal nonce records = %d, want %d", got, tc.wantCount)
			}
			if tc.wantCount == 1 {
				record := metrics.missingTerminalNonce[0]
				if record.source != "hermes" || record.eventType != tc.eventType {
					t.Fatalf("missing terminal nonce record = %#v, want source=hermes eventType=%s", record, tc.eventType)
				}
			}
		})
	}
}

func TestPersistMessageDoesNotRecordTerminalMissingClientNonceWhenPresent(t *testing.T) {
	bus := &Bus{scope: "default"}
	store := &recordingStore{}
	metrics := &recordingMetrics{}
	raw, _ := json.Marshal(map[string]any{
		"event_id":     "evt-completed",
		"session_id":   "63",
		"actor":        "runner",
		"source":       "hermes",
		"type":         "turn.completed",
		"created_at":   "2026-05-12T00:00:00.000Z",
		"order_key":    "order-completed",
		"visibility":   "durable",
		"turn_id":      "turn-1",
		"client_nonce": "client-1",
	})
	msg := &stubMsg{subject: SessionEventSubject("63"), data: raw}

	bus.handlePersistMessage(context.Background(), store, metrics, msg)

	if got := len(metrics.missingTerminalNonce); got != 0 {
		t.Fatalf("missing terminal nonce records = %d, want 0", got)
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
// comparable. The shape token order is: subjectRoot, scopeToken,
// sessionToken, "control", sanitizedProvider.
func TestControlSubjectShape(t *testing.T) {
	got := ControlSubject("abc", "claude")
	wantPrefix := "tank.session."
	wantSuffix := ".control.claude"
	if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("control subject shape = %q, want prefix %q + %q", got, wantPrefix, wantSuffix)
	}
	parts := strings.Split(got, ".")
	if len(parts) != 6 {
		t.Fatalf("control subject tokens = %d, want 6: %q", len(parts), got)
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
