package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type recordingTranscriptRowsStore struct {
	turnID  string
	entries []map[string]any
}

func (s *recordingTranscriptRowsStore) ReplaceForTurn(_ context.Context, _ string, turnID string, entries []map[string]any) error {
	s.turnID = turnID
	s.entries = entries
	return nil
}

func (s *recordingTranscriptRowsStore) ReplaceForSession(context.Context, string, []map[string]any) error {
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

func (s *recordingTranscriptRowsStore) BackfillSessionIDs(context.Context) ([]string, error) {
	return nil, nil
}

type lockingTranscriptRowsStore struct {
	recordingTranscriptRowsStore
	t                   *testing.T
	lockHeld            bool
	eventsReadUnderLock bool
	replaceUnderLock    bool
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
	return nil
}

func (s *lockingTranscriptRowsStore) UpsertRowsTx(context.Context, pgx.Tx, string, []map[string]any) error {
	if !s.lockHeld {
		s.t.Fatal("UpsertRowsTx called outside materialization lock")
	}
	return nil
}

type txAwareSessionEventStore struct {
	fakeSessionEventStore
	rows *lockingTranscriptRowsStore
}

func (s txAwareSessionEventStore) EventsForTurnTx(ctx context.Context, _ pgx.Tx, tankSessionID, turnID string, limit int) (store.SessionEventPage, error) {
	if !s.rows.lockHeld {
		s.rows.t.Fatal("EventsForTurnTx called outside materialization lock")
	}
	s.rows.eventsReadUnderLock = true
	return s.fakeSessionEventStore.EventsForTurn(ctx, tankSessionID, turnID, limit)
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
