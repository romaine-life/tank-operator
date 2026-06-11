package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These tests replay the REAL durable ledgers of three Glimmung test-slot
// sessions captured on 2026-06-11 (tank-operator-slot-1 sessions 159/160/161,
// the bug museums behind tank-operator#1037's failed validation) through the
// pure transcript projection. They pin the "durable transcript is not durable
// for parked turns" defect family:
//
//   - session 161 (codex, "1037-codex-bg-parity"): seven turns; turns 2-7 each
//     parked on a background shell. The shipped projection suppressed those
//     turns' activity shells AND compacted their final answers, leaving the
//     durable read model with bare user prompts — every ack the user saw lived
//     only in the live SSE stream, and the missing shells are why the turn
//     pager rendered "Current turn" instead of numbers.
//   - session 160 (antigravity, "1036-fold-round3"): the fold re-homed the
//     wake replies ("THE TIMER FIRED.") onto the asking turns, but the parked
//     turns' own acks were annihilated the same way.
//   - session 159 (antigravity, pre-fold builds): the old unfolded shape plus
//     restart-replay artifacts; replayed to keep the fail-soft path honest.
//
// The invariant under test is the repo's first principle applied to the
// projection: every projected entry gets exactly one durable home. A parked
// ("continuation") turn keeps its shell — parked is a state on the shell, not
// grounds for suppression — and content compacted into a body is only droppable
// from the flat stream when the shell that names it survives.
func loadReplayLedger(t *testing.T, name string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read replay fixture %s: %v", name, err)
	}
	var events []map[string]any
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("parse replay fixture %s: %v", name, err)
	}
	if len(events) == 0 {
		t.Fatalf("replay fixture %s is empty", name)
	}
	return events
}

type replayLedgerFacts struct {
	// realTurnOrder lists non-wake turn ids in first-appearance order.
	realTurnOrder []string
	// userMessageIDs maps real turn id -> the durable user_message.created id.
	userMessageIDs map[string]string
	// terminalTurns is the set of turn ids carrying a turn terminal event.
	terminalTurns map[string]bool
	// assistantProse lists every non-empty assistant message item in the
	// ledger: the content whose durability the projection must preserve.
	assistantProse []replayProse
	// taskTurns maps shell task id -> originating turn id.
	taskTurns map[string]string
}

type replayProse struct {
	ID     string
	TurnID string
	Text   string
}

func replayFactsFromEvents(events []map[string]any) replayLedgerFacts {
	facts := replayLedgerFacts{
		userMessageIDs: map[string]string{},
		terminalTurns:  map[string]bool{},
		taskTurns:      map[string]string{},
	}
	seenTurn := map[string]bool{}
	for _, event := range orderedTranscriptEvents(events) {
		turnID := transcriptString(event, "turn_id")
		switch transcriptString(event, "type") {
		case "user_message.created":
			if turnID == "" || isBackgroundWakeTurnID(turnID) {
				continue
			}
			if !seenTurn[turnID] {
				seenTurn[turnID] = true
				facts.realTurnOrder = append(facts.realTurnOrder, turnID)
			}
			// Projected row ids are the durable timeline_id (turn_X:user).
			facts.userMessageIDs[turnID] = transcriptString(event, "timeline_id")
		case "turn.completed", "turn.failed", "turn.interrupted":
			if turnID != "" {
				facts.terminalTurns[turnID] = true
			}
		case "item.completed":
			kind := transcriptPayloadString(event, "kind")
			if kind != "message" && kind != "agent_message" {
				continue
			}
			text := strings.TrimSpace(transcriptPayloadString(event, "text"))
			if text == "" {
				continue
			}
			// Projected item ids are the durable timeline_id (turn_X:item:...).
			facts.assistantProse = append(facts.assistantProse, replayProse{
				ID:     transcriptString(event, "timeline_id"),
				TurnID: turnID,
				Text:   text,
			})
		case "shell_task.started":
			if taskID := transcriptPayloadString(event, "task_id"); taskID != "" && turnID != "" {
				facts.taskTurns[taskID] = turnID
			}
		}
	}
	return facts
}

// durableEntryIDs returns the ids of rows that survive into the settled
// projection plus, for every surviving turn_activity shell, the ids the shell
// names as its reachable compacted/activity children.
func durableHomes(projection transcriptProjection) (entryIDs map[string]bool, shellTurnIDs map[string]bool, shellNamedIDs map[string]bool) {
	entryIDs = map[string]bool{}
	shellTurnIDs = map[string]bool{}
	shellNamedIDs = map[string]bool{}
	for _, entry := range projection.Entries {
		id := transcriptMapString(entry, "id")
		if id != "" {
			entryIDs[id] = true
		}
		if transcriptMapString(entry, "kind") != "turn_activity" {
			continue
		}
		turnID := transcriptMapString(entry, "turnId")
		if turnID != "" {
			shellTurnIDs[turnID] = true
		}
		// A shell makes its body reachable: the Turns view expands the body
		// through the turn-activity endpoint. Content listed here has a
		// durable home only because this shell row exists.
		if ids, ok := entry["activityIds"].([]string); ok {
			for _, child := range ids {
				shellNamedIDs[child] = true
			}
		}
		if ids, ok := entry["activityIds"].([]any); ok {
			for _, child := range ids {
				if s, ok := child.(string); ok {
					shellNamedIDs[s] = true
				}
			}
		}
		if body, ok := projection.ActivityBodies[turnID]; ok {
			for _, child := range body.Entries {
				if id := transcriptMapString(child, "id"); id != "" {
					shellNamedIDs[id] = true
				}
			}
		}
	}
	return entryIDs, shellTurnIDs, shellNamedIDs
}

func assertReplayDurableHomes(t *testing.T, fixture string) (transcriptProjection, replayLedgerFacts) {
	t.Helper()
	events := loadReplayLedger(t, fixture)
	facts := replayFactsFromEvents(events)
	projection := projectTranscriptEvents(events)
	entryIDs, shellTurnIDs, shellNamedIDs := durableHomes(projection)

	// Every real user message survives as a settled row.
	for turnID, userID := range facts.userMessageIDs {
		if !entryIDs[userID] {
			t.Errorf("user message for turn %s lost from settled projection (id %s)", turnID, userID)
		}
	}

	// No compacted body without a surviving container: whenever the projection
	// compacts content into a turn's activity body, the shell that names that
	// body must survive into the settled projection — except for wake turns
	// whose body folds into a known originating turn (the parent's shell is
	// then the container). Suppressing a shell whose body holds content is the
	// annihilation this replay pins.
	wakeParents := backgroundWakeParentTurnsFromEvents(events)
	for turnID, body := range projection.ActivityBodies {
		if len(body.CompactedEntryIDs) == 0 {
			continue
		}
		if wakeParents[turnID] != "" {
			continue
		}
		if !shellTurnIDs[turnID] {
			t.Errorf("turn %s compacted %d entries but has no surviving turn_activity shell", turnID, len(body.CompactedEntryIDs))
		}
	}

	// Every real turn that originated a background shell task and reached a
	// terminal keeps its shell — parked is a state on the shell, not grounds
	// for suppression. This is what carries the stamped turn number; dropping
	// it is how turns 2-7 of session 161 rendered as "Current turn".
	for taskID, turnID := range facts.taskTurns {
		if isBackgroundWakeTurnID(turnID) || !facts.terminalTurns[turnID] {
			continue
		}
		if !shellTurnIDs[turnID] {
			t.Errorf("parked turn %s (task %s) has no turn_activity shell in the settled projection", turnID, taskID)
		}
	}

	// Every non-empty assistant message in the ledger has exactly one durable
	// home: a settled row (possibly re-homed onto the originating turn with
	// backendTurnId provenance) or membership in a surviving shell's body.
	for _, prose := range facts.assistantProse {
		if entryIDs[prose.ID] || shellNamedIDs[prose.ID] {
			continue
		}
		t.Errorf("assistant prose annihilated — no durable home for %q (item %s, turn %s)", clipReplayText(prose.Text), prose.ID, prose.TurnID)
	}

	// Wake turns with a derivable originating turn must not surface as
	// standalone settled turns; their content re-homes onto the parent.
	for _, entry := range projection.Entries {
		turnID := transcriptMapString(entry, "turnId")
		if parent := wakeParents[turnID]; parent != "" {
			t.Errorf("settled row %s still owned by wake turn %s (parent %s known)", transcriptMapString(entry, "id"), turnID, parent)
		}
	}

	// Settled rows and every reachable activity body stay in ascending
	// order-key order — the fold must not append later wake content above
	// earlier in-turn content.
	assertAscendingOrderKeys(t, "settled entries", projection.Entries)
	for turnID, body := range projection.ActivityBodies {
		if !shellTurnIDs[turnID] {
			continue
		}
		assertAscendingOrderKeys(t, "activity body "+turnID, body.Entries)
	}

	return projection, facts
}

func assertAscendingOrderKeys(t *testing.T, label string, entries []map[string]any) {
	t.Helper()
	last := ""
	for _, entry := range entries {
		key := transcriptMapString(entry, "orderKey")
		if key == "" {
			continue
		}
		if last != "" && key < last {
			t.Errorf("%s out of order: %q after %q (id %s)", label, key, last, transcriptMapString(entry, "id"))
			return
		}
		last = key
	}
}

func clipReplayText(text string) string {
	if len(text) > 60 {
		return text[:60] + "…"
	}
	return text
}

func settledTextsByTurn(projection transcriptProjection) map[string][]string {
	out := map[string][]string{}
	for _, entry := range projection.Entries {
		if transcriptMapString(entry, "kind") != "message" {
			continue
		}
		turnID := transcriptMapString(entry, "turnId")
		out[turnID] = append(out[turnID], transcriptMapString(entry, "text"))
	}
	return out
}

func TestProjectTranscriptEventsReplaySlotSession161CodexBugMuseum(t *testing.T) {
	projection, facts := assertReplayDurableHomes(t, "slot1_session_161_events.json")

	if got, want := len(facts.realTurnOrder), 7; got != want {
		t.Fatalf("fixture shape changed: %d real turns, want %d", got, want)
	}

	// Turn 1 stays the fully-settled shape: prompt, shell, "READY".
	texts := settledTextsByTurn(projection)
	turn1 := facts.realTurnOrder[0]
	foundReady := false
	for _, text := range texts[turn1] {
		if strings.TrimSpace(text) == "READY" {
			foundReady = true
		}
	}
	if !foundReady {
		t.Errorf("turn 1 settled READY answer missing: %#v", texts[turn1])
	}

	// Turns 2-7 all parked on background shells; their acks ("Started … I'll
	// report when it completes.") are the prose session 788's user watched get
	// annihilated. assertReplayDurableHomes already proves a durable home per
	// prose item; this pins that the museum still exercises all six parked
	// turns.
	parked := 0
	for _, prose := range facts.assistantProse {
		if isBackgroundWakeTurnID(prose.TurnID) {
			continue
		}
		if prose.TurnID != turn1 {
			parked++
		}
	}
	if parked < 6 {
		t.Fatalf("fixture shape changed: only %d parked-turn prose items, want >= 6", parked)
	}
}

func TestProjectTranscriptEventsReplaySlotSession160AntigravityFoldMuseum(t *testing.T) {
	projection, facts := assertReplayDurableHomes(t, "slot1_session_160_events.json")

	if got, want := len(facts.realTurnOrder), 3; got != want {
		t.Fatalf("fixture shape changed: %d real turns, want %d", got, want)
	}

	// The wake replies stay settled, re-homed onto the asking turns with
	// backend-turn provenance preserved.
	wantFolded := map[string]string{
		"THE TIMER FIRED.":  facts.realTurnOrder[1],
		"ROUND FOUR FIRED.": facts.realTurnOrder[2],
	}
	for _, entry := range projection.Entries {
		text := strings.TrimSpace(transcriptMapString(entry, "text"))
		wantTurn, ok := wantFolded[text]
		if !ok {
			continue
		}
		delete(wantFolded, text)
		if got := transcriptMapString(entry, "turnId"); got != wantTurn {
			t.Errorf("folded wake reply %q owned by turn %s, want originating turn %s", text, got, wantTurn)
		}
		if transcriptMapString(entry, "backendTurnId") == "" {
			t.Errorf("folded wake reply %q lost backendTurnId provenance", text)
		}
	}
	for text := range wantFolded {
		t.Errorf("folded wake reply %q missing from settled projection", text)
	}
}

func TestProjectTranscriptEventsReplaySlotSession159PreFoldMuseum(t *testing.T) {
	// The pre-fold museum (broken builds, restart-replay artifacts) only has to
	// satisfy the generic invariants: nothing annihilated, order preserved,
	// shells for terminal turns.
	assertReplayDurableHomes(t, "slot1_session_159_events.json")
}

// sortedness helper kept out of assertReplayDurableHomes so fixture-shape
// drift produces a targeted failure instead of a confusing ordering error.
var _ = sort.StringsAreSorted
