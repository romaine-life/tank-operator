package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func shellFoldTestEntries() []map[string]any {
	return []map[string]any{
		{"id": "t1:user", "kind": "message", "role": "user", "turnId": "t1", "orderKey": "001", "time": "T1", "sourceEventId": "e1"},
		{"id": "t1:tool-1", "kind": "tool", "toolStatus": "completed", "turnId": "t1", "orderKey": "002", "time": "T2", "startedAt": "T2", "completedAt": "T3", "sourceEventId": "e2"},
		{"id": "t1:reason", "kind": "reasoning", "reasoning": map[string]any{"text": "thinking"}, "turnId": "t1", "orderKey": "003", "time": "T3", "sourceEventId": "e3"},
		{"id": "t1:note", "kind": "message", "role": "assistant", "text": "progress", "turnId": "t1", "orderKey": "004", "time": "T4", "sourceEventId": "e4"},
		{"id": "t1:final", "kind": "message", "role": "assistant", "text": "done", "turnId": "t1", "orderKey": "005", "time": "T5", "sourceEventId": "e5"},
		{"id": "t1:meta", "kind": "meta", "metaKind": "turn_progress", "turnId": "t1", "orderKey": "000", "meta": map[string]any{"title": "Turn queued", "severity": "info"}, "sourceEventId": "e0", "progressStatus": "submitted"},
	}
}

// TestTurnShellFoldResumable pins the property stages B2/B3 depend on: a fold
// fed a turn's entries in one pass, in two arbitrary chunks, or with an entry
// later revised in place, derives byte-identical bodies from every finish
// mode. This is what lets a future refresh resume a stored fold instead of
// re-reading the turn.
func TestTurnShellFoldResumable(t *testing.T) {
	entries := shellFoldTestEntries()
	terminal := turnTerminalProjection{
		TurnID:         "t1",
		Status:         "completed",
		OrderKey:       "006",
		SourceEventID:  "e6",
		FinalAnswerIDs: map[string]bool{"t1:final": true},
	}

	finishes := map[string]func(f *turnShellFold) (turnActivityBody, bool){
		"terminal": func(f *turnShellFold) (turnActivityBody, bool) {
			return f.finishTerminal(terminal, false, false)
		},
		"active": func(f *turnShellFold) (turnActivityBody, bool) {
			return f.finishActive("streaming")
		},
	}
	for name, finish := range finishes {
		t.Run(name, func(t *testing.T) {
			oneShot := newTurnShellFold("t1")
			for _, entry := range entries {
				oneShot.upsertEntry(entry)
			}
			wantBody, wantOK := finish(oneShot)

			for split := 0; split <= len(entries); split++ {
				resumed := newTurnShellFold("t1")
				for _, entry := range entries[:split] {
					resumed.upsertEntry(entry)
				}
				for _, entry := range entries[split:] {
					resumed.upsertEntry(entry)
				}
				gotBody, gotOK := finish(resumed)
				if gotOK != wantOK || !reflect.DeepEqual(gotBody, wantBody) {
					t.Fatalf("split %d diverged from one-shot fold", split)
				}
			}

			// Revision: the tool entry arrives as running first, then is
			// revised to its completed form — the steady-state shape of a
			// live refresh. The result must equal the one-shot fold of the
			// final revisions.
			revised := newTurnShellFold("t1")
			running := map[string]any{"id": "t1:tool-1", "kind": "tool", "toolStatus": "running", "turnId": "t1", "orderKey": "002", "time": "T2", "startedAt": "T2", "sourceEventId": "e2"}
			revised.upsertEntry(entries[0])
			revised.upsertEntry(running)
			for _, entry := range entries[1:] {
				revised.upsertEntry(entry)
			}
			gotBody, gotOK := finish(revised)
			if gotOK != wantOK || !reflect.DeepEqual(gotBody, wantBody) {
				t.Fatalf("revision-fed fold diverged from one-shot fold")
			}
		})
	}
}

// TestTurnShellFoldFixtureShellParity replays the #1051 incident fixtures and
// asserts the fold-driven pipeline still derives a shell for every turn the
// ledger closed with compacted activity — a coarse fixture-level guard on the
// refactor, on top of the projection suite's exact-output pins.
func TestTurnShellFoldFixtureShellParity(t *testing.T) {
	for _, fixture := range []string{"slot1_session_159_events.json", "slot1_session_160_events.json", "slot1_session_161_events.json"} {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var events []map[string]any
			if err := json.Unmarshal(raw, &events); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			projection := projectTranscriptEvents(events)
			shells := 0
			for _, entry := range projection.Entries {
				if transcriptMapString(entry, "kind") == "turn_activity" {
					shells++
					if transcriptMapString(entry, "turnId") == "" {
						t.Fatalf("shell without turnId: %#v", entry)
					}
				}
			}
			if shells == 0 {
				t.Fatalf("fixture projected no turn_activity shells")
			}
			if len(projection.ActivityBodies) == 0 {
				t.Fatalf("fixture projected no activity bodies")
			}
		})
	}
}

