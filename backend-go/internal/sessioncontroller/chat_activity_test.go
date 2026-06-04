package sessioncontroller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestRefreshSessionActivityEmitsWhenReadStateChangesUnreadCount(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	if _, err := readStates.Set(context.Background(), "user@example.com", "63", "002"); err != nil {
		t.Fatal(err)
	}
	emitter := &recordingRowEmitter{}
	eventStore := &activityEventStore{unread: 0}
	activity := mustActivityJSON(t, map[string]any{
		"status":         "ready",
		"unread_count":   3,
		"needs_input":    false,
		"failed":         false,
		"last_order_key": "002",
	})
	refresher := &ChatActivityEmitter{
		Writer: &RowWriter{
			Emitter: emitter,
		},
		ChatEvents: eventStore,
		ReadStates: readStates,
		Rows: activityRowFetcher{record: sessionmodel.SessionRecord{
			Email:           "user@example.com",
			Scope:           "default",
			ID:              "63",
			Status:          "Active",
			Visible:         true,
			ActivitySummary: activity,
		}},
		Scope: "default",
	}

	if err := refresher.RefreshSessionActivity(context.Background(), "user@example.com", "63"); err != nil {
		t.Fatal(err)
	}

	if eventStore.afterOrderKey != "002" {
		t.Fatalf("UnreadOutputCount afterOrderKey = %q, want 002", eventStore.afterOrderKey)
	}
	if emitter.calls != 1 {
		t.Fatalf("row publishes = %d, want 1", emitter.calls)
	}
}

func TestRefreshSessionActivitySkipsNoOpUnreadCount(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	if _, err := readStates.Set(context.Background(), "user@example.com", "63", "002"); err != nil {
		t.Fatal(err)
	}
	emitter := &recordingRowEmitter{}
	eventStore := &activityEventStore{unread: 3}
	activity := mustActivityJSON(t, map[string]any{
		"status":         "ready",
		"unread_count":   3,
		"needs_input":    false,
		"failed":         false,
		"last_order_key": "002",
	})
	refresher := &ChatActivityEmitter{
		Writer: &RowWriter{
			Emitter: emitter,
		},
		ChatEvents: eventStore,
		ReadStates: readStates,
		Rows: activityRowFetcher{record: sessionmodel.SessionRecord{
			Email:           "user@example.com",
			Scope:           "default",
			ID:              "63",
			Status:          "Active",
			Visible:         true,
			ActivitySummary: activity,
		}},
		Scope: "default",
	}

	if err := refresher.RefreshSessionActivity(context.Background(), "user@example.com", "63"); err != nil {
		t.Fatal(err)
	}

	if emitter.calls != 0 {
		t.Fatalf("row publishes = %d, want 0", emitter.calls)
	}
}

func TestRefreshSessionActivityRepairsStaleStoppingWhenTargetTurnIsTerminal(t *testing.T) {
	emitter := &recordingRowEmitter{}
	eventStore := &activityEventStore{
		lifecycleEvents: []map[string]any{
			{"type": "turn.interrupt_requested", "turn_id": "turn-1", "order_key": "003"},
		},
		terminalTurns: map[string]map[string]any{
			"turn-1": {"type": "turn.completed", "turn_id": "turn-1", "order_key": "002"},
		},
	}
	activity := mustActivityJSON(t, map[string]any{
		"status":         "streaming",
		"active_turn_id": "turn-1",
		"unread_count":   0,
		"needs_input":    false,
		"failed":         false,
		"last_order_key": "001",
	})
	refresher := &ChatActivityEmitter{
		Writer: &RowWriter{
			Emitter: emitter,
		},
		ChatEvents: eventStore,
		ReadStates: store.NewStubConversationReadStateStore(),
		Rows: activityRowFetcher{record: sessionmodel.SessionRecord{
			Email:           "user@example.com",
			Scope:           "default",
			ID:              "63",
			Status:          "Active",
			Visible:         true,
			ActivitySummary: activity,
		}},
		Scope: "default",
	}

	if err := refresher.RefreshSessionActivity(context.Background(), "user@example.com", "63"); err != nil {
		t.Fatal(err)
	}

	if emitter.calls != 1 {
		t.Fatalf("row publishes = %d, want 1", emitter.calls)
	}
	if eventStore.terminalLookups != 1 {
		t.Fatalf("terminal lookups = %d, want 1", eventStore.terminalLookups)
	}
}

func TestResolveStoppingActivityFromTerminalKeepsLateInterruptTail(t *testing.T) {
	activeTurnID := "turn-1"
	eventStore := &activityEventStore{
		terminalTurns: map[string]map[string]any{
			"turn-1": {"type": "turn.completed", "turn_id": "turn-1", "order_key": "002"},
		},
	}
	refresher := &ChatActivityEmitter{ChatEvents: eventStore}
	lateInterruptOrderKey := "003"
	summary := sessionactivity.ActivitySummary{
		Status:       "stopping",
		ActiveTurnID: &activeTurnID,
		NeedsInput:   true,
		Failed:       false,
		LastOrderKey: &lateInterruptOrderKey,
	}

	got, repaired, err := refresher.resolveStoppingActivityFromTerminal(context.Background(), "63", summary)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatal("expected stale stopping summary to be repaired")
	}
	if got.Status != "ready" || got.ActiveTurnID != nil || got.NeedsInput || got.Failed {
		t.Fatalf("repaired summary = %+v, want ready inactive non-failed", got)
	}
	if got.LastOrderKey == nil || *got.LastOrderKey != "003" {
		t.Fatalf("last_order_key = %#v, want late interrupt key 003", got.LastOrderKey)
	}
}

type recordingRowEmitter struct {
	calls int
}

func (e *recordingRowEmitter) PublishCurrentRow(_ context.Context, _, _ string) {
	e.calls++
}

type activityRowFetcher struct {
	record sessionmodel.SessionRecord
	found  bool
}

func (f activityRowFetcher) Get(_ context.Context, _, _ string) (sessionmodel.SessionRecord, bool, error) {
	if !f.found && f.record.ID == "" {
		return sessionmodel.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

type activityEventStore struct {
	unread          int
	afterOrderKey   string
	lifecycleEvents []map[string]any
	terminalTurns   map[string]map[string]any
	terminalLookups int
	compactions     int64
	compactionScans int
}

func (s *activityEventStore) Upsert(_ context.Context, _ map[string]any) error {
	return nil
}

func (s *activityEventStore) FindStrandedLaunchTurns(context.Context, time.Time, time.Time, int) ([]store.StrandedLaunchTurn, error) {
	return nil, nil
}

func (s *activityEventStore) ListBySession(_ context.Context, _ string, _ store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
}

func (s *activityEventStore) LatestEvents(_ context.Context, _ string, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
}

func (s *activityEventStore) EventsForTurnAfter(_ context.Context, _ string, _ string, _ string, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{FoundNewest: true}, nil
}

func (s *activityEventStore) HasOrderKey(_ context.Context, _ string, _ string) (bool, error) {
	return true, nil
}

func (s *activityEventStore) OrderKeyForTimelineID(_ context.Context, _ string, _ string) (string, error) {
	return "", nil
}

func (s *activityEventStore) FindTurnTerminal(_ context.Context, _ string, turnID string) (map[string]any, error) {
	s.terminalLookups++
	if s.terminalTurns == nil {
		return nil, nil
	}
	return s.terminalTurns[turnID], nil
}

func (s *activityEventStore) LatestLifecycleEvents(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return s.lifecycleEvents, nil
}

func (s *activityEventStore) UnreadOutputCount(_ context.Context, _ string, afterOrderKey string) (int, error) {
	s.afterOrderKey = afterOrderKey
	return s.unread, nil
}

func (s *activityEventStore) CountContextCompactions(_ context.Context, _ string) (int64, error) {
	s.compactionScans++
	return s.compactions, nil
}

func mustActivityJSON(t *testing.T, value map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type staticOwnerResolver struct{ owner string }

func (r staticOwnerResolver) OwnerForSession(_ context.Context, _, _ string) (string, error) {
	return r.owner, nil
}

// captureCompactionMetrics embeds the no-op metrics so it satisfies the full
// LifecycleEmitterMetrics surface while only capturing the compaction counter.
type captureCompactionMetrics struct {
	noopLifecycleEmitterMetrics
	calls    int
	provider string
	trigger  string
}

func (m *captureCompactionMetrics) RecordCompaction(provider, trigger string) {
	m.calls++
	m.provider = provider
	m.trigger = trigger
}

func compactionEmitter(emitter *recordingRowEmitter, events *activityEventStore, metrics *captureCompactionMetrics, priorCount int64) *ChatActivityEmitter {
	return &ChatActivityEmitter{
		Writer:     &RowWriter{Emitter: emitter},
		ChatEvents: events,
		ReadStates: store.NewStubConversationReadStateStore(),
		Registry:   staticOwnerResolver{owner: "user@example.com"},
		Rows: activityRowFetcher{record: sessionmodel.SessionRecord{
			ID:              "63",
			Email:           "user@example.com",
			Scope:           "default",
			CompactionCount: priorCount,
		}},
		Metrics: metrics,
		Scope:   "default",
	}
}

func contextCompactedEvent() map[string]any {
	return map[string]any{
		"type":       "context.compacted",
		"session_id": "63",
		"source":     "claude",
		"payload":    map[string]any{"trigger": "auto"},
	}
}

// TestEmitChatActivityDeltaRecordsAdvancingCompaction proves a context.compacted
// event whose recomputed total exceeds the durable prior writes + publishes the
// row and records exactly one provider/trigger-labeled metric — and that it
// takes the compaction branch, never the activity-summary unread path.
func TestEmitChatActivityDeltaRecordsAdvancingCompaction(t *testing.T) {
	emitter := &recordingRowEmitter{}
	events := &activityEventStore{compactions: 2}
	metrics := &captureCompactionMetrics{}
	e := compactionEmitter(emitter, events, metrics, 1)

	if err := e.EmitChatActivityDelta(context.Background(), contextCompactedEvent()); err != nil {
		t.Fatal(err)
	}
	if events.compactionScans != 1 {
		t.Fatalf("CountContextCompactions calls = %d, want 1", events.compactionScans)
	}
	if emitter.calls != 1 {
		t.Fatalf("row publishes = %d, want 1 (advancing compaction must write+publish)", emitter.calls)
	}
	if events.afterOrderKey != "" {
		t.Fatalf("activity unread path ran (afterOrderKey=%q); compaction must not touch the activity summary", events.afterOrderKey)
	}
	if metrics.calls != 1 || metrics.provider != "claude" || metrics.trigger != "auto" {
		t.Fatalf("RecordCompaction = (calls=%d provider=%q trigger=%q), want (1, claude, auto)", metrics.calls, metrics.provider, metrics.trigger)
	}
}

// TestEmitChatActivityDeltaDeduplicatesRedeliveredCompaction proves an
// at-least-once redelivery — the recomputed total equals the durable prior — is
// a no-op: no row publish and no metric. This is the idempotency guard that
// keeps redelivered compaction events off the row-version cursor.
func TestEmitChatActivityDeltaDeduplicatesRedeliveredCompaction(t *testing.T) {
	emitter := &recordingRowEmitter{}
	events := &activityEventStore{compactions: 1}
	metrics := &captureCompactionMetrics{}
	e := compactionEmitter(emitter, events, metrics, 1)

	if err := e.EmitChatActivityDelta(context.Background(), contextCompactedEvent()); err != nil {
		t.Fatal(err)
	}
	if emitter.calls != 0 {
		t.Fatalf("row publishes = %d, want 0 (redelivered compaction at the same total must be a no-op)", emitter.calls)
	}
	if metrics.calls != 0 {
		t.Fatalf("RecordCompaction calls = %d, want 0 on redelivery", metrics.calls)
	}
}

// TestDeriveActivitySummaryIgnoresContextCompacted is a defensive guard: a
// context.compacted event is inert for the activity summary — folding one
// yields the same summary as folding nothing. Compaction is intra-turn noise,
// not a lifecycle transition, so it must never move run status, active turn, or
// needs-input. (In production it never reaches the fold at all — the lifecycle
// query filters it out — but this locks the invariant if that filter changes.)
func TestDeriveActivitySummaryIgnoresContextCompacted(t *testing.T) {
	base := sessionactivity.DeriveActivitySummary(nil, nil, 0, false)
	withCompaction := sessionactivity.DeriveActivitySummary(
		nil,
		[]map[string]any{{"type": "context.compacted", "turn_id": "turn-1", "order_key": "004"}},
		0,
		false,
	)
	// Compaction may advance LastOrderKey — it is a real ledger event — but it
	// must not move any run-state field: it is intra-turn noise, not a
	// lifecycle transition.
	if withCompaction.Status != base.Status {
		t.Fatalf("status = %q, want unchanged %q", withCompaction.Status, base.Status)
	}
	if withCompaction.ActiveTurnID != nil {
		t.Fatalf("active turn id = %v, want nil (compaction is not a turn start)", *withCompaction.ActiveTurnID)
	}
	if withCompaction.NeedsInput != base.NeedsInput {
		t.Fatalf("needs_input = %v, want unchanged %v", withCompaction.NeedsInput, base.NeedsInput)
	}
	if withCompaction.Failed != base.Failed {
		t.Fatalf("failed = %v, want unchanged %v", withCompaction.Failed, base.Failed)
	}
}
