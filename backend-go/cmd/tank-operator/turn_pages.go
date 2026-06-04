package main

import (
	"context"

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
	return all, nil
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
	Answered      bool             `json:"answered,omitempty"`
}

type turnEventPage struct {
	Kind          string
	Events        []map[string]any
	QuestionIndex int
}

// turnPagesProjection is the page-aware projection of a single turn: a
// terminal-correct shell summary plus the ordered page directory and bodies.
type turnPagesProjection struct {
	TurnID          string
	Shell           map[string]any
	Pages           []turnPage
	TotalEventCount int
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
// size threshold: each turn.awaiting_input pause owns a dedicated question-set
// page. The activity before the question ends, the question page carries the
// durable pause plus its matching durable answer when present, and later
// provider work resumes on a normal activity page.
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
	}

	for _, event := range ordered {
		if len(pendingQuestionPages) > 0 {
			if isTurnInputAnsweredForQuestion(event, pendingQuestionTimelineID) {
				flushPendingQuestionPages(event)
				continue
			}
			flushPendingQuestionPages(nil)
		}
		if isTurnAwaitingInputEvent(event) {
			current = append(current, awaitingInputInvocationEvent(event))
			flush()
			pendingQuestionPages = awaitingInputQuestionPages(event)
			pendingQuestionTimelineID = awaitingInputTimelineID(event)
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

func awaitingInputInvocationEvent(event map[string]any) map[string]any {
	out := cloneAnyMap(event)
	out["type"] = "turn.awaiting_input.invocation"
	return out
}

func awaitingInputQuestionPages(event map[string]any) []turnEventPage {
	questions := projectionAwaitingInputQuestions(event)
	if len(questions) == 0 {
		return []turnEventPage{{Kind: "question_set", Events: []map[string]any{event}}}
	}
	pages := make([]turnEventPage, 0, len(questions))
	for idx := range questions {
		pages = append(pages, turnEventPage{
			Kind:          "question_set",
			Events:        []map[string]any{awaitingInputQuestionPageEvent(event, idx, len(questions))},
			QuestionIndex: idx + 1,
		})
	}
	return pages
}

func awaitingInputQuestionPageEvent(event map[string]any, index, count int) map[string]any {
	out := cloneAnyMap(event)
	payload := cloneAnyMap(transcriptPayload(event))
	payload["question_index"] = index + 1
	payload["question_count"] = count
	out["payload"] = payload
	return out
}

func awaitingInputTimelineID(event map[string]any) string {
	if timelineID := transcriptPayloadString(event, "timeline_id"); timelineID != "" {
		return timelineID
	}
	return transcriptString(event, "timeline_id")
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
// User-message and turn-progress rows are transcript-level, not page body, and
// are dropped; the context.compacted marker is kept as the page's seam header.
func projectPageBodyEntries(events []map[string]any) []map[string]any {
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	flat := state.projectFlatEntries()
	out := make([]map[string]any, 0, len(flat))
	for _, entry := range flat {
		if isProjectedUserMessage(entry) || isProjectionTurnProgress(entry) {
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
	pageSlices := splitTurnEventsIntoSemanticPages(events)

	// Terminal-correct shell from the COMPLETE event set: the full projection
	// folds the whole turn, so its activity summary always reflects the
	// terminal regardless of how many events the turn has.
	full := projectTranscriptEvents(events)
	status := ""
	shell := map[string]any{}
	if body, ok := full.ActivityBodies[turnID]; ok {
		shell = cloneAnyMap(body.Summary)
		status = body.Status
	}
	live := turnPageStatusIsLive(status)

	pages := make([]turnPage, 0, len(pageSlices))
	directory := make([]map[string]any, 0, len(pageSlices))
	for i, slice := range pageSlices {
		number := i + 1
		// Every page but the last is sealed; the last page is sealed too once
		// the turn has reached a durable terminal.
		sealed := number < len(pageSlices) || !live
		questionIndex, questionCount, answered := turnPageQuestionSetState(slice)
		entries := projectPageBodyEntries(slice.Events)
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
		if page.Kind == "question_set" {
			pageInfo["questionCount"] = page.QuestionCount
			pageInfo["questionIndex"] = page.QuestionIndex
			pageInfo["answered"] = page.Answered
		}
		directory = append(directory, pageInfo)
	}

	shell["pageCount"] = len(pages)
	shell["totalEventCount"] = len(events)
	shell["pages"] = directory

	return turnPagesProjection{
		TurnID:          turnID,
		Shell:           shell,
		Pages:           pages,
		TotalEventCount: len(events),
	}
}

func defaultTurnActivityPageNumber(projection turnPagesProjection) int {
	if len(projection.Pages) == 0 {
		return 0
	}
	if transcriptMapString(projection.Shell, "status") == "needs_input" {
		for i := 0; i < len(projection.Pages); i++ {
			page := projection.Pages[i]
			if page.Kind == "question_set" && !page.Answered {
				return page.Number
			}
		}
	}
	return len(projection.Pages)
}

func turnPageQuestionSetState(page turnEventPage) (int, int, bool) {
	questionTimelineID := ""
	questionIndex := page.QuestionIndex
	questionCount := 0
	answered := false
	for _, event := range orderedTranscriptEvents(page.Events) {
		if isTurnAwaitingInputEvent(event) {
			questionTimelineID = awaitingInputTimelineID(event)
			payload := transcriptPayload(event)
			if rawIndex, ok := transcriptNumeric(payload["question_index"]); ok {
				questionIndex = int(rawIndex)
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
	return questionIndex, questionCount, answered
}
