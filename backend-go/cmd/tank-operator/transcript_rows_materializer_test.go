package main

import (
	"context"
	"testing"

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
		projectionTestEvent("terminal", "005", "turn.completed", "runner", "codex", "turn-1", "", nil),
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
	if _, hasInlineChildren := rowStore.entries[1]["entries"]; hasInlineChildren {
		t.Fatalf("materialized transcript row inlined activity children: %#v", rowStore.entries[1])
	}
}
