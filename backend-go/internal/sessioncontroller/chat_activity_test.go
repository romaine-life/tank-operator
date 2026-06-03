package sessioncontroller

import (
	"context"
	"encoding/json"
	"testing"

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
}

func (s *activityEventStore) Upsert(_ context.Context, _ map[string]any) error {
	return nil
}

func (s *activityEventStore) ListBySession(_ context.Context, _ string, _ store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
}

func (s *activityEventStore) LatestEvents(_ context.Context, _ string, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
}

func (s *activityEventStore) EventsForTurn(_ context.Context, _ string, _ string, _ int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
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

func mustActivityJSON(t *testing.T, value map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
