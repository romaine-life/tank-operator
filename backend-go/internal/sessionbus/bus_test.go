package sessionbus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
)

type recordingStore struct {
	upserts   []map[string]any
	err       error
	duplicate bool
}

func (r *recordingStore) Upsert(_ context.Context, event map[string]any) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	r.upserts = append(r.upserts, event)
	return !r.duplicate, nil
}

type recordingRefresher struct {
	batches [][]map[string]any
	err     error
}

func (r *recordingRefresher) RefreshEventBatch(_ context.Context, events []map[string]any) error {
	if r.err != nil {
		return r.err
	}
	batch := make([]map[string]any, len(events))
	copy(batch, events)
	r.batches = append(r.batches, batch)
	return nil
}

type recordingMetrics struct {
	schemaRejected       int
	transientFailure     int
	turnFailureRecorded  []turnFailureRecord
	turnLifecycle        map[string]int
	missingTerminalNonce []missingTerminalNonceRecord
	duplicates           int
	redelivered          int
	exhausted            map[string]int
	truncationGap        float64
	reconcilerRepaired   int
	phaseDurations       map[string]int
	batchSizes           []int
	processedAges        []float64
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
func (m *recordingMetrics) RecordDuplicatePersisted() { m.duplicates++ }
func (m *recordingMetrics) RecordRedelivered()        { m.redelivered++ }
func (m *recordingMetrics) RecordPersistPhaseDuration(phase string, _ float64) {
	if m.phaseDurations == nil {
		m.phaseDurations = map[string]int{}
	}
	m.phaseDurations[phase]++
}
func (m *recordingMetrics) RecordPersistBatchSize(n int) { m.batchSizes = append(m.batchSizes, n) }
func (m *recordingMetrics) RecordProcessedEventAge(seconds float64) {
	m.processedAges = append(m.processedAges, seconds)
}
func (m *recordingMetrics) RecordExhaustedRepair(outcome string) {
	if m.exhausted == nil {
		m.exhausted = map[string]int{}
	}
	m.exhausted[outcome]++
}
func (m *recordingMetrics) RecordStreamTruncationGap(missing float64) { m.truncationGap += missing }
func (m *recordingMetrics) RecordReconcilerRepairedHole()             { m.reconcilerRepaired++ }
func (m *recordingMetrics) RecordPersisterConsumerLag(float64, float64) {
}
func (m *recordingMetrics) RecordPersisterQueueDepth(int) {}

type missingTerminalNonceRecord struct {
	source    string
	eventType string
}

type stubMsg struct {
	subject      string
	data         []byte
	acked        int
	naked        int
	nakDelay     time.Duration
	termed       int
	termRsn      string
	inProgress   int
	numDelivered uint64
}

func (s *stubMsg) Subject() string                    { return s.subject }
func (s *stubMsg) Data() []byte                       { return s.data }
func (s *stubMsg) Ack() error                         { s.acked++; return nil }
func (s *stubMsg) NakWithDelay(d time.Duration) error { s.naked++; s.nakDelay = d; return nil }
func (s *stubMsg) TermWithReason(reason string) error { s.termed++; s.termRsn = reason; return nil }
func (s *stubMsg) InProgress() error                  { s.inProgress++; return nil }
func (s *stubMsg) Metadata() (*jetstream.MsgMetadata, error) {
	delivered := s.numDelivered
	if delivered == 0 {
		delivered = 1
	}
	return &jetstream.MsgMetadata{NumDelivered: delivered}, nil
}

// newTestDispatcher builds a dispatcher around a NATS-less Bus: the wake
// publish is skipped (nc == nil) so processBatch is fully synchronous and
// deterministic for unit tests.
func newTestDispatcher(store EventStore, refresher TranscriptRefresher, metrics PersisterMetrics) *persistDispatcher {
	return newPersistDispatcher(&Bus{scope: "default"}, store, refresher, metrics, persistDispatcherConfig{})
}

func inflightFor(t *testing.T, event map[string]any) (*inflightSessionEvent, *stubMsg) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	sessionID, _ := event["session_id"].(string)
	msg := &stubMsg{subject: SessionEventSubject(sessionID), data: raw}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	return &inflightSessionEvent{
		msg:        msg,
		event:      decoded,
		storageKey: "default:" + sessionID,
		enqueuedAt: time.Now(),
	}, msg
}

func validEvent(eventType, eventID, orderKey string) map[string]any {
	return map[string]any{
		"event_id":   eventID,
		"session_id": "63",
		"actor":      "runner",
		"source":     "codex",
		"type":       eventType,
		"created_at": "2026-05-12T00:00:00.000Z",
		"order_key":  orderKey,
		"visibility": "durable",
		"turn_id":    "turn-1",
	}
}

func TestProcessBatchAcksValidEvent(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	event := validEvent("turn.started", "evt-1", "order-1")
	event["source"] = "tank"
	in, msg := inflightFor(t, event)

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

	if msg.acked != 1 {
		t.Fatalf("acked = %d, want 1", msg.acked)
	}
	if msg.naked != 0 || msg.termed != 0 {
		t.Fatalf("unexpected nak/term: nak=%d term=%d", msg.naked, msg.termed)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	if len(refresher.batches) != 1 || len(refresher.batches[0]) != 1 {
		t.Fatalf("refresher batches = %#v, want one batch of one event", refresher.batches)
	}
	if metrics.schemaRejected != 0 || metrics.transientFailure != 0 {
		t.Fatalf("counters fired on success: schema=%d transient=%d",
			metrics.schemaRejected, metrics.transientFailure)
	}
}

func TestProcessBatchTerminatesSchemaRejected(t *testing.T) {
	store := &recordingStore{
		err: &conversation.SchemaError{Cause: errors.New("event_id is required")},
	}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	in, msg := inflightFor(t, map[string]any{
		"type":       "user_message.created",
		"id":         "broken",
		"session_id": "63",
	})

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

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
	if len(refresher.batches) != 0 {
		t.Fatalf("refresher ran on a fully rejected batch: %#v", refresher.batches)
	}
}

func TestProcessBatchRetriesTransientFailures(t *testing.T) {
	store := &recordingStore{
		err: errors.New("postgres connection refused"),
	}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	in, msg := inflightFor(t, validEvent("turn.started", "evt-transient", "order-1"))

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

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

func TestEnqueueTerminatesInvalidJSON(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	msg := &stubMsg{subject: SessionEventSubject("63"), data: []byte("not-json")}

	d.enqueue(context.Background(), msg)

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
	if d.queueDepth() != 0 {
		t.Fatalf("queue depth = %d, want 0 — invalid JSON must not occupy a queue slot", d.queueDepth())
	}
}

// TestProcessBatchCoalescesRefreshPerBatch pins the PR-1 amortization from
// tank-operator#1051: N events in one batch produce exactly one refresher
// call carrying all N, not N calls. The 2026-06-11 incident cost was one
// full re-projection per event; coalescing is the contract that prevents
// its return at the dispatch layer.
func TestProcessBatchCoalescesRefreshPerBatch(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	var batch []*inflightSessionEvent
	var msgs []*stubMsg
	for i := 0; i < 5; i++ {
		in, msg := inflightFor(t, validEvent("turn.started", "evt-"+strings.Repeat("x", i+1), "order-"+strings.Repeat("x", i+1)))
		batch = append(batch, in)
		msgs = append(msgs, msg)
	}

	d.processBatch(context.Background(), batch[0].storageKey, batch)

	if len(refresher.batches) != 1 {
		t.Fatalf("refresher calls = %d, want 1 (coalesced)", len(refresher.batches))
	}
	if len(refresher.batches[0]) != 5 {
		t.Fatalf("coalesced batch size = %d, want 5", len(refresher.batches[0]))
	}
	for i, msg := range msgs {
		if msg.acked != 1 {
			t.Fatalf("msg %d acked = %d, want 1", i, msg.acked)
		}
	}
	if len(metrics.batchSizes) != 1 || metrics.batchSizes[0] != 5 {
		t.Fatalf("batch size metric = %#v, want [5]", metrics.batchSizes)
	}
}

// TestProcessBatchNaksAllOnRefreshFailure pins the redelivery contract: the
// event rows are already durable and idempotent, so a projection failure
// NAKs every message in the batch — redelivery retries the refresh, never
// loses the events, and never acks rows whose projection state is unknown.
func TestProcessBatchNaksAllOnRefreshFailure(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{err: errors.New("projection tx failed")}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	inA, msgA := inflightFor(t, validEvent("turn.started", "evt-a", "order-a"))
	inB, msgB := inflightFor(t, validEvent("item.completed", "evt-b", "order-b"))

	d.processBatch(context.Background(), inA.storageKey, []*inflightSessionEvent{inA, inB})

	if msgA.naked != 1 || msgB.naked != 1 {
		t.Fatalf("naks = (%d, %d), want (1, 1) — refresh failure NAKs the whole batch", msgA.naked, msgB.naked)
	}
	if msgA.acked != 0 || msgB.acked != 0 {
		t.Fatalf("acks = (%d, %d), want (0, 0) on refresh failure", msgA.acked, msgB.acked)
	}
	if metrics.transientFailure != 1 {
		t.Fatalf("transient counter = %d, want 1", metrics.transientFailure)
	}
}

// TestProcessBatchDuplicateSkipsSideEffectsButRefreshes pins the dedupe
// semantics: an upsert reporting the row already existed (at-least-once
// redelivery) must not double-count per-event metrics, but MUST still be
// included in the refresh batch — the redelivery may exist precisely
// because the previous attempt's projection refresh failed after the row
// landed.
func TestProcessBatchDuplicateSkipsSideEffectsButRefreshes(t *testing.T) {
	store := &recordingStore{duplicate: true}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	in, msg := inflightFor(t, validEvent("turn.completed", "evt-dup", "order-dup"))

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

	if metrics.duplicates != 1 {
		t.Fatalf("duplicate counter = %d, want 1", metrics.duplicates)
	}
	if len(metrics.turnLifecycle) != 0 {
		t.Fatalf("lifecycle counter fired for a duplicate: %#v", metrics.turnLifecycle)
	}
	if len(metrics.missingTerminalNonce) != 0 {
		t.Fatalf("missing-nonce counter fired for a duplicate: %#v", metrics.missingTerminalNonce)
	}
	if len(refresher.batches) != 1 || len(refresher.batches[0]) != 1 {
		t.Fatalf("refresher batches = %#v, want the duplicate still refreshed", refresher.batches)
	}
	if msg.acked != 1 {
		t.Fatalf("acked = %d, want 1 — duplicates ack after refresh", msg.acked)
	}
}

// TestEnqueueCountsRedelivery pins the visibility contract for messages that
// outlived AckWait: NumDelivered > 1 increments the redelivered counter. The
// 2026-06-11 incident ground both replicas over the same messages with zero
// telemetry; this counter is that failure mode's direct signal.
func TestEnqueueCountsRedelivery(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	raw, _ := json.Marshal(validEvent("turn.started", "evt-r", "order-r"))
	first := &stubMsg{subject: SessionEventSubject("63"), data: raw, numDelivered: 1}
	second := &stubMsg{subject: SessionEventSubject("63"), data: raw, numDelivered: 2}

	d.enqueue(context.Background(), first)
	d.enqueue(context.Background(), second)

	if metrics.redelivered != 1 {
		t.Fatalf("redelivered counter = %d, want 1 (only NumDelivered > 1 counts)", metrics.redelivered)
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
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	event := validEvent("turn.failed", "evt-fail-1", "order-fail-1")
	event["payload"] = map[string]any{
		"reason": "provider_failure",
		"error":  "rate limit exceeded",
	}
	in, msg := inflightFor(t, event)

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

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
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	event := validEvent("turn.command_failed", "evt-cmdfail-1", "order-cmdfail-1")
	event["actor"] = "system"
	event["source"] = "tank"
	event["payload"] = map[string]any{
		"reason": "command_failed",
	}
	in, _ := inflightFor(t, event)

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

	if len(metrics.turnFailureRecorded) != 1 {
		t.Fatalf("turn-failure counter recorded %d times, want 1 for turn.command_failed", len(metrics.turnFailureRecorded))
	}
	got := metrics.turnFailureRecorded[0]
	if got.source != "tank" || got.reason != "command_failed" {
		t.Fatalf("label tuple = (%q, %q), want (tank, command_failed)", got.source, got.reason)
	}
}

func TestPersistMessageDoesNotRecordTurnFailureForSuccess(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	in, _ := inflightFor(t, validEvent("turn.completed", "evt-ok-1", "order-ok-1"))

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

	if len(metrics.turnFailureRecorded) != 0 {
		t.Fatalf("turn-failure counter fired on turn.completed (%d times); the counter is for failures only", len(metrics.turnFailureRecorded))
	}
}

// TestPersistMessageRecordsTurnLifecycleCounter pins the silent-stranding
// observability contract: each lifecycle boundary event type
// (turn.submitted + the terminal types) MUST bump
// tank_turn_lifecycle_total{event_type=<type>} when the persister
// commits a durable row, and non-lifecycle types MUST NOT contribute
// (so the alert's expr stays comparing the right cardinalities). The
// silent-stranding alert in k8s/templates/observability.yaml reads this
// counter directly — a divergence here is the prototypical ea70777-
// shape failure surface per docs/features/claude-runners/contract.md.
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
			store := &recordingStore{}
			refresher := &recordingRefresher{}
			metrics := &recordingMetrics{}
			d := newTestDispatcher(store, refresher, metrics)
			in, _ := inflightFor(t, validEvent(eventType, "evt-"+eventType, "order-"+eventType))

			d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

			if metrics.turnLifecycle[eventType] != 1 {
				t.Fatalf("lifecycle counter for %q = %d, want 1", eventType, metrics.turnLifecycle[eventType])
			}
		})
	}
}

// TestPersistMessageOmitsNonLifecycleFromLifecycleCounter pins the bound on
// tank_turn_lifecycle_total's label set: only submitted + terminal lifecycle
// boundaries contribute. turn.awaiting_input and turn.input_answered are valid
// same-turn AskUserQuestion state changes, but they are not terminal and must
// not skew the submitted-vs-terminal divergence the silent-stranding alert reads.
func TestPersistMessageOmitsNonLifecycleFromLifecycleCounter(t *testing.T) {
	nonLifecycleEvents := []struct {
		eventType string
		payload   map[string]any
		extra     map[string]any
		actor     string
		source    string
	}{
		{eventType: "turn.started"},
		{eventType: "turn.interrupt_requested"},
		{eventType: "item.completed", payload: map[string]any{"kind": "agent_message"}},
		{eventType: "item.started", payload: map[string]any{"kind": "agent_message"}},
		{eventType: "session.status", payload: map[string]any{"status": "ready"}},
		{eventType: "user_message.created", actor: "user", source: "tank", payload: map[string]any{"text": "hello", "display": map[string]any{"kind": "plain"}}, extra: map[string]any{"timeline_id": "turn-1:user", "client_nonce": "turn-1"}},
		{eventType: "turn.awaiting_input", payload: map[string]any{"questions": []any{map[string]any{"question": "Proceed?"}}}},
		{eventType: "turn.input_answered", actor: "user", source: "tank", payload: map[string]any{"question_timeline_id": "turn-1:item:toolu_ask", "provider_item_id": "toolu_ask", "answers": map[string]any{"Proceed?": []any{"Yes"}}}, extra: map[string]any{"timeline_id": "turn-1:item:toolu_ask:answer", "client_nonce": "answer-1"}},
	}
	for _, tc := range nonLifecycleEvents {
		t.Run(tc.eventType, func(t *testing.T) {
			store := &recordingStore{}
			refresher := &recordingRefresher{}
			metrics := &recordingMetrics{}
			d := newTestDispatcher(store, refresher, metrics)
			event := validEvent(tc.eventType, "evt-"+tc.eventType, "order-"+tc.eventType)
			if tc.actor != "" {
				event["actor"] = tc.actor
			}
			if tc.source != "" {
				event["source"] = tc.source
			}
			if tc.payload != nil {
				event["payload"] = tc.payload
			}
			for key, value := range tc.extra {
				event[key] = value
			}
			in, _ := inflightFor(t, event)

			d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

			if len(store.upserts) != 1 {
				t.Fatalf("persisted events = %d, want 1; event may be schema-invalid: %#v", len(store.upserts), event)
			}
			if got := metrics.turnLifecycle[tc.eventType]; got != 0 {
				t.Fatalf("lifecycle counter unexpectedly bumped for %q (= %d); only lifecycle boundaries contribute", tc.eventType, got)
			}
			if len(metrics.turnLifecycle) != 0 {
				t.Fatalf("lifecycle counter has %d entries after non-lifecycle event %q; want 0", len(metrics.turnLifecycle), tc.eventType)
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
			store := &recordingStore{}
			refresher := &recordingRefresher{}
			metrics := &recordingMetrics{}
			d := newTestDispatcher(store, refresher, metrics)
			in, _ := inflightFor(t, validEvent(tc.eventType, "evt-"+tc.eventType, "order-"+tc.eventType))

			d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

			if got := len(metrics.missingTerminalNonce); got != tc.wantCount {
				t.Fatalf("missing terminal nonce records = %d, want %d", got, tc.wantCount)
			}
			if tc.wantCount == 1 {
				record := metrics.missingTerminalNonce[0]
				if record.source != "codex" || record.eventType != tc.eventType {
					t.Fatalf("missing terminal nonce record = %#v, want source=codex eventType=%s", record, tc.eventType)
				}
			}
		})
	}
}

func TestPersistMessageDoesNotRecordTerminalMissingClientNonceWhenPresent(t *testing.T) {
	store := &recordingStore{}
	refresher := &recordingRefresher{}
	metrics := &recordingMetrics{}
	d := newTestDispatcher(store, refresher, metrics)
	event := validEvent("turn.completed", "evt-completed", "order-completed")
	event["client_nonce"] = "client-1"
	in, _ := inflightFor(t, event)

	d.processBatch(context.Background(), in.storageKey, []*inflightSessionEvent{in})

	if got := len(metrics.missingTerminalNonce); got != 0 {
		t.Fatalf("missing terminal nonce records = %d, want 0", got)
	}
}

func TestOrderKeyAgeSeconds(t *testing.T) {
	now := time.UnixMilli(1781192568194 + 21_000)
	age, ok := orderKeyAgeSeconds("1781192568194-00000001-turn_x:turn.submitted", now)
	if !ok {
		t.Fatalf("expected parseable order key")
	}
	if age < 20.9 || age > 21.1 {
		t.Fatalf("age = %v, want ~21s", age)
	}
	if _, ok := orderKeyAgeSeconds("garbage", now); ok {
		t.Fatalf("expected unparseable order key to report !ok")
	}
	if _, ok := orderKeyAgeSeconds("", now); ok {
		t.Fatalf("expected empty order key to report !ok")
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
