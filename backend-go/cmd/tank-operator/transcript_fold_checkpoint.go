package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// sessionFoldMemo is stages B2+B3 of the checkpointed-projector plan
// (tank-operator#1051): the bounded fold state that lets the persister apply
// a flood-class event to the transcript-row projection without re-reading the
// session ledger. The memo is durable (session_transcript_fold_state, loaded
// and saved inside the same materialization transaction as the row writes),
// and deliberately conservative: any event the fold cannot handle with full
// confidence reports needsResync, and the batch pipeline — which remains the
// reference implementation, never a retired path — re-projects the session
// and reseeds the memo.
//
// What the memo retains is bounded by construction:
//   - OPEN items only (an item leaves the memo when it reaches a terminal
//     status; a later touch of an evicted id forces a resync);
//   - background tasks and turn usages/terminals, bounded by count;
//   - per-turn PRUNED entries: only the ~20 fields the shell summary math
//     and membership predicates read (turnFoldEntryFields) — tool payloads,
//     message bodies, and reasoning text never enter the memo. Shell rows are
//     rebuilt from these via the same B1 fold finishes and
//     buildTurnActivityShellRow the batch pipeline uses, so the summary math
//     has exactly one implementation.
//
// Versioning: bump sessionFoldMemoVersion whenever the memo shape or the
// projection semantics it snapshots change; a version mismatch on load is a
// resync, never a best-effort read of stale state.
const sessionFoldMemoVersion = 2

// sessionFoldMemoMaxBytes caps each serialized memo PART: the shared
// session part and every per-turn entry blob individually. A session with a
// part over the cap has the fold disabled durably (always batch-projected,
// exactly the pre-fold behavior) and counted — never silently truncated.
// Partitioning is what lets the incident-class sessions fold: their v1
// single-blob memos exceeded the cap (disabled_size=16 on first deploy),
// while their largest single turn fits comfortably.
const sessionFoldMemoMaxBytes = 1_500_000

type sessionFoldMemo struct {
	Version      int    `json:"version"`
	LastOrderKey string `json:"last_order_key"`
	RunStatus    string `json:"run_status"`
	ActiveTurnID string `json:"active_turn_id"`

	OpenItems    map[string]*projectionItem `json:"open_items,omitempty"`
	EvictedItems map[string]bool            `json:"evicted_items,omitempty"`

	Tasks           []*projectionBackgroundTask `json:"tasks,omitempty"`
	TaskProviderIDs map[string]bool             `json:"task_provider_ids,omitempty"`

	TurnUsages    map[string]turnUsageProjection    `json:"turn_usages,omitempty"`
	TurnTerminals map[string]turnTerminalProjection `json:"turn_terminals,omitempty"`

	BackgroundWakeTurns   map[string]bool   `json:"background_wake_turns,omitempty"`
	BackgroundWakeTaskIDs map[string]string `json:"background_wake_task_ids,omitempty"`
	WakeParents           map[string]string `json:"wake_parents,omitempty"`
	ContinuationTurns     map[string]bool   `json:"continuation_turns,omitempty"`

	// Turns holds pruned entries keyed by the entry's ORIGINAL turn. It is
	// deliberately excluded from the session part's serialization: each
	// turn's entry set persists as its own row
	// (session_transcript_fold_turns), loaded per batch for exactly the
	// turns the batch touches. TouchedTurns tracks which sets this in-memory
	// fold pass modified, so the save writes only those.
	Turns        map[string]*turnFoldEntries `json:"-"`
	TouchedTurns map[string]bool             `json:"-"`
}

type turnFoldEntries struct {
	Order   []string                  `json:"order"`
	Entries map[string]map[string]any `json:"entries"`
}

// turnFoldEntryFields is the exact field set the shell derivation reads:
// every membership predicate (isProjectedUserMessage, isProjectionWakePrompt,
// isProjectionTurnProgress, isProjectionAwaitingInputEntry,
// isProjectionTerminalMetaEntry, isProjectedAssistantMessage), the summary
// math in turnActivitySummaryMap, the anchor lookups in
// buildTurnActivityShellRow, and the terminal annotations. Pruning to this
// set is what keeps the memo small; if shell derivation grows a new field,
// add it here — the equivalence harness fails loudly if this set goes stale.
var turnFoldEntryFields = []string{
	"id", "kind", "metaKind", "role", "turnId", "backendTurnId",
	"orderKey", "time", "startedAt", "completedAt", "updatedAt",
	"toolStatus", "taskStatus", "severity", "sessionStatus",
	"progressStatus", "wakePrompt", "sourceEventId",
	"turnUsage", "usageObservation",
	"turnTerminalStatus", "turnTerminalAt", "turnTerminalEventId", "turnTerminalOrderKey",
	"activityEndOrderKey", "awaitingInput",
}

func pruneFoldEntry(entry map[string]any) map[string]any {
	out := make(map[string]any, len(turnFoldEntryFields)+1)
	for _, field := range turnFoldEntryFields {
		if v, ok := entry[field]; ok {
			out[field] = v
		}
	}
	// meta.severity feeds errorCount; the rest of meta is presentation.
	if meta := transcriptMap(entry, "meta"); meta != nil {
		if severity, ok := meta["severity"]; ok {
			out["meta"] = map[string]any{"severity": severity}
		}
	}
	return out
}

func newSessionFoldMemo() *sessionFoldMemo {
	return &sessionFoldMemo{
		Version:               sessionFoldMemoVersion,
		RunStatus:             "ready",
		OpenItems:             map[string]*projectionItem{},
		EvictedItems:          map[string]bool{},
		TaskProviderIDs:       map[string]bool{},
		TurnUsages:            map[string]turnUsageProjection{},
		TurnTerminals:         map[string]turnTerminalProjection{},
		BackgroundWakeTurns:   map[string]bool{},
		BackgroundWakeTaskIDs: map[string]string{},
		WakeParents:           map[string]string{},
		ContinuationTurns:     map[string]bool{},
		Turns:                 map[string]*turnFoldEntries{},
		TouchedTurns:          map[string]bool{},
	}
}

// foldEventOutcome is the per-event verdict. Anything but foldApplied or
// foldNoop means the batch must take the reference (re-projection) path.
type foldEventOutcome int

const (
	foldApplied foldEventOutcome = iota
	foldNoop
	foldNeedsResync
)

// foldEvent applies one just-persisted event to the memo. It returns the turn
// whose shell row changed (the RENDER turn — a wake-turn event reports its
// originating parent) or needsResync when the event is outside the fold's
// confident envelope: turn lifecycle boundaries, terminals, questions/answers,
// messages, compaction, session status, new/exiting background tasks, wake
// boundaries, revisions of evicted items — every structure-class event. The
// flood classes (item.*, turn.usage, shell_task.updated on a known running
// task) are the fold's whole purpose: they were >90% of the #1051 incident
// session's ledger.
func (m *sessionFoldMemo) foldEvent(event map[string]any) (string, foldEventOutcome) {
	if m == nil || event == nil {
		return "", foldNeedsResync
	}
	eventType := transcriptString(event, "type")
	switch eventType {
	case "item.started", "item.completed", "item.failed":
		return m.foldItemEvent(event)
	case "turn.usage":
		return m.foldTurnUsageEvent(event)
	case "shell_task.updated":
		return m.foldShellTaskUpdate(event)
	default:
		return "", foldNeedsResync
	}
}

func (m *sessionFoldMemo) foldItemEvent(event map[string]any) (string, foldEventOutcome) {
	if projectionIsCodexUserMessageEcho(event) {
		return "", foldNoop
	}
	id := transcriptString(event, "timeline_id")
	turnID := transcriptString(event, "turn_id")
	if id == "" || turnID == "" {
		return "", foldNoop
	}
	if m.EvictedItems[id] {
		// A revision of an item the memo no longer holds: the merged item
		// state cannot be reconstructed without the ledger. Resync.
		return "", foldNeedsResync
	}
	// Replay the event through the real apply() on a scratch state seeded
	// with the item's prior revision, so merge semantics (payload union,
	// terminal-status preservation, timing fields) have one implementation.
	scratch := newProjectionState()
	if existing := m.OpenItems[id]; existing != nil {
		item := *existing
		scratch.items = []*projectionItem{&item}
		scratch.itemIndex[id] = 0
	}
	scratch.apply(event)
	idx, ok := scratch.itemIndex[id]
	if !ok || idx >= len(scratch.items) {
		return "", foldNoop
	}
	item := scratch.items[idx]
	if isTerminalProjectionItemStatus(item.Status) {
		delete(m.OpenItems, id)
		m.EvictedItems[id] = true
	} else {
		m.OpenItems[id] = item
	}
	if item.ProviderItemID != "" && m.TaskProviderIDs[item.ProviderItemID] {
		// Background-task echo items never become flat entries; the task's
		// own shell_task.* lifecycle renders them.
		return "", foldNoop
	}
	entry := projectProjectionItem(item)
	if entry == nil {
		return "", foldNoop
	}
	return m.routeEntry(entry, turnID)
}

func (m *sessionFoldMemo) foldTurnUsageEvent(event map[string]any) (string, foldEventOutcome) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" || transcriptPayloadValue(event, "usage") == nil {
		return "", foldNoop
	}
	scratch := newProjectionState()
	if existing, ok := m.TurnUsages[turnID]; ok {
		scratch.turnUsages[turnID] = existing
	}
	scratch.apply(event)
	usage, ok := scratch.turnUsages[turnID]
	if !ok {
		return "", foldNoop
	}
	m.TurnUsages[turnID] = usage
	return m.routeEntry(projectTurnUsage(usage), turnID)
}

func (m *sessionFoldMemo) foldShellTaskUpdate(event map[string]any) (string, foldEventOutcome) {
	id := transcriptString(event, "timeline_id")
	turnID := transcriptString(event, "turn_id")
	if id == "" || turnID == "" {
		return "", foldNoop
	}
	existingIdx := -1
	for i, task := range m.Tasks {
		if task != nil && task.ID == id {
			existingIdx = i
			break
		}
	}
	if existingIdx < 0 {
		// First sight of a task on an update (its started event predates the
		// memo, or producers disagree on ids): structure change, resync.
		return "", foldNeedsResync
	}
	scratch := newProjectionState()
	seeded := *m.Tasks[existingIdx]
	scratch.backgroundTasks = []*projectionBackgroundTask{&seeded}
	scratch.backgroundIndex[id] = 0
	scratch.apply(event)
	idx, ok := scratch.backgroundIndex[id]
	if !ok || idx >= len(scratch.backgroundTasks) {
		return "", foldNoop
	}
	task := scratch.backgroundTasks[idx]
	if isTerminalProjectionBackgroundStatus(task.Status) {
		// A task reaching a terminal through an update changes parked /
		// continuation structure — the batch pipeline owns that transition.
		return "", foldNeedsResync
	}
	m.Tasks[existingIdx] = task
	return m.routeEntry(projectProjectionBackgroundTask(task), task.TurnID)
}

// routeEntry annotates and upserts the pruned entry under its ORIGINAL turn —
// wake content stays in the wake turn's set, exactly as the batch pipeline
// keeps per-turn bodies separate and merges them at shell-build time. The
// returned id is the RENDER turn (the wake's originating parent) so callers
// know whose shell to rebuild.
func (m *sessionFoldMemo) routeEntry(entry map[string]any, turnID string) (string, foldEventOutcome) {
	if entry == nil {
		return "", foldNoop
	}
	entry = annotateProjectionTerminal(entry, m.TurnTerminals)
	render := turnID
	if isBackgroundWakeTurnID(turnID) || m.BackgroundWakeTurns[turnID] {
		parent := m.WakeParents[turnID]
		if parent == "" {
			// Wake turn with no established parent edge: the batch pipeline
			// owns parent resolution.
			return "", foldNeedsResync
		}
		render = parent
	}
	turn := m.Turns[turnID]
	if turn == nil {
		turn = &turnFoldEntries{Entries: map[string]map[string]any{}}
		m.Turns[turnID] = turn
	}
	turn.upsert(pruneFoldEntry(entry))
	m.TouchedTurns[turnID] = true
	return render, foldApplied
}

// upsert keeps Order sorted by orderKey (ties keep arrival order), mirroring
// the batch pipeline's orderKey-sorted flat list and the orderKey-sorted
// wake-body merge. A revision that changes the entry's orderKey — an item
// completing re-keys it to the completion event's position — re-sorts the
// entry, exactly as the batch pipeline's full re-sort would.
func (t *turnFoldEntries) upsert(entry map[string]any) {
	id := transcriptMapString(entry, "id")
	if id == "" {
		return
	}
	key := transcriptMapString(entry, "orderKey")
	if prior, seen := t.Entries[id]; seen {
		if transcriptMapString(prior, "orderKey") == key {
			t.Entries[id] = entry
			return
		}
		for i, existingID := range t.Order {
			if existingID == id {
				t.Order = append(t.Order[:i], t.Order[i+1:]...)
				break
			}
		}
	}
	t.Entries[id] = entry
	pos := len(t.Order)
	for i, existingID := range t.Order {
		if transcriptMapString(t.Entries[existingID], "orderKey") > key {
			pos = i
			break
		}
	}
	t.Order = append(t.Order, "")
	copy(t.Order[pos+1:], t.Order[pos:])
	t.Order[pos] = id
}

func (t *turnFoldEntries) ordered() []map[string]any {
	out := make([]map[string]any, 0, len(t.Order))
	for _, id := range t.Order {
		if entry := t.Entries[id]; entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

// bodyForTurn derives one turn's activity body in its own mode, exactly as
// the batch pipeline does before any wake merging.
func (m *sessionFoldMemo) bodyForTurn(turnID string) (turnActivityBody, bool) {
	turn := m.Turns[turnID]
	if turn == nil {
		return turnActivityBody{}, false
	}
	fold := newTurnShellFold(turnID)
	for _, entry := range turn.ordered() {
		fold.upsertEntry(entry)
	}
	if terminal, hasTerminal := m.TurnTerminals[turnID]; hasTerminal {
		return fold.finishTerminal(terminal, m.BackgroundWakeTurns[turnID], m.ContinuationTurns[turnID])
	}
	if body, ok := fold.finishAwaitingInputHandoff(); ok {
		return body, true
	}
	if turnID == m.ActiveTurnID {
		return fold.finishActive(m.RunStatus)
	}
	return turnActivityBody{}, false
}

// shellRowForTurn rebuilds the durable turn_activity row for one RENDER turn
// from the memo: per-member bodies (the turn plus every wake turn parented to
// it) are derived in their own modes and merged through the same
// mergeBackgroundWakeActivityBodies the batch pipeline uses, then the row is
// built by the shared builder. ok=false with foldOK=true means the turn
// currently emits no shell (the emission gate) — the fold never deletes a
// previously-emitted shell, because fold-class events only add content.
// foldOK=false means the merged result includes an entry that escapes
// compaction (a promoted final answer): such rows carry full content the
// pruned memo deliberately does not hold, so the event class must resync.
func (m *sessionFoldMemo) shellRowForTurn(turnID string) (row map[string]any, ok bool, foldOK bool) {
	if m.BackgroundWakeTurns[turnID] && m.WakeParents[turnID] != "" {
		// Folded wake turns never surface their own shell.
		return nil, false, true
	}
	bodies := map[string]turnActivityBody{}
	memberParents := map[string]string{}
	if body, has := m.bodyForTurn(turnID); has {
		bodies[turnID] = body
	}
	for wakeID, parent := range m.WakeParents {
		if parent != turnID {
			continue
		}
		if body, has := m.bodyForTurn(wakeID); has {
			bodies[wakeID] = body
			memberParents[wakeID] = parent
		}
	}
	if len(bodies) == 0 {
		return nil, false, true
	}
	merged := mergeBackgroundWakeActivityBodies(bodies, memberParents)
	body, has := merged[turnID]
	if !has {
		return nil, false, true
	}
	// Every merged entry must be inside the compacted set (shell content) —
	// an escaping entry is a promoted row whose full content the pruned memo
	// does not hold.
	compacted := map[string]bool{}
	for _, id := range body.CompactedEntryIDs {
		compacted[id] = true
	}
	for _, entry := range body.Entries {
		id := transcriptMapString(entry, "id")
		if id == "" || compacted[id] || isProjectionAwaitingInputEntry(entry) {
			continue
		}
		return nil, false, false
	}
	parentBody := bodies[turnID]
	parentEntries := parentBody.Entries
	// Emission gate, mirroring compactProjectedTranscript: the gate runs on
	// the parent's OWN body, exactly as the batch pipeline gates shells
	// before the wake fold rewrites them. The turn-progress probe reads the
	// memo's full entry set — progress chips are anchors, not body entries.
	var progressProbe []map[string]any
	if turn := m.Turns[turnID]; turn != nil {
		progressProbe = turn.ordered()
	}
	active := parentBody.Summary["active"] == true
	activeProgressOnly := active && len(parentBody.CompactedEntryIDs) == 0 && foldEntriesHaveTurnProgress(progressProbe)
	activeNeedsInput := active && parentBody.Status == "needs_input"
	handoff := parentBody.Summary["awaitingInputHandoff"] == true
	if len(parentBody.CompactedEntryIDs) == 0 && !activeProgressOnly && !activeNeedsInput && !handoff {
		return nil, false, true
	}
	if m.ContinuationTurns[turnID] {
		parentBody.Summary["continuation"] = true
	}
	// Build the parent-only row first (anchoring, top-level usage fields),
	// then let the SAME refreshFoldedParentShells the batch pipeline uses
	// rewrite it with the merged body — preserved fields and all.
	shellRow := buildTurnActivityShellRow(parentBody, parentEntries)
	if len(memberParents) > 0 {
		shellRow = refreshFoldedParentShells([]map[string]any{shellRow}, merged, memberParents)[0]
	}
	return shellRow, true, true
}

func foldEntriesHaveTurnProgress(entries []map[string]any) bool {
	for _, entry := range entries {
		if isProjectionTurnProgress(entry) {
			return true
		}
	}
	return false
}

// buildSessionFoldMemo seeds the memo from a full ledger replay — the resync
// half of the contract. It runs the same apply loop and flat projection the
// batch pipeline runs, then snapshots the bounded context and routes pruned
// entries to their render turns exactly as foldEvent does.
func buildSessionFoldMemo(events []map[string]any) *sessionFoldMemo {
	memo := newSessionFoldMemo()
	state := newProjectionState()
	ordered := orderedTranscriptEvents(events)
	for _, event := range ordered {
		state.apply(event)
	}
	state.continuationTurns = state.backgroundTaskContinuationTurns()
	state.backgroundWakeParents = state.backgroundTaskWakeParentTurns()

	memo.RunStatus = state.runStatus
	memo.ActiveTurnID = state.activeTurnID
	for id, idx := range state.itemIndex {
		if idx >= len(state.items) || state.items[idx] == nil {
			continue
		}
		item := *state.items[idx]
		if isTerminalProjectionItemStatus(item.Status) {
			memo.EvictedItems[id] = true
		} else {
			memo.OpenItems[id] = &item
		}
	}
	for _, task := range state.backgroundTasks {
		if task == nil {
			continue
		}
		copied := *task
		memo.Tasks = append(memo.Tasks, &copied)
	}
	for providerID := range state.backgroundProviderItemIDs() {
		memo.TaskProviderIDs[providerID] = true
	}
	for turnID, usage := range state.turnUsages {
		memo.TurnUsages[turnID] = usage
	}
	for turnID, terminal := range state.turnTerminals {
		memo.TurnTerminals[turnID] = terminal
	}
	for turnID := range state.backgroundWakeTurns {
		memo.BackgroundWakeTurns[turnID] = true
	}
	for turnID, taskID := range state.backgroundWakeTaskIDs {
		memo.BackgroundWakeTaskIDs[turnID] = taskID
	}
	for wakeID, parent := range state.backgroundWakeParents {
		memo.WakeParents[wakeID] = parent
	}
	for turnID := range state.continuationTurns {
		memo.ContinuationTurns[turnID] = true
	}

	flat := state.projectFlatEntries()
	assignSessionStatusOwnership(flat)
	flat = dropOrphanSessionLifecycle(flat)
	for _, entry := range flat {
		turnID := transcriptMapString(entry, "turnId")
		if turnID == "" {
			continue
		}
		turn := memo.Turns[turnID]
		if turn == nil {
			turn = &turnFoldEntries{Entries: map[string]map[string]any{}}
			memo.Turns[turnID] = turn
		}
		turn.upsert(pruneFoldEntry(entry))
	}
	if len(ordered) > 0 {
		memo.LastOrderKey = transcriptString(ordered[len(ordered)-1], "order_key")
	}
	return memo
}

// marshalSessionFoldMemo serializes the session part plus the given turn
// sets (touched-only for fold saves; all for reseeds). fits=false when any
// single part exceeds the cap.
func marshalSessionFoldMemo(memo *sessionFoldMemo, turnIDs []string) ([]byte, map[string][]byte, bool) {
	if memo == nil {
		return nil, nil, false
	}
	raw, err := json.Marshal(memo)
	if err != nil || len(raw) > sessionFoldMemoMaxBytes {
		return nil, nil, false
	}
	turns := map[string][]byte{}
	for _, turnID := range turnIDs {
		set := memo.Turns[turnID]
		if set == nil {
			continue
		}
		blob, err := json.Marshal(set)
		if err != nil || len(blob) > sessionFoldMemoMaxBytes {
			return nil, nil, false
		}
		turns[turnID] = blob
	}
	return raw, turns, true
}

func unmarshalSessionFoldMemo(raw []byte) *sessionFoldMemo {
	if len(raw) == 0 {
		return nil
	}
	var memo sessionFoldMemo
	if err := json.Unmarshal(raw, &memo); err != nil {
		return nil
	}
	if memo.Version != sessionFoldMemoVersion {
		return nil
	}
	// Maps elided by omitempty must come back usable.
	if memo.OpenItems == nil {
		memo.OpenItems = map[string]*projectionItem{}
	}
	if memo.EvictedItems == nil {
		memo.EvictedItems = map[string]bool{}
	}
	if memo.TaskProviderIDs == nil {
		memo.TaskProviderIDs = map[string]bool{}
	}
	if memo.TurnUsages == nil {
		memo.TurnUsages = map[string]turnUsageProjection{}
	}
	if memo.TurnTerminals == nil {
		memo.TurnTerminals = map[string]turnTerminalProjection{}
	}
	if memo.BackgroundWakeTurns == nil {
		memo.BackgroundWakeTurns = map[string]bool{}
	}
	if memo.BackgroundWakeTaskIDs == nil {
		memo.BackgroundWakeTaskIDs = map[string]string{}
	}
	if memo.WakeParents == nil {
		memo.WakeParents = map[string]string{}
	}
	if memo.ContinuationTurns == nil {
		memo.ContinuationTurns = map[string]bool{}
	}
	memo.Turns = map[string]*turnFoldEntries{}
	memo.TouchedTurns = map[string]bool{}
	return &memo
}

// attachFoldTurnBlobs deserializes per-turn entry sets into a loaded memo.
// A blob that fails to decode poisons the memo (returns false) — the caller
// falls back to the reference path, which reseeds.
func attachFoldTurnBlobs(memo *sessionFoldMemo, blobs map[string][]byte) bool {
	for turnID, blob := range blobs {
		var set turnFoldEntries
		if err := json.Unmarshal(blob, &set); err != nil {
			return false
		}
		if set.Entries == nil {
			set.Entries = map[string]map[string]any{}
		}
		memo.Turns[turnID] = &set
	}
	return true
}

// foldTurnIDsToLoad computes the turn entry sets a batch needs: the batch's
// own turns, their render parents, and — for any parent shell rebuild — every
// wake child of those parents (the family closure mergeBackgroundWake needs).
func (m *sessionFoldMemo) foldTurnIDsToLoad(events []map[string]any) []string {
	need := map[string]bool{}
	for _, event := range events {
		turnID := transcriptString(event, "turn_id")
		if turnID == "" {
			continue
		}
		need[turnID] = true
		if parent := m.WakeParents[turnID]; parent != "" {
			need[parent] = true
		}
	}
	// Family closure: children of every needed parent.
	for wakeID, parent := range m.WakeParents {
		if need[parent] {
			need[wakeID] = true
		}
	}
	out := make([]string, 0, len(need))
	for turnID := range need {
		out = append(out, turnID)
	}
	sort.Strings(out)
	return out
}

// foldBatch applies a sorted batch of events to the memo. It returns the set
// of changed render turns when every event folded; any resync-class event
// aborts the whole batch to the reference path (one session re-projection
// covers everything, matching the pre-fold coalescing behavior).
func (m *sessionFoldMemo) foldBatch(events []map[string]any) (map[string]bool, bool) {
	changed := map[string]bool{}
	for _, event := range orderedTranscriptEvents(events) {
		orderKey := transcriptString(event, "order_key")
		if orderKey != "" && m.LastOrderKey != "" && orderKey <= m.LastOrderKey {
			// Already folded (reconciler replay, duplicate delivery): the
			// upsert-by-id semantics make re-folding idempotent, but skipping
			// avoids resurrecting evicted items as resyncs.
			continue
		}
		turnID, outcome := m.foldEvent(event)
		switch outcome {
		case foldNeedsResync:
			return nil, false
		case foldApplied:
			changed[turnID] = true
		}
		if orderKey > m.LastOrderKey {
			m.LastOrderKey = orderKey
		}
	}
	return changed, true
}

// sortedFoldTurnIDs gives deterministic write order for changed shells.
func sortedFoldTurnIDs(changed map[string]bool) []string {
	out := make([]string, 0, len(changed))
	for turnID := range changed {
		if strings.TrimSpace(turnID) != "" {
			out = append(out, turnID)
		}
	}
	sort.Strings(out)
	return out
}
