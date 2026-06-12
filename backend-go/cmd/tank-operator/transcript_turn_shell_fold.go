package main

// turnShellFold is the per-turn accumulator behind every turn_activity shell.
// It is stage B1 of the checkpointed-projector plan (tank-operator#1051): the
// shell derivation used to slice global entry lists by positional index
// (turnIndexes / finalAnswerProjectedIndexes over []entries), which coupled a
// turn's shell to whole-session state and forced O(session) re-projection per
// refresh. The fold owns one turn's entries, accepts them one at a time in
// projection order with upsert-by-id revision semantics, and derives the same
// turnActivityBody the positional code produced — proven by the existing
// projection suite plus the resumability property test
// (transcript_turn_shell_fold_test.go).
//
// Stage boundaries, so later PRs read coherently:
//   - B1 (this): entries are still retained per fold and the summary is still
//     computed at finish over the retained slice — identical math, fold-shaped
//     API, positional coupling gone.
//   - B2 turns the finish-time summary into running aggregates with row-delta
//     emission; B3 makes the fold state durable per session.
type turnShellFold struct {
	turnID string
	// order holds entry ids in first-seen projection order; entries holds the
	// latest revision per id. Per-turn projection order is the turn's
	// subsequence of the global flat-entry order, so iterating order yields
	// exactly what the positional code saw for this turn.
	order   []string
	entries map[string]map[string]any
}

func newTurnShellFold(turnID string) *turnShellFold {
	return &turnShellFold{
		turnID:  turnID,
		entries: map[string]map[string]any{},
	}
}

// upsertEntry folds one projected entry into the turn. A later revision of an
// already-seen id (an item completing, a question being answered) replaces
// the prior revision in place, keeping first-seen order — the property that
// makes the fold resumable across refreshes.
func (f *turnShellFold) upsertEntry(entry map[string]any) {
	if f == nil || entry == nil {
		return
	}
	id := transcriptMapString(entry, "id")
	if id == "" {
		return
	}
	if _, seen := f.entries[id]; !seen {
		f.order = append(f.order, id)
	}
	f.entries[id] = entry
}

func (f *turnShellFold) entriesInOrder() []map[string]any {
	out := make([]map[string]any, 0, len(f.order))
	for _, id := range f.order {
		if entry := f.entries[id]; entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

// groupTurnShellFolds routes every turn-tagged entry to its turn's fold,
// returning folds in first-seen turn order — the single pass that replaces
// the positional turnIndexes maps.
func groupTurnShellFolds(entries []map[string]any) []*turnShellFold {
	byTurn := map[string]*turnShellFold{}
	var order []*turnShellFold
	for _, entry := range entries {
		turnID := transcriptMapString(entry, "turnId")
		if turnID == "" {
			continue
		}
		fold, ok := byTurn[turnID]
		if !ok {
			fold = newTurnShellFold(turnID)
			byTurn[turnID] = fold
			order = append(order, fold)
		}
		fold.upsertEntry(entry)
	}
	return order
}

// finishTerminal derives the shell body for a turn that reached a durable
// terminal. Mirrors the membership rules the positional code applied: user
// messages stay out of the body (wake prompts are body content), the
// terminal's own meta chip stays out except on wake turns, turn-progress chips
// never enter, and on a completed turn the promoted final answers leave the
// compacted set (unless the turn is a parked continuation, whose "final"
// answer is not a hand-off). Returns ok=false when the turn contributed no
// body content.
func (f *turnShellFold) finishTerminal(terminal turnTerminalProjection, backgroundWake, continuation bool) (turnActivityBody, bool) {
	entries := f.entriesInOrder()
	if terminal.Status == "completed" && len(terminal.FinalAnswerIDs) == 0 && projectedEntriesHaveAssistantMessage(entries) {
		recordTranscriptMaterializationInvariantViolation("completed_turn_missing_final_answer", "completed")
	}
	finalIDs := map[string]bool{}
	if terminal.Status == "completed" {
		for _, entry := range entries {
			id := transcriptMapString(entry, "id")
			if terminal.FinalAnswerIDs[id] && isProjectedAssistantMessage(entry) {
				finalIDs[id] = true
			}
		}
		if len(terminal.FinalAnswerIDs) > 0 && len(finalIDs) == 0 {
			recordTranscriptMaterializationInvariantViolation("completed_turn_final_answer_missing_entry", "completed")
		}
	}
	var compacted []map[string]any
	var activityEntries []map[string]any
	for _, entry := range entries {
		if (isProjectedUserMessage(entry) && !isProjectionWakePrompt(entry)) ||
			(!backgroundWake && isProjectionTerminalMetaEntry(entry, terminal)) ||
			isProjectionTurnProgress(entry) {
			continue
		}
		activityEntries = append(activityEntries, entry)
		if (!finalIDs[transcriptMapString(entry, "id")] || continuation) && !isProjectionAwaitingInputEntry(entry) {
			compacted = append(compacted, entry)
		}
	}
	if len(activityEntries) == 0 {
		return turnActivityBody{}, false
	}
	return makeTurnActivityBody(f.turnID, terminal.Status, activityEntries, compacted, false), true
}

// finishAwaitingInputHandoff derives the shell body for a non-terminal turn
// whose Tank-visible response was an AskUserQuestion hand-off: the assistant
// message carrying the question set plays the final-answer role. Returns
// ok=false when the turn has no such hand-off or no body content.
func (f *turnShellFold) finishAwaitingInputHandoff() (turnActivityBody, bool) {
	entries := f.entriesInOrder()
	handoffIDs := map[string]bool{}
	for _, entry := range entries {
		if !isProjectedAssistantMessage(entry) {
			continue
		}
		awaiting, _ := entry["awaitingInput"].(map[string]any)
		questionTurnID := transcriptMapString(awaiting, "questionTurnId")
		if questionTurnID == "" || questionTurnID == f.turnID {
			continue
		}
		askingTurnID := transcriptMapString(awaiting, "askingTurnId")
		if askingTurnID != "" && askingTurnID != f.turnID {
			continue
		}
		handoffIDs[transcriptMapString(entry, "id")] = true
	}
	if len(handoffIDs) == 0 {
		return turnActivityBody{}, false
	}
	var compacted []map[string]any
	var activityEntries []map[string]any
	for _, entry := range entries {
		if isProjectedUserMessage(entry) || isProjectionTurnProgress(entry) {
			continue
		}
		activityEntries = append(activityEntries, entry)
		if !handoffIDs[transcriptMapString(entry, "id")] && !isProjectionAwaitingInputEntry(entry) {
			compacted = append(compacted, entry)
		}
	}
	if len(activityEntries) == 0 {
		return turnActivityBody{}, false
	}
	activity := makeTurnActivityBody(f.turnID, "completed", activityEntries, compacted, false)
	activity.Summary["awaitingInputHandoff"] = true
	return activity, true
}

// finishActive derives the shell body for the session's live turn. Progress
// chips anchor the shell instead of joining the body; entries that already
// carry a terminal stamp stay out (a late entry race the positional code also
// excluded). Returns ok=false when the live turn has produced nothing yet.
func (f *turnShellFold) finishActive(runStatus string) (turnActivityBody, bool) {
	var activityEntries []map[string]any
	var progressEntries []map[string]any
	for _, entry := range f.entriesInOrder() {
		if transcriptMapString(entry, "turnTerminalStatus") != "" ||
			(isProjectedUserMessage(entry) && !isProjectionWakePrompt(entry)) {
			continue
		}
		if isProjectionTurnProgress(entry) {
			progressEntries = append(progressEntries, entry)
			continue
		}
		activityEntries = append(activityEntries, entry)
	}
	if len(activityEntries) == 0 && len(progressEntries) == 0 {
		return turnActivityBody{}, false
	}
	status := "active"
	if runStatus == "needs_input" {
		status = "needs_input"
	}
	compactedEntries := make([]map[string]any, 0, len(activityEntries))
	for _, entry := range activityEntries {
		if isProjectionAwaitingInputEntry(entry) {
			continue
		}
		compactedEntries = append(compactedEntries, entry)
	}
	body := makeTurnActivityBody(f.turnID, status, activityEntries, compactedEntries, true)
	if len(progressEntries) > 0 {
		applyActivityAnchorSummary(body.Summary, progressEntries, len(activityEntries) == 0)
	}
	return body, true
}

func projectedEntriesHaveAssistantMessage(entries []map[string]any) bool {
	for _, entry := range entries {
		if isProjectedAssistantMessage(entry) {
			return true
		}
	}
	return false
}
