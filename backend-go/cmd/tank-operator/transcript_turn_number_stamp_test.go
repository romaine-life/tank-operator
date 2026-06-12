package main

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// turnNumberStampTestEvents builds a completed turn with compacted tool
// activity, so projection produces a turn_activity shell (a turn whose only
// output is the final answer is promoted whole and carries no shell to stamp).
func turnNumberStampTestEvents() []map[string]any {
	return []map[string]any{
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
}

func findTurnActivityShell(entries []map[string]any) map[string]any {
	for _, entry := range entries {
		if entry["kind"] == "turn_activity" {
			return entry
		}
	}
	return nil
}

// TestMaterializerStampsDurableTurnNumber proves the materializer enriches the
// turn_activity shell with the durable number from session_turns, so the SPA
// reads turnNumber off the timeline projection rather than computing it from
// render position.
func TestMaterializerStampsDurableTurnNumber(t *testing.T) {
	turnEvents := turnNumberStampTestEvents()
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{
		events: eventStore,
		rows:   rowStore,
		turns:  fakeSessionTurnStore{byTurnID: map[string]int64{"turn-1": 7}},
	}

	if err := materializer.RefreshEvent(context.Background(), turnEvents[len(turnEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}
	shell := findTurnActivityShell(rowStore.entries)
	if shell == nil {
		t.Fatalf("no turn_activity shell in %#v", rowStore.entries)
	}
	if shell["turnNumber"] != int64(7) {
		t.Fatalf("shell turnNumber = %#v, want int64(7)", shell["turnNumber"])
	}
}

// TestMaterializerRecordsMissingTurnNumber proves that a shell whose turn has no
// durable number does not fail the projection (it still renders without a
// number) and is counted on the missing-number invariant counter that
// TankTurnNumberMissing alerts on.
func TestMaterializerRecordsMissingTurnNumber(t *testing.T) {
	before := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize"))
	turnEvents := turnNumberStampTestEvents()
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{
		events: eventStore,
		rows:   rowStore,
		// Numbering is active (not the stub) but this turn has no row.
		turns: fakeSessionTurnStore{byTurnID: map[string]int64{}},
	}

	if err := materializer.RefreshEvent(context.Background(), turnEvents[len(turnEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}
	shell := findTurnActivityShell(rowStore.entries)
	if shell == nil {
		t.Fatalf("no turn_activity shell in %#v", rowStore.entries)
	}
	if _, stamped := shell["turnNumber"]; stamped {
		t.Fatalf("unnumbered turn should not be stamped: %#v", shell["turnNumber"])
	}
	after := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize"))
	if after-before != 1 {
		t.Fatalf("missing-number counter delta = %v, want 1", after-before)
	}
}

// TestMaterializerDoesNotCountUnnumberedWakeTurnShells pins the migration-0139
// contract at the observability boundary: background-wake continuation turns
// (turn_bgtask-<task>) are excluded from durable numbering by design, so a
// wake-turn shell materializing without a number is intended state — it must
// not increment the missing-number counter TankTurnNumberMissing alerts on,
// and must still render unstamped. During the 2026-06-11 incident this
// mis-count produced 12 standing false alerts.
func TestMaterializerDoesNotCountUnnumberedWakeTurnShells(t *testing.T) {
	before := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize"))
	// A wake turn with no origin-task context in the ledger: the projection
	// cannot fold it into a parent, so it materializes as a standalone
	// turn_bgtask-* shell — the production shape behind the false alerts.
	wakeEvents := []map[string]any{
		projectionTestEvent("wake-submitted", "001", "turn.submitted", "runner", "tank", "turn_bgtask-orphan", "", map[string]any{
			"status": "submitted", "source": "background-task", "task_id": "orphan",
			"prompt": "A background task you started earlier has finished.",
		}),
		projectionTestEvent("wake-tool", "002", "item.completed", "tool", "claude", "turn_bgtask-orphan", "turn_bgtask-orphan:item:tool-1", map[string]any{
			"kind": "command_execution", "output": "ok",
		}),
		projectionTestEvent("wake-final", "003", "item.completed", "assistant", "claude", "turn_bgtask-orphan", "turn_bgtask-orphan:item:final", map[string]any{
			"kind": "message", "text": "done",
		}),
		projectionTestEvent("wake-terminal", "004", "turn.completed", "runner", "claude", "turn_bgtask-orphan", "", projectionFinalAnswerPayload("turn_bgtask-orphan:item:final")),
	}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: wakeEvents, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{
		events: eventStore,
		rows:   rowStore,
		// Numbering active, and (correctly, per 0139) no row for the wake turn.
		turns: fakeSessionTurnStore{byTurnID: map[string]int64{}},
	}

	if err := materializer.RefreshEvent(context.Background(), wakeEvents[len(wakeEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}
	shell := findTurnActivityShell(rowStore.sessionEntries)
	if shell == nil {
		t.Fatalf("no wake turn_activity shell in session entries: %#v", rowStore.sessionEntries)
	}
	if got := transcriptMapString(shell, "turnId"); got != "turn_bgtask-orphan" {
		t.Fatalf("shell turnId = %q, want the standalone wake turn", got)
	}
	if _, stamped := shell["turnNumber"]; stamped {
		t.Fatalf("wake-turn shell must not be stamped: %#v", shell["turnNumber"])
	}
	if after := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize")); after != before {
		t.Fatalf("missing-number counter must not count by-design-unnumbered wake turns: delta=%v", after-before)
	}
}

// TestMaterializerSkipsStampingWhenNumberingInactive proves the stub store
// (no-Postgres mode) neither stamps nor spams the missing-number counter.
func TestMaterializerSkipsStampingWhenNumberingInactive(t *testing.T) {
	before := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize"))
	turnEvents := turnNumberStampTestEvents()
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
		},
	}
	rowStore := &recordingTranscriptRowsStore{}
	materializer := transcriptRowsMaterializer{
		events: eventStore,
		rows:   rowStore,
		turns:  store.StubSessionTurnStore{},
	}

	if err := materializer.RefreshEvent(context.Background(), turnEvents[len(turnEvents)-1]); err != nil {
		t.Fatalf("RefreshEvent: %v", err)
	}
	shell := findTurnActivityShell(rowStore.entries)
	if shell == nil {
		t.Fatalf("no turn_activity shell in %#v", rowStore.entries)
	}
	if _, stamped := shell["turnNumber"]; stamped {
		t.Fatalf("stub numbering should not stamp: %#v", shell["turnNumber"])
	}
	if after := testutil.ToFloat64(turnNumberMissingTotal.WithLabelValues("materialize")); after != before {
		t.Fatalf("stub numbering must not record missing: delta=%v", after-before)
	}
}
