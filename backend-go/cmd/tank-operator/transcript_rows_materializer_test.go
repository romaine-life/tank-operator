package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type recordingTranscriptRowsStore struct {
	turnID          string
	entries         []map[string]any
	needsBackfill   bool
	needsCalls      int
	sessionID       string
	sessionEntries  []map[string]any
	replaceSessions []string
}

func (s *recordingTranscriptRowsStore) ReplaceForTurn(_ context.Context, _ string, turnID string, entries []map[string]any) error {
	s.turnID = turnID
	s.entries = entries
	return nil
}

func (s *recordingTranscriptRowsStore) ReplaceForSession(_ context.Context, sessionID string, entries []map[string]any) error {
	s.sessionID = sessionID
	s.sessionEntries = entries
	s.replaceSessions = append(s.replaceSessions, sessionID)
	s.needsBackfill = false
	return nil
}

func (s *recordingTranscriptRowsStore) UpsertRows(context.Context, string, []map[string]any) error {
	return nil
}

func (s *recordingTranscriptRowsStore) ListChangedAfterOrderKey(context.Context, string, string, int) (store.TranscriptRowDeltaPage, error) {
	return store.TranscriptRowDeltaPage{}, nil
}

func (s *recordingTranscriptRowsStore) ListLatest(context.Context, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}

func (s *recordingTranscriptRowsStore) ListOldest(context.Context, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}

func (s *recordingTranscriptRowsStore) ListBefore(context.Context, string, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}

func (s *recordingTranscriptRowsStore) ListAround(context.Context, string, string, int, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}

func (s *recordingTranscriptRowsStore) ResolveCursorForTimelineID(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *recordingTranscriptRowsStore) NeedsBackfill(context.Context, string) (bool, error) {
	s.needsCalls++
	return s.needsBackfill, nil
}

type lockingTranscriptRowsStore struct {
	recordingTranscriptRowsStore
	t                    *testing.T
	lockHeld             bool
	needsBackfillTx      bool
	needsBackfillTxCalls int
	eventsReadUnderLock  bool
	replaceUnderLock     bool
	replaceSessionTx     bool
}

func (s *lockingTranscriptRowsStore) WithTranscriptMaterializationTx(ctx context.Context, _ string, fn func(context.Context, pgx.Tx) error) error {
	if s.lockHeld {
		s.t.Fatal("materialization lock re-entered")
	}
	s.lockHeld = true
	defer func() {
		s.lockHeld = false
	}()
	return fn(ctx, nil)
}

func (s *lockingTranscriptRowsStore) ReplaceForTurnTx(_ context.Context, _ pgx.Tx, _ string, turnID string, entries []map[string]any) error {
	if !s.lockHeld {
		s.t.Fatal("ReplaceForTurnTx called outside materialization lock")
	}
	if !s.eventsReadUnderLock {
		s.t.Fatal("ReplaceForTurnTx called before EventsForTurnTx under the same lock")
	}
	s.replaceUnderLock = true
	s.turnID = turnID
	s.entries = entries
	return nil
}

func (s *lockingTranscriptRowsStore) ReplaceForSessionTx(context.Context, pgx.Tx, string, []map[string]any) error {
	if !s.lockHeld {
		s.t.Fatal("ReplaceForSessionTx called outside materialization lock")
	}
	s.replaceSessionTx = true
	return nil
}

func (s *lockingTranscriptRowsStore) UpsertRowsTx(context.Context, pgx.Tx, string, []map[string]any) error {
	if !s.lockHeld {
		s.t.Fatal("UpsertRowsTx called outside materialization lock")
	}
	return nil
}

func (s *lockingTranscriptRowsStore) NeedsBackfillTx(context.Context, pgx.Tx, string) (bool, error) {
	if !s.lockHeld {
		s.t.Fatal("NeedsBackfillTx called outside materialization lock")
	}
	s.needsBackfillTxCalls++
	return s.needsBackfillTx, nil
}

type txAwareSessionEventStore struct {
	fakeSessionEventStore
	rows *lockingTranscriptRowsStore
}

func (s txAwareSessionEventStore) EventsForTurnAfterTx(ctx context.Context, _ pgx.Tx, tankSessionID, turnID, afterOrderKey string, limit int) (store.SessionEventPage, error) {
	if !s.rows.lockHeld {
		s.rows.t.Fatal("EventsForTurnAfterTx called outside materialization lock")
	}
	s.rows.eventsReadUnderLock = true
	return s.fakeSessionEventStore.EventsForTurnAfter(ctx, tankSessionID, turnID, afterOrderKey, limit)
}

func (s txAwareSessionEventStore) ListBySessionTx(ctx context.Context, _ pgx.Tx, tankSessionID string, cursor store.SessionEventCursor, limit int) (store.SessionEventPage, error) {
	if !s.rows.lockHeld {
		s.rows.t.Fatal("ListBySessionTx called outside materialization lock")
	}
	return s.fakeSessionEventStore.ListBySession(ctx, tankSessionID, cursor, limit)
}

func TestTranscriptRowsMaterializerStoresProjectedRowsForTurn(t *testing.T) {
	turnEvents := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "do work",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool-start", "002", "item.started", "tool", "codex", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":    "command_execution",
			"command": "go test ./...",
		}),
		projectionTestEvent("tool-done", "003", "item.completed", "tool", "codex", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":   "command_execution",
			"output": "ok",
		}),
		projectionTestEvent("final", "004", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:msg-1", map[string]any{
			"kind": "message",
			"text": "done",
		}),
		projectionTestEvent("terminal", "005", "turn.completed", "runner", "codex", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:msg-1")),
	}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	if err := materializer.RefreshEvent(context.Background(), turnEvents[len(turnEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}

	if rowStore.turnID != "turn-1" {
		t.Fatalf("turnID = %q, want turn-1", rowStore.turnID)
	}
	if got, want := len(rowStore.entries), 3; got != want {
		t.Fatalf("entries = %d, want %d: %#v", got, want, rowStore.entries)
	}
	if rowStore.entries[1]["kind"] != "turn_activity" {
		t.Fatalf("middle entry kind = %v, want turn_activity", rowStore.entries[1]["kind"])
	}
	activity := rowStore.entries[1]["activity"].(map[string]any)
	if activity["active"] == true || activity["status"] == "active" {
		t.Fatalf("completed turn activity rendered active: %#v", activity)
	}
	if _, hasInlineChildren := rowStore.entries[1]["entries"]; hasInlineChildren {
		t.Fatalf("materialized transcript row inlined activity children: %#v", rowStore.entries[1])
	}
}

func TestTranscriptRowsMaterializerRefreshesWholeSessionForBackgroundWakeTurn(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "Run CI and tell me when it passes.",
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("task-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": "task-ci",
			"status":  "running",
			"summary": "CI check",
		}),
		projectionTestEvent("waiting-final", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:waiting", map[string]any{
			"kind": "message",
			"text": "I will wait for CI and check back when it finishes.",
		}),
		projectionTestEvent("turn-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:waiting")),
		projectionTestEvent("task-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": "task-ci",
			"status":  "completed",
			"summary": "CI passed",
		}),
		projectionTestEvent("wake-submitted", "007", "turn.submitted", "runner", "tank", "turn_bgtask-task-ci", "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "task-ci", "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("wake-final", "008", "item.completed", "assistant", "claude", "turn_bgtask-task-ci", "turn_bgtask-task-ci:item:final", map[string]any{
			"kind": "message",
			"text": "CI passed. The branch is ready.",
		}),
		projectionTestEvent("wake-terminal", "009", "turn.completed", "runner", "claude", "turn_bgtask-task-ci", "", projectionFinalAnswerPayload("turn_bgtask-task-ci:item:final")),
	}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	if err := materializer.RefreshEvent(context.Background(), events[len(events)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}

	if rowStore.turnID != "" {
		t.Fatalf("ReplaceForTurn should not be used for wake turns, got turnID=%q entries=%#v", rowStore.turnID, rowStore.entries)
	}
	if rowStore.sessionID != "63" {
		t.Fatalf("sessionID = %q, want full-session replacement for wake turn", rowStore.sessionID)
	}
	if got, want := len(rowStore.sessionEntries), 3; got != want {
		t.Fatalf("session entries = %d, want user + parked origin shell + resumed final: %#v", got, rowStore.sessionEntries)
	}
	if got := transcriptMapString(rowStore.sessionEntries[1], "kind"); got != "turn_activity" {
		t.Fatalf("second session entry kind = %q, want parked origin shell: %#v", got, rowStore.sessionEntries[1])
	}
	final := rowStore.sessionEntries[2]
	if got, want := transcriptMapString(final, "turnId"), "turn-1"; got != want {
		t.Fatalf("final turnId = %q, want parent: %#v", got, final)
	}
	if got, want := transcriptMapString(final, "backendTurnId"), "turn_bgtask-task-ci"; got != want {
		t.Fatalf("final backendTurnId = %q, want wake turn: %#v", got, final)
	}
}

func TestTranscriptRowsMaterializerInputAnsweredRefreshesAssistantQuestionRow(t *testing.T) {
	questionTimelineID := "turn-question:item:toolu_ask"
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "ask me",
		}),
		projectionTestEvent("msg", "002", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Pick one",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-question",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          questionTimelineID,
				"provider_timeline_id": "turn-1:item:toolu_ask",
				"questions": []any{
					map[string]any{"question": "Pick one"},
				},
			},
		}),
		projectionTestEvent("q-submit", "003", "turn.submitted", "runner", "tank", "turn-question", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("awaiting", "004", "turn.awaiting_input", "runner", "claude", "turn-question", questionTimelineID, map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-question",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          questionTimelineID,
			"provider_timeline_id": "turn-1:item:toolu_ask",
			"questions": []any{
				map[string]any{"question": "Pick one"},
			},
		}),
		projectionTestEvent("answer", "005", "turn.input_answered", "user", "tank", "turn-question", questionTimelineID+":answer", map[string]any{
			"question_timeline_id": questionTimelineID,
			"provider_item_id":     "toolu_ask",
			"answers": map[string]any{
				"Pick one": []any{"Yes"},
			},
		}),
	}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	if err := materializer.RefreshEvent(context.Background(), events[len(events)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}

	if rowStore.sessionID != "63" {
		t.Fatalf("sessionID = %q, want full-session rematerialization", rowStore.sessionID)
	}
	var assistant map[string]any
	for _, entry := range rowStore.sessionEntries {
		if entry["role"] == "assistant" {
			assistant = entry
			break
		}
	}
	if assistant == nil {
		t.Fatalf("assistant question row missing from sessionEntries: %#v", rowStore.sessionEntries)
	}
	awaiting, _ := assistant["awaitingInput"].(map[string]any)
	if awaiting["answered"] != true {
		t.Fatalf("assistant awaitingInput.answered = %v, want true: %#v", awaiting["answered"], awaiting)
	}
	if answers, _ := awaiting["answers"].(map[string]any); answers == nil {
		t.Fatalf("assistant awaitingInput.answers missing: %#v", awaiting)
	}
}

func TestTranscriptRowsMaterializerLocksReadProjectionAndReplace(t *testing.T) {
	turnEvents := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "do work",
		}),
		projectionTestEvent("note", "002", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:note", map[string]any{
			"kind": "message",
			"text": "working",
		}),
		projectionTestEvent("final", "003", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:final", map[string]any{
			"kind": "message",
			"text": "done",
		}),
		projectionTestEvent("terminal", "004", "turn.completed", "runner", "codex", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:final")),
	}
	rowStore := &lockingTranscriptRowsStore{t: t}
	eventStore := txAwareSessionEventStore{
		fakeSessionEventStore: fakeSessionEventStore{
			pages: map[string]store.SessionEventPage{
				"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
			},
		},
		rows: rowStore,
	}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	if err := materializer.RefreshEvent(context.Background(), turnEvents[len(turnEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}

	if !rowStore.eventsReadUnderLock {
		t.Fatal("EventsForTurnTx was not called under materialization lock")
	}
	if !rowStore.replaceUnderLock {
		t.Fatal("ReplaceForTurnTx was not called under materialization lock")
	}
}

func TestTranscriptRowsMaterializerEnsureSessionBackfillsStaleSession(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("turn-1:user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "hello",
		}),
	}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{needsBackfill: true}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	if err := materializer.EnsureSession(context.Background(), "63"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if rowStore.needsCalls < 2 {
		t.Fatalf("NeedsBackfill calls = %d, want initial check and pre-replace recheck", rowStore.needsCalls)
	}
	if rowStore.sessionID != "63" {
		t.Fatalf("sessionID = %q, want 63", rowStore.sessionID)
	}
	if len(rowStore.sessionEntries) != 1 || rowStore.sessionEntries[0]["id"] != "turn-1:user" {
		t.Fatalf("sessionEntries = %#v", rowStore.sessionEntries)
	}
}

func TestTranscriptRowsMaterializerEnsureSessionSkipsFreshSession(t *testing.T) {
	rowStore := &recordingTranscriptRowsStore{needsBackfill: false}
	materializer := transcriptRowsMaterializer{
		events: fakeSessionEventStore{pages: map[string]store.SessionEventPage{
			"": {Events: []map[string]any{
				projectionTestEvent("turn-1:user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
					"text": "hello",
				}),
			}, FoundNewest: true},
		}},
		rows: rowStore,
	}

	if err := materializer.EnsureSession(context.Background(), "63"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if rowStore.needsCalls != 1 {
		t.Fatalf("NeedsBackfill calls = %d, want 1", rowStore.needsCalls)
	}
	if len(rowStore.replaceSessions) != 0 {
		t.Fatalf("ReplaceForSession called for fresh session: %#v", rowStore.replaceSessions)
	}
}

func TestTranscriptRowsMaterializerBackfillRechecksUnderLock(t *testing.T) {
	rowStore := &lockingTranscriptRowsStore{t: t, needsBackfillTx: false}
	eventStore := txAwareSessionEventStore{
		fakeSessionEventStore: fakeSessionEventStore{
			pages: map[string]store.SessionEventPage{
				"": {Events: []map[string]any{
					projectionTestEvent("turn-1:user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
						"text": "hello",
					}),
				}, FoundNewest: true},
			},
		},
		rows: rowStore,
	}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	backfilled, err := materializer.BackfillSession(context.Background(), "63")
	if err != nil {
		t.Fatalf("BackfillSession: %v", err)
	}
	if backfilled {
		t.Fatal("BackfillSession reported backfilled after locked freshness recheck returned fresh")
	}
	if rowStore.needsBackfillTxCalls != 1 {
		t.Fatalf("NeedsBackfillTx calls = %d, want 1", rowStore.needsBackfillTxCalls)
	}
	if rowStore.replaceSessionTx {
		t.Fatal("ReplaceForSessionTx called after locked freshness recheck returned fresh")
	}
}
