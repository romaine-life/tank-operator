package main

import (
	"context"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// Turn-activity pagination.
//
// A single agent turn can accumulate thousands of session_events (a long
// implementation turn, especially one that crosses a context-compaction
// boundary). The transcript read model must never (a) drop the turn's terminal
// event, which makes a finished turn look perpetually active, nor (b) re-read
// and re-project the entire turn on every event. The previous design did both:
// it folded only the first `turnActivityEventLimit` events of a turn, ordered
// oldest-first, so the terminal (always the last event of a long turn) fell
// outside the window and the shell stayed `active` forever.
//
// The page model fixes the class:
//
//   - The turn is split into pages. A page seals when it reaches
//     `turnPageEventLimit` events. A sealed page is immutable; only the live
//     last page changes as events arrive.
//   - The turn-activity *shell* (active / terminal status, counts, completedAt)
//     is derived from a complete fold over the whole turn, so the terminal can
//     never be a casualty of a body window.
//   - Each page exposes its own body (the rendered tool/message rows for that
//     page's event range). The Turns view renders one page at a time and
//     defaults to the last page.

// turnPageEventLimit is the maximum number of raw session_events (counted by
// order_key) a single page holds before it seals and a new page begins. It is
// also the boundary that previously truncated the whole turn; here it bounds
// only a page body, never lifecycle truth.
const turnPageEventLimit = 1000

// turnPageReadBatch is the page size used to read a turn's events to
// exhaustion. Independent of turnPageEventLimit: it only bounds one round-trip.
const turnPageReadBatch = 1000

// readAllTurnEvents reads every event of a turn in ASC order by paging the
// turn-scoped cursor to exhaustion. This is the read that pagination is built
// on: it never truncates the turn's terminal the way a single bounded
// fixed-size per-turn read does. Used on the (rare) per-turn detail read, not
// the per-event materialization hot path.
func readAllTurnEvents(ctx context.Context, eventStore store.SessionEventStore, sessionID, turnID string) ([]map[string]any, error) {
	var all []map[string]any
	cursor := ""
	for {
		page, err := eventStore.EventsForTurnAfter(ctx, sessionID, turnID, cursor, turnPageReadBatch)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Events...)
		if page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	return adoptLeadingSessionLifecycle(ctx, eventStore, sessionID, all)
}

// readAllSessionEvents reads every event of a session in ASC order by paging the
// session cursor to exhaustion. Used by the rare deep-link path that must
// project the whole session — folding a historical background-wake turn number
// to its originating real turn — not the per-event materialization hot path.
func readAllSessionEvents(ctx context.Context, eventStore store.SessionEventStore, sessionID string) ([]map[string]any, error) {
	var all []map[string]any
	cursor := ""
	for {
		page, err := eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{AfterOrderKey: cursor}, turnPageReadBatch)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Events...)
		if page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	return all, nil
}

// readUserFacingTurnEvents returns the event set for the turn as the user
// experiences it. A background-task wake is a backend continuation turn, but it
// belongs in the originating turn's activity detail.
func readUserFacingTurnEvents(ctx context.Context, eventStore store.SessionEventStore, sessionID, turnID string) ([]map[string]any, error) {
	events, _, _, err := readUserFacingTurnEventsWithChain(ctx, eventStore, sessionID, turnID)
	return events, err
}

// readUserFacingTurnEventsWithChain additionally returns the candidate turn
// ids (origin + every derivable wake-chain id, in discovery order) and the
// max order_key observed per candidate — the turn-activity cache's
// freshness inputs (issue #1077 item 1). The observed max equals the DB max
// at read time because every candidate is read to exhaustion; candidates
// with no events yet map to "".
func readUserFacingTurnEventsWithChain(ctx context.Context, eventStore store.SessionEventStore, sessionID, turnID string) ([]map[string]any, []string, map[string]string, error) {
	observed := map[string]string{}
	noteObserved := func(id string, events []map[string]any) {
		max := observed[id]
		for _, event := range events {
			if key, _ := event["order_key"].(string); key > max {
				max = key
			}
		}
		observed[id] = max
	}
	events, err := readAllTurnEvents(ctx, eventStore, sessionID, turnID)
	if err != nil {
		return nil, nil, nil, err
	}
	candidates := []string{turnID}
	noteObserved(turnID, events)
	if askingTurnID := askUserQuestionAskingTurnID(turnID, events); askingTurnID != "" {
		askingEvents, err := readAllTurnEvents(ctx, eventStore, sessionID, askingTurnID)
		if err != nil {
			return nil, nil, nil, err
		}
		candidates = append(candidates, askingTurnID)
		noteObserved(askingTurnID, askingEvents)
		events = append(events, askingTurnFinalAnswerContextEvents(turnID, events, askingEvents)...)
		// Carry the asking turn's triggering user prompt onto the question turn so
		// Q2+ pages can render it under a "continued from previous turn" header,
		// keeping the question fused to the context that produced it.
		events = append(events, askingTurnPromptContextEvents(turnID, askingEvents)...)
	}
	// Transitively pull the entire background-wake continuation chain rooted at
	// turnID. A wake turn can itself launch a background task whose terminal
	// wakes a further continuation turn (wake-of-a-wake); the whole chain folds
	// into this turn's activity, so the /activity body must read every link, not
	// only the direct children. seen also bounds the read against any cycle.
	seen := map[string]bool{turnID: true}
	frontier := backgroundWakeTurnIDsForParentEvents(events, turnID)
	for len(frontier) > 0 {
		wakeTurnID := frontier[0]
		frontier = frontier[1:]
		if seen[wakeTurnID] {
			continue
		}
		seen[wakeTurnID] = true
		candidates = append(candidates, wakeTurnID)
		wakeEvents, err := readAllTurnEvents(ctx, eventStore, sessionID, wakeTurnID)
		if err != nil {
			return nil, nil, nil, err
		}
		noteObserved(wakeTurnID, wakeEvents)
		events = append(events, wakeEvents...)
		frontier = append(frontier, backgroundWakeTurnIDsForParentEvents(wakeEvents, wakeTurnID)...)
	}
	return orderedTranscriptEvents(events), candidates, observed, nil
}

const questionFinalAnswerContextForTurnField = "_tank_question_final_answer_context_for_turn"

func askUserQuestionAskingTurnFinalAnswer(questionTurnID string, events []map[string]any) (string, map[string]bool) {
	if questionTurnID == "" {
		return "", nil
	}
	for _, event := range orderedTranscriptEvents(events) {
		if !isQuestionOnlyAwaitingInputEvent(event) {
			continue
		}
		payload := transcriptPayload(event)
		if transcriptMapString(payload, "question_turn_id") != questionTurnID {
			continue
		}
		askingTurnID := transcriptMapString(payload, "asking_turn_id")
		finalAnswerIDs := finalAnswerTimelineIDsFromPayload(payload, "asking_turn_final_answer")
		if askingTurnID != "" && askingTurnID != questionTurnID && len(finalAnswerIDs) > 0 {
			return askingTurnID, finalAnswerIDs
		}
	}
	return "", nil
}

// askUserQuestionAskingTurnID resolves the turn that asked the question for a
// question-only turn, independent of whether a durable final-answer snapshot was
// captured. askUserQuestionAskingTurnFinalAnswer requires final-answer timeline
// ids (it drives the answer-candidate copy); the prompt-context copy only needs
// the asking turn id, so it has its own resolver.
func askUserQuestionAskingTurnID(questionTurnID string, events []map[string]any) string {
	if questionTurnID == "" {
		return ""
	}
	for _, event := range orderedTranscriptEvents(events) {
		if !isQuestionOnlyAwaitingInputEvent(event) {
			continue
		}
		payload := transcriptPayload(event)
		if transcriptMapString(payload, "question_turn_id") != questionTurnID {
			continue
		}
		askingTurnID := transcriptMapString(payload, "asking_turn_id")
		if askingTurnID != "" && askingTurnID != questionTurnID {
			return askingTurnID
		}
	}
	return ""
}

func finalAnswerTimelineIDsFromPayload(payload map[string]any, field string) map[string]bool {
	raw, _ := payload[field].(map[string]any)
	rawIDs, _ := raw["timeline_ids"].([]any)
	if len(rawIDs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, rawID := range rawIDs {
		id, _ := rawID.(string)
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func askingTurnFinalAnswerContextEvents(questionTurnID string, questionEvents, askingEvents []map[string]any) []map[string]any {
	if questionTurnID == "" {
		return nil
	}
	_, finalAnswerIDs := askUserQuestionAskingTurnFinalAnswer(questionTurnID, questionEvents)
	if len(finalAnswerIDs) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(finalAnswerIDs))
	for _, event := range orderedTranscriptEvents(askingEvents) {
		if transcriptString(event, "type") != "item.completed" || transcriptString(event, "actor") != "assistant" {
			continue
		}
		if !finalAnswerIDs[transcriptString(event, "timeline_id")] {
			continue
		}
		context := cloneAnyMap(event)
		context[questionFinalAnswerContextForTurnField] = questionTurnID
		out = append(out, context)
	}
	return out
}

func eventIsQuestionFinalAnswerContextForTurn(event map[string]any, turnID string) bool {
	return turnID != "" && transcriptString(event, questionFinalAnswerContextForTurnField) == turnID
}

// questionPromptContextForTurnField tags a clone of the asking turn's triggering
// user_message.created so it rides into the question turn's event window. Like
// the final-answer-context tag, ownTurnPageEvents strips it from the page bodies
// and projectQuestionPromptContextEntry surfaces it as the question turn's prompt
// context (under a "continued from previous turn" header on Q2+ pages).
const questionPromptContextForTurnField = "_tank_question_prompt_context_for_turn"

// askingTurnPromptContextEvents copies the asking turn's triggering user message
// into the question turn's window, tagged for question turnID. It returns at most
// one event (the first user_message.created of the asking turn). A background-wake
// asking turn has no user_message.created and yields nothing — the question page
// then falls back to the "Question N of M" heading.
func askingTurnPromptContextEvents(questionTurnID string, askingEvents []map[string]any) []map[string]any {
	if questionTurnID == "" {
		return nil
	}
	for _, event := range orderedTranscriptEvents(askingEvents) {
		if transcriptString(event, "type") != "user_message.created" {
			continue
		}
		context := cloneAnyMap(event)
		context[questionPromptContextForTurnField] = questionTurnID
		return []map[string]any{context}
	}
	return nil
}

func eventIsQuestionPromptContextForTurn(event map[string]any, turnID string) bool {
	return turnID != "" && transcriptString(event, questionPromptContextForTurnField) == turnID
}

func ownTurnPageEvents(events []map[string]any, turnID string) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if eventIsQuestionFinalAnswerContextForTurn(event, turnID) {
			continue
		}
		if eventIsQuestionPromptContextForTurn(event, turnID) {
			continue
		}
		out = append(out, event)
	}
	return out
}

// projectQuestionPromptContextEntry builds the question turn's prompt-context
// entry from the tagged copy of the asking turn's triggering user message. It
// runs against the FULL event set (before ownTurnPageEvents strips the tag) and
// marks the entry turnContextContinued so the Turns view labels it "Question
// prompt continued from previous turn".
func projectQuestionPromptContextEntry(turnID string, events []map[string]any) map[string]any {
	if strings.TrimSpace(turnID) == "" {
		return nil
	}
	for _, event := range orderedTranscriptEvents(events) {
		if !eventIsQuestionPromptContextForTurn(event, turnID) {
			continue
		}
		entry := projectUserMessageEvent(event)
		if entry == nil {
			return nil
		}
		entry = cloneAnyMap(entry)
		entry["id"] = transcriptMapString(entry, "id") + ":question_prompt_context"
		entry["turnContext"] = true
		entry["turnContextContinued"] = true
		return entry
	}
	return nil
}

func projectQuestionFinalAnswerContextEntries(turnID string, events []map[string]any) []map[string]any {
	var out []map[string]any
	for _, event := range orderedTranscriptEvents(events) {
		if !eventIsQuestionFinalAnswerContextForTurn(event, turnID) {
			continue
		}
		entry := projectQuestionFinalAnswerContextEvent(event)
		if entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

func projectQuestionFinalAnswerContextEvent(event map[string]any) map[string]any {
	item := &projectionItem{
		ID:             transcriptString(event, "timeline_id"),
		TurnID:         transcriptString(event, "turn_id"),
		ParentID:       transcriptString(event, "parent_id"),
		ProviderItemID: transcriptString(event, "provider_item_id"),
		Actor:          transcriptString(event, "actor"),
		Kind:           projectionFirstNonEmpty(transcriptPayloadString(event, "kind"), defaultProjectionItemKind(event)),
		Status:         "completed",
		Title:          transcriptPayloadString(event, "title"),
		Text:           transcriptPayloadString(event, "text"),
		Payload:        transcriptPayload(event),
		OrderKey:       transcriptString(event, "order_key"),
		SourceEventID:  transcriptString(event, "event_id"),
		CreatedAt:      transcriptString(event, "created_at"),
		StartedAt:      transcriptString(event, "created_at"),
		CompletedAt:    transcriptString(event, "created_at"),
	}
	entry := projectProjectionItem(item)
	if entry == nil {
		return nil
	}
	entry = cloneAnyMap(entry)
	entry["id"] = transcriptMapString(entry, "id") + ":question_final_answer_context"
	entry["questionFinalAnswerContext"] = true
	entry["turnOnly"] = true
	return entry
}

func backgroundWakeTurnIDsForParentEvents(events []map[string]any, parentTurnID string) []string {
	if parentTurnID == "" {
		return nil
	}
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	if !state.backgroundTaskContinuationTurns()[parentTurnID] {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, task := range state.backgroundTasks {
		if task == nil || task.TurnID != parentTurnID || task.TaskID == "" {
			continue
		}
		wakeTurnID := conversation.TurnIDForClientNonce(pgstore.BackgroundTaskWakeClientNonce(task.TaskID))
		if wakeTurnID == "" {
			continue
		}
		if seen[wakeTurnID] {
			continue
		}
		seen[wakeTurnID] = true
		out = append(out, wakeTurnID)
	}
	return out
}

// adoptLeadingSessionLifecycle prepends the session-startup lifecycle
// (session.status loading/ready emitted before the first turn) onto the FIRST
// turn's event set, so projectTranscriptEvents folds it into that turn's activity
// body — the noise bin — instead of leaving it as standalone top-level rows.
//
// Only the first turn adopts: if any user_message.created precedes this turn, the
// leading window belongs to an earlier turn and the events are returned
// unchanged. session.status:failed is not adopted; it stays a promoted top-level
// banner. This is the single seam shared by the materializer (durable /timeline
// rows) and the lazy /activity body, so both fold the lifecycle identically.
func adoptLeadingSessionLifecycle(ctx context.Context, eventStore store.SessionEventStore, sessionID string, turnEvents []map[string]any) ([]map[string]any, error) {
	bound := firstEventOrderKey(turnEvents)
	if bound == "" {
		return turnEvents, nil
	}
	var lifecycle []map[string]any
	cursor := ""
	for {
		page, err := eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{AfterOrderKey: cursor}, turnPageReadBatch)
		if err != nil {
			return nil, err
		}
		adopt, stop, prior := scanLeadingLifecycle(page.Events, bound)
		if prior {
			return turnEvents, nil
		}
		lifecycle = append(lifecycle, adopt...)
		if stop || page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	if len(lifecycle) == 0 {
		return turnEvents, nil
	}
	return append(lifecycle, turnEvents...), nil
}

// scanLeadingLifecycle walks a page of session-ordered events that precede a
// turn's first event (`bound`). It collects session.status loading/ready
// (prior=false), stops once it reaches the bound (stop=true), and reports
// prior=true the moment it sees an earlier user_message.created — meaning a prior
// turn already owns this leading window, so nothing is adopted.
func scanLeadingLifecycle(events []map[string]any, bound string) (adopt []map[string]any, stop bool, prior bool) {
	for _, ev := range events {
		if transcriptString(ev, "order_key") >= bound {
			return adopt, true, false
		}
		switch transcriptString(ev, "type") {
		case "user_message.created":
			return nil, true, true
		case "session.status":
			if st := transcriptPayloadString(ev, "status"); st == "loading" || st == "ready" {
				adopt = append(adopt, ev)
			}
		}
	}
	return adopt, false, false
}

func firstEventOrderKey(events []map[string]any) string {
	best := ""
	for _, ev := range events {
		ok := transcriptString(ev, "order_key")
		if ok == "" {
			continue
		}
		if best == "" || ok < best {
			best = ok
		}
	}
	return best
}

// turnPage is one sealed-or-live page of a turn's activity body.
type turnPage struct {
	Number        int              `json:"number"`
	Kind          string           `json:"kind"`
	StartOrderKey string           `json:"startOrderKey"`
	EndOrderKey   string           `json:"endOrderKey"`
	EventCount    int              `json:"eventCount"`
	Sealed        bool             `json:"sealed"`
	Entries       []map[string]any `json:"entries"`
	QuestionCount int              `json:"questionCount,omitempty"`
	QuestionIndex int              `json:"questionIndex,omitempty"`
	QuestionSet   int              `json:"questionSet,omitempty"`
	Answered      bool             `json:"answered,omitempty"`
}

type turnEventPage struct {
	Kind          string
	Events        []map[string]any
	QuestionIndex int
	QuestionSet   int
}

// turnPagesProjection is the page-aware projection of a single turn: a
// terminal-correct shell summary plus the ordered page directory and bodies.
type turnPagesProjection struct {
	TurnID             string
	Shell              map[string]any
	TurnContext        map[string]any
	FinalAnswerEntries []map[string]any
	Collapse           map[string]any
	Pages              []turnPage
	TotalEventCount    int
}

// splitTurnEventsIntoPages folds a turn's events (any order) into ordered page
// slices, sealing a page once it reaches turnPageEventLimit events.
func splitTurnEventsIntoPages(events []map[string]any) [][]map[string]any {
	ordered := orderedTranscriptEvents(events)
	if len(ordered) == 0 {
		return nil
	}
	var pages [][]map[string]any
	var current []map[string]any
	for _, event := range ordered {
		if len(current) >= turnPageEventLimit {
			pages = append(pages, current)
			current = nil
		}
		current = append(current, event)
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}
	return pages
}

// splitTurnEventsIntoSemanticPages adds one semantic boundary on top of the
// size threshold: each turn.awaiting_input pause owns one page per question.
// The asking turn gets a separate turn.awaiting_input.invocation event; the
// synthetic question-only turn must not manufacture a tool/activity page before
// its question pages.
func splitTurnEventsIntoSemanticPages(events []map[string]any) []turnEventPage {
	ordered := orderedTranscriptEvents(events)
	if len(ordered) == 0 {
		return nil
	}
	pages := make([]turnEventPage, 0, len(ordered)/turnPageEventLimit+1)
	currentKind := "activity"
	var current []map[string]any
	var pendingQuestionPages []turnEventPage
	pendingQuestionTimelineID := ""
	pendingQuestionTurnID := ""
	questionSet := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		pages = append(pages, turnEventPage{Kind: currentKind, Events: current})
		current = nil
		currentKind = "activity"
	}

	flushPendingQuestionPages := func(answer map[string]any) {
		if len(pendingQuestionPages) == 0 {
			return
		}
		for _, page := range pendingQuestionPages {
			if answer != nil {
				page.Events = append(page.Events, answer)
			}
			pages = append(pages, page)
		}
		pendingQuestionPages = nil
		pendingQuestionTimelineID = ""
		pendingQuestionTurnID = ""
	}

	for _, event := range ordered {
		if len(pendingQuestionPages) > 0 {
			if isTurnInputAnsweredForQuestion(event, pendingQuestionTimelineID) {
				flushPendingQuestionPages(event)
				continue
			}
			// A non-answer terminal on the question turn dismisses the
			// pending question set (Stop, supersession, sweep — issue
			// #1077 item 4): absorb it into the question pages exactly
			// like an answer, so the page fold renders the cards
			// dismissed instead of leaving them answerable forever. The
			// turn shell still folds the terminal from the full event
			// set.
			if isQuestionDismissalTerminal(event, pendingQuestionTurnID) {
				flushPendingQuestionPages(event)
				continue
			}
			// Stop drives a question turn to its dismissing turn.interrupted via a
			// preceding turn.interrupt_requested on the same turn. That pre-terminal
			// marker is not itself a dismissal terminal, so without this guard it
			// breaks the pending-question fold below and spills the Stop sequence
			// into a spurious trailing activity page — which then becomes the
			// dismissed turn's default page and strands the Turns prompt slot on
			// "Prompt context unavailable" (#1312). Hold the fold open across it so
			// the dismissal terminal still seals the question page; the turn shell
			// folds the marker from the complete event set regardless.
			if isQuestionInterruptRequested(event, pendingQuestionTurnID) {
				continue
			}
			flushPendingQuestionPages(nil)
		}
		if isTurnAwaitingInputEvent(event) {
			if isQuestionOnlyAwaitingInputEvent(event) {
				if turnPageEventsHaveBody(current) {
					flush()
				} else {
					current = nil
					currentKind = "activity"
				}
			} else {
				current = append(current, awaitingInputInvocationEvent(event))
				flush()
			}
			questionSet += 1
			pendingQuestionPages = awaitingInputQuestionPages(event, questionSet)
			pendingQuestionTimelineID = awaitingInputTimelineID(event)
			pendingQuestionTurnID = transcriptString(event, "turn_id")
			continue
		}
		current = append(current, event)
		if len(current) >= turnPageEventLimit {
			flush()
		}
	}
	flush()
	flushPendingQuestionPages(nil)
	return pages
}

func isTurnAwaitingInputEvent(event map[string]any) bool {
	return transcriptString(event, "type") == "turn.awaiting_input"
}

func isQuestionOnlyAwaitingInputEvent(event map[string]any) bool {
	if !isTurnAwaitingInputEvent(event) {
		return false
	}
	payload := transcriptPayload(event)
	turnID := transcriptString(event, "turn_id")
	questionTurnID := transcriptMapString(payload, "question_turn_id")
	askingTurnID := transcriptMapString(payload, "asking_turn_id")
	return turnID != "" &&
		questionTurnID == turnID &&
		askingTurnID != "" &&
		askingTurnID != turnID
}

func turnPageEventsHaveBody(events []map[string]any) bool {
	for _, event := range events {
		switch transcriptString(event, "type") {
		case "turn.submitted", "turn.claimed", "turn.started", "user_message.created":
			continue
		default:
			return true
		}
	}
	return false
}

func awaitingInputInvocationEvent(event map[string]any) map[string]any {
	out := cloneAnyMap(event)
	out["type"] = "turn.awaiting_input.invocation"
	return out
}

func awaitingInputQuestionPages(event map[string]any, questionSet int) []turnEventPage {
	questions := projectionAwaitingInputQuestions(event)
	if len(questions) == 0 {
		return []turnEventPage{{Kind: "question", Events: []map[string]any{event}, QuestionSet: questionSet}}
	}
	pages := make([]turnEventPage, 0, len(questions))
	for idx := range questions {
		pages = append(pages, turnEventPage{
			Kind:          "question",
			Events:        []map[string]any{awaitingInputQuestionPageEvent(event, idx, len(questions), questionSet)},
			QuestionIndex: idx + 1,
			QuestionSet:   questionSet,
		})
	}
	return pages
}

func awaitingInputQuestionPageEvent(event map[string]any, index, count, questionSet int) map[string]any {
	out := cloneAnyMap(event)
	payload := cloneAnyMap(transcriptPayload(event))
	payload["question_index"] = index + 1
	payload["question_count"] = count
	payload["question_set"] = questionSet
	out["payload"] = payload
	return out
}

func awaitingInputTimelineID(event map[string]any) string {
	if timelineID := transcriptPayloadString(event, "timeline_id"); timelineID != "" {
		return timelineID
	}
	return transcriptString(event, "timeline_id")
}

// isQuestionDismissalTerminal reports whether event is a non-answer
// terminal on the pending question turn — the dismissal class the
// projection's noteQuestionDismissal renders.
func isQuestionDismissalTerminal(event map[string]any, questionTurnID string) bool {
	if questionTurnID == "" || transcriptString(event, "turn_id") != questionTurnID {
		return false
	}
	switch transcriptString(event, "type") {
	case "turn.interrupted", "turn.failed", "turn.command_failed":
		return true
	}
	return false
}

// isQuestionInterruptRequested reports whether event is the turn.interrupt_requested
// that Stop emits on the question turn before the dismissing turn.interrupted. It is
// a pre-terminal control marker, not a dismissal terminal, so the page fold must
// hold the pending question pages open across it instead of spilling it (and the
// terminal that follows) into a spurious trailing activity page.
func isQuestionInterruptRequested(event map[string]any, questionTurnID string) bool {
	return questionTurnID != "" &&
		transcriptString(event, "turn_id") == questionTurnID &&
		transcriptString(event, "type") == "turn.interrupt_requested"
}

func isTurnInputAnsweredForQuestion(event map[string]any, questionTimelineID string) bool {
	if transcriptString(event, "type") != "turn.input_answered" {
		return false
	}
	if questionTimelineID == "" {
		return false
	}
	payload := transcriptAnyMap(event["payload"])
	return transcriptString(payload, "question_timeline_id") == questionTimelineID
}

// projectPageBodyEntries renders the body rows for one page's events. Unlike
// the turn shell it does not depend on lifecycle ownership, so a middle page
// (no turn.submitted, no terminal) still renders its tool/message rows.
// Human user-message and turn-progress rows are transcript-level, not page body,
// and are dropped. Background-wake prompts are system-user rows carried on
// turn.submitted for Turn activity only, so they remain page-body entries.
// The context.compacted marker is kept as the page's seam header.
func projectPageBodyEntries(events []map[string]any) []map[string]any {
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	flat := state.projectFlatEntries()
	out := make([]map[string]any, 0, len(flat))
	for _, entry := range flat {
		if (isProjectedUserMessage(entry) && !isProjectionWakePrompt(entry)) || isProjectionTurnProgress(entry) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// turnPageStatusIsLive reports whether a turn whose shell carries this status
// still has an open last page (no durable terminal yet).
func turnPageStatusIsLive(status string) bool {
	return status == "active" || status == "needs_input" || status == ""
}

// projectTurnPages builds the page-aware projection for a single turn. The
// shell summary comes from a complete fold over every event (terminal-correct),
// while page bodies come from each page's own event range.
func projectTurnPages(turnID string, events []map[string]any) turnPagesProjection {
	questionFinalAnswerContextEntries := projectQuestionFinalAnswerContextEntries(turnID, events)
	ownEvents := ownTurnPageEvents(events, turnID)
	pageSlices := splitTurnEventsIntoSemanticPages(ownEvents)
	wakeParents := backgroundWakeParentTurnsFromEvents(ownEvents)
	turnContext := projectTurnContextEntry(turnID, ownEvents)
	if turnContext == nil {
		// A question-only turn has no user_message.created of its own (the agent
		// started it by invoking AskUserQuestion). Surface the asking turn's
		// triggering prompt as the question turn's context so Q2+ pages render it
		// under the "continued from previous turn" header instead of only the
		// "Question N of M" heading.
		turnContext = projectQuestionPromptContextEntry(turnID, events)
	}
	finalAnswerIDs := finalAnswerIDsFromTurnEvents(ownEvents)

	// Terminal-correct shell from the COMPLETE event set: the full projection
	// folds the whole turn, so its activity summary always reflects the
	// terminal regardless of how many events the turn has.
	full := projectTranscriptEvents(ownEvents)
	status := ""
	shell := map[string]any{}
	activityBody := full.ActivityBodies[turnID]
	if activityBody.Summary != nil {
		shell = cloneAnyMap(activityBody.Summary)
		status = activityBody.Status
	}
	finalAnswerEntries := turnFinalAnswerEntries(activityBody.Entries, turnID, finalAnswerIDs, turnHasCompletedTerminal(ownEvents), turnCompletedTerminalCount(ownEvents) > 1)
	// An asking turn that paused on AskUserQuestion / ExitPlanMode never reaches
	// turn.completed (the answer rotates execution onto a separate continuation
	// turn), so the turn.completed.final_answer path above yields nothing and the
	// Turns view would render "No turn activity". Its hand-off IS its final
	// answer: the agent's preamble prose plus the AskUserQuestion card. The
	// question widget renders inline in the main transcript; there is no longer a
	// navigate-to-question-turn shortcut.
	if len(finalAnswerEntries) == 0 && activityBody.Summary["awaitingInputHandoff"] == true {
		finalAnswerEntries = awaitingInputHandoffFinalAnswerEntries(activityBody.Entries, turnID, askingTurnHandoffPreambleIDs(ownEvents, turnID))
	}
	finalAnswerIDs = mergeFinalAnswerEntryIDs(finalAnswerIDs, finalAnswerEntries)
	collapse := turnActivityCollapseSummary(activityBody, finalAnswerEntries, finalAnswerIDs)
	live := turnPageStatusIsLive(status)

	pages := make([]turnPage, 0, len(pageSlices))
	directory := make([]map[string]any, 0, len(pageSlices))
	for i, slice := range pageSlices {
		number := i + 1
		// Every page but the last is sealed; the last page is sealed too once
		// the turn has reached a durable terminal.
		sealed := number < len(pageSlices) || !live
		questionSet, questionIndex, questionCount, answered := turnPageQuestionSetState(slice)
		entries := reassignBackgroundWakeProjectedEntries(projectPageBodyEntries(slice.Events), wakeParents)
		if slice.Kind == "question" && len(questionFinalAnswerContextEntries) > 0 {
			entries = append(cloneProjectedEntries(questionFinalAnswerContextEntries), entries...)
		}
		page := turnPage{
			Number:        number,
			Kind:          slice.Kind,
			StartOrderKey: transcriptString(slice.Events[0], "order_key"),
			EndOrderKey:   transcriptString(slice.Events[len(slice.Events)-1], "order_key"),
			EventCount:    len(slice.Events),
			Sealed:        sealed,
			Entries:       entries,
			QuestionCount: questionCount,
			QuestionIndex: questionIndex,
			QuestionSet:   questionSet,
			Answered:      answered,
		}
		pages = append(pages, page)
		pageInfo := map[string]any{
			"number":        page.Number,
			"kind":          page.Kind,
			"startOrderKey": page.StartOrderKey,
			"endOrderKey":   page.EndOrderKey,
			"eventCount":    page.EventCount,
			"sealed":        page.Sealed,
		}
		if page.Kind == "question" {
			pageInfo["questionCount"] = page.QuestionCount
			pageInfo["questionIndex"] = page.QuestionIndex
			pageInfo["questionSet"] = page.QuestionSet
			pageInfo["answered"] = page.Answered
		}
		directory = append(directory, pageInfo)
	}

	shell["pageCount"] = len(pages)
	shell["totalEventCount"] = len(ownEvents)
	shell["pages"] = directory

	return turnPagesProjection{
		TurnID:             turnID,
		Shell:              shell,
		TurnContext:        turnContext,
		FinalAnswerEntries: finalAnswerEntries,
		Collapse:           collapse,
		Pages:              pages,
		TotalEventCount:    len(ownEvents),
	}
}

func cloneProjectedEntries(entries []map[string]any) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, cloneAnyMap(entry))
	}
	return out
}

// finalAnswerIDsFromTurnEvents resolves the turn-detail final answer for a
// turn's COMBINED event set (the origin turn plus its folded wake-continuation
// chain). The chain's LAST completed terminal owns the final answer: a parked
// origin turn's promoted ack ("I'll report when it completes") is not the
// user-final response — the continuation supersedes it. Unioning ids across
// every terminal rendered the superseded ack as the page's final answer,
// below (and visually replacing) the later wake content — the session-161
// "the fold replaced the turn's answer" defect. When the last completed link
// promoted nothing, the turn has no true final answer and the page shows the
// chronological body only.
func finalAnswerIDsFromTurnEvents(events []map[string]any) map[string]bool {
	var out map[string]bool
	for _, event := range orderedTranscriptEvents(events) {
		if transcriptString(event, "type") != string(conversation.EventTurnCompleted) {
			continue
		}
		out = map[string]bool{}
		for id := range projectionFinalAnswerIDs(event) {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func turnHasCompletedTerminal(events []map[string]any) bool {
	for _, event := range orderedTranscriptEvents(events) {
		if transcriptString(event, "type") == string(conversation.EventTurnCompleted) {
			return true
		}
	}
	return false
}

// turnCompletedTerminalCount counts completed terminals across the combined
// origin+continuation event set. More than one means this is a folded
// continuation chain.
func turnCompletedTerminalCount(events []map[string]any) int {
	count := 0
	for _, event := range events {
		if transcriptString(event, "type") == string(conversation.EventTurnCompleted) {
			count++
		}
	}
	return count
}

func turnFinalAnswerEntries(entries []map[string]any, turnID string, finalAnswerIDs map[string]bool, completed bool, chained bool) []map[string]any {
	var out []map[string]any
	var fallback map[string]any
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") != turnID {
			continue
		}
		if !isProjectedAssistantMessage(entry) {
			continue
		}
		// The no-marker fallback exists for single-terminal turns from ledgers
		// that predate explicit final-answer promotion. It must never apply to
		// a folded continuation chain: when the chain's last link promoted
		// nothing, resurrecting an earlier (superseded) ack as the "final
		// answer" is exactly the answer-replacement confusion this projection
		// exists to prevent.
		if len(finalAnswerIDs) == 0 && completed && !chained && !isProjectionAwaitingInputEntry(entry) {
			fallback = entry
			continue
		}
		if !finalAnswerIDs[transcriptMapString(entry, "id")] {
			continue
		}
		next := cloneAnyMap(entry)
		next["turnDetailRole"] = "final_answer"
		out = append(out, next)
	}
	if len(out) == 0 && fallback != nil {
		next := cloneAnyMap(fallback)
		next["turnDetailRole"] = "final_answer"
		out = append(out, next)
	}
	return out
}

// askingTurnHandoffPreambleIDs returns the timeline ids of the asking turn's
// preamble — the assistant prose the runner snapshotted as
// asking_turn_final_answer when it handed off to the question turn. The asking
// turn's own assistant_message.created hand-off event carries that snapshot in
// payload.awaiting_input.asking_turn_final_answer, using the same
// final_answer.timeline_ids shape turn.completed uses. Empty when the agent
// paused with no preceding prose.
func askingTurnHandoffPreambleIDs(events []map[string]any, turnID string) map[string]bool {
	if turnID == "" {
		return nil
	}
	for _, event := range orderedTranscriptEvents(events) {
		if transcriptString(event, "turn_id") != turnID {
			continue
		}
		if transcriptString(event, "type") != "assistant_message.created" {
			continue
		}
		awaiting := transcriptAnyMap(transcriptPayload(event)["awaiting_input"])
		if len(awaiting) == 0 {
			continue
		}
		if ids := finalAnswerTimelineIDsFromPayload(awaiting, "asking_turn_final_answer"); len(ids) > 0 {
			return ids
		}
	}
	return nil
}

// awaitingInputHandoffFinalAnswerEntries builds the final-answer bundle for an
// asking turn that paused on AskUserQuestion / ExitPlanMode. The hand-off is the
// turn's terminal Tank-visible response, so it plays the final-answer role: the
// agent's preamble prose (the asking_turn_final_answer items named by
// preambleIDs) followed by the AskUserQuestion card, whose awaitingInput carries
// the shortcut to the question turn. Entries are returned in display order
// (preamble, then card) and tagged turnDetailRole=final_answer like any other
// final answer. Returns nil when no hand-off card is present.
func awaitingInputHandoffFinalAnswerEntries(entries []map[string]any, turnID string, preambleIDs map[string]bool) []map[string]any {
	var preamble []map[string]any
	var card map[string]any
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") != turnID || !isProjectedAssistantMessage(entry) {
			continue
		}
		if awaiting, _ := entry["awaitingInput"].(map[string]any); len(awaiting) > 0 {
			questionTurnID := transcriptMapString(awaiting, "questionTurnId")
			askingTurnID := transcriptMapString(awaiting, "askingTurnId")
			if questionTurnID != "" && questionTurnID != turnID && (askingTurnID == "" || askingTurnID == turnID) {
				card = entry
			}
			continue
		}
		if preambleIDs[transcriptMapString(entry, "id")] {
			preamble = append(preamble, entry)
		}
	}
	if card == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(preamble)+1)
	for _, entry := range append(preamble, card) {
		next := cloneAnyMap(entry)
		next["turnDetailRole"] = "final_answer"
		out = append(out, next)
	}
	return out
}

func mergeFinalAnswerEntryIDs(finalAnswerIDs map[string]bool, entries []map[string]any) map[string]bool {
	if len(entries) == 0 {
		return finalAnswerIDs
	}
	if finalAnswerIDs == nil {
		finalAnswerIDs = map[string]bool{}
	}
	for _, entry := range entries {
		if id := transcriptMapString(entry, "id"); id != "" {
			finalAnswerIDs[id] = true
		}
	}
	return finalAnswerIDs
}

func turnActivityCollapseSummary(body turnActivityBody, finalAnswerEntries []map[string]any, finalAnswerIDs map[string]bool) map[string]any {
	finalCount := len(finalAnswerEntries)
	hiddenCount := 0
	for _, entry := range body.Entries {
		id := transcriptMapString(entry, "id")
		if finalAnswerIDs[id] || isProjectionWakePrompt(entry) {
			continue
		}
		hiddenCount++
	}
	collapsible := finalCount > 0 && hiddenCount > 0
	reason := "no_final_answer"
	if finalCount > 0 && hiddenCount == 0 {
		reason = "no_collapsible_activity"
	} else if collapsible {
		reason = "final_answer"
	}
	return map[string]any{
		"collapsible":        collapsible,
		"default_collapsed":  collapsible,
		"hidden_count":       hiddenCount,
		"final_answer_count": finalCount,
		"reason":             reason,
	}
}

func projectTurnContextEntry(turnID string, events []map[string]any) map[string]any {
	if strings.TrimSpace(turnID) == "" {
		return nil
	}
	for _, event := range orderedTranscriptEvents(events) {
		if transcriptString(event, "type") != "user_message.created" ||
			transcriptString(event, "turn_id") != turnID {
			continue
		}
		entry := projectUserMessageEvent(event)
		if entry == nil {
			return nil
		}
		entry = cloneAnyMap(entry)
		entry["turnContext"] = true
		return entry
	}
	for _, event := range orderedTranscriptEvents(events) {
		if transcriptString(event, "type") != "turn.submitted" ||
			transcriptString(event, "turn_id") != turnID ||
			transcriptPayloadString(event, "source") != "background-task" {
			continue
		}
		entry := projectSystemSubmittedTurnContextEvent(event)
		if entry == nil {
			return nil
		}
		return entry
	}
	return nil
}

func projectSystemSubmittedTurnContextEvent(event map[string]any) map[string]any {
	text := strings.TrimSpace(transcriptPayloadString(event, "prompt"))
	turnID := transcriptString(event, "turn_id")
	clientNonce := transcriptString(event, "client_nonce")
	eventID := transcriptString(event, "event_id")
	if text == "" || turnID == "" || clientNonce == "" || eventID == "" {
		return nil
	}
	return map[string]any{
		"id":                eventID + ":turn_context",
		"kind":              "message",
		"role":              "user",
		"authorKind":        "system",
		"text":              text,
		"turnId":            turnID,
		"clientNonce":       clientNonce,
		"time":              transcriptString(event, "created_at"),
		"sourceEventId":     eventID,
		"orderKey":          transcriptString(event, "order_key"),
		"turnContext":       true,
		"turnContextSource": transcriptPayloadString(event, "source"),
		"display":           map[string]any{"kind": "plain"},
	}
}

func defaultTurnActivityPageNumber(projection turnPagesProjection) int {
	if len(projection.Pages) == 0 {
		return 0
	}
	if transcriptMapString(projection.Shell, "status") == "needs_input" {
		for i := 0; i < len(projection.Pages); i++ {
			page := projection.Pages[i]
			if page.Kind == "question" && !page.Answered {
				return page.Number
			}
		}
	}
	return len(projection.Pages)
}

func turnPageQuestionSetState(page turnEventPage) (int, int, int, bool) {
	questionTimelineID := ""
	questionIndex := page.QuestionIndex
	questionSet := page.QuestionSet
	questionCount := 0
	answered := false
	for _, event := range orderedTranscriptEvents(page.Events) {
		if isTurnAwaitingInputEvent(event) {
			questionTimelineID = awaitingInputTimelineID(event)
			payload := transcriptPayload(event)
			if rawIndex, ok := transcriptNumeric(payload["question_index"]); ok {
				questionIndex = int(rawIndex)
			}
			if rawSet, ok := transcriptNumeric(payload["question_set"]); ok {
				questionSet = int(rawSet)
			}
			if questions, ok := payload["questions"].([]any); ok {
				questionCount = len(questions)
			}
			continue
		}
		if isTurnInputAnsweredForQuestion(event, questionTimelineID) {
			answered = true
		}
	}
	return questionSet, questionIndex, questionCount, answered
}
