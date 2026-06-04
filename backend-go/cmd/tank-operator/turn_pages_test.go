package main

import (
	"fmt"
	"testing"
)

// The load-bearing regression: a turn with more than turnPageEventLimit events
// must still materialize a terminal-correct shell (the bug was that the
// terminal, always the last event, fell outside the first-1000 window and the
// turn looked perpetually active).
func TestProjectTurnPagesKeepsTerminalShellWhenOverLimit(t *testing.T) {
	var events []map[string]any
	seq := 0
	next := func() string {
		seq++
		return fmt.Sprintf("%08d", seq)
	}

	events = append(events,
		projectionTestEvent("u", next(), "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "go",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", next(), "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
	)

	// More than one page worth of tool activity.
	var lastMsgTimeline string
	for i := 0; i < turnPageEventLimit+10; i++ {
		lastMsgTimeline = fmt.Sprintf("turn-1:item:msg-%d", i)
		events = append(events, projectionTestEvent(
			fmt.Sprintf("msg-%d", i), next(), "item.completed", "assistant", "claude", "turn-1", lastMsgTimeline,
			map[string]any{"kind": "message", "text": fmt.Sprintf("step %d", i)},
		))
	}

	events = append(events, projectionTestEvent(
		"terminal", next(), "turn.completed", "runner", "claude", "turn-1", "",
		projectionFinalAnswerPayload(lastMsgTimeline),
	))

	proj := projectTurnPages("turn-1", events)

	if got := transcriptMapString(proj.Shell, "status"); got != "completed" {
		t.Fatalf("shell status = %q, want completed (terminal must not be dropped for an over-limit turn)", got)
	}
	if proj.Shell["active"] == true {
		t.Fatalf("shell active = true, want false for a completed turn")
	}
	if proj.TotalEventCount != len(events) {
		t.Fatalf("totalEventCount = %d, want %d", proj.TotalEventCount, len(events))
	}
	pageCount, _ := proj.Shell["pageCount"].(int)
	if pageCount < 2 {
		t.Fatalf("pageCount = %d, want >= 2 for an over-limit turn", pageCount)
	}
	if len(proj.Pages) != pageCount {
		t.Fatalf("len(pages) = %d, want %d", len(proj.Pages), pageCount)
	}
	last := proj.Pages[len(proj.Pages)-1]
	if !last.Sealed {
		t.Fatalf("last page sealed = false, want true once the turn has a durable terminal")
	}
	// Every event is accounted for across the pages exactly once.
	total := 0
	for _, p := range proj.Pages {
		total += p.EventCount
	}
	if total != len(events) {
		t.Fatalf("sum of page event counts = %d, want %d", total, len(events))
	}
}

// Pages seal at the event threshold: each page holds at most turnPageEventLimit
// events, and they partition the turn's events in order with none lost.
func TestSplitTurnEventsIntoPagesSealsAtThreshold(t *testing.T) {
	var events []map[string]any
	total := turnPageEventLimit*2 + 5
	for i := 0; i < total; i++ {
		events = append(events, projectionTestEvent(
			fmt.Sprintf("e-%d", i), fmt.Sprintf("%08d", i+1), "item.completed", "tool", "claude", "turn-1",
			fmt.Sprintf("turn-1:item:%d", i), map[string]any{"kind": "tool_result", "name": "Read", "output": "x"},
		))
	}

	pages := splitTurnEventsIntoPages(events)
	if len(pages) != 3 {
		t.Fatalf("page count = %d, want 3 for %d events at limit %d", len(pages), total, turnPageEventLimit)
	}
	if len(pages[0]) != turnPageEventLimit || len(pages[1]) != turnPageEventLimit {
		t.Fatalf("full pages = %d,%d events, want %d each", len(pages[0]), len(pages[1]), turnPageEventLimit)
	}
	if len(pages[2]) != total-2*turnPageEventLimit {
		t.Fatalf("last page = %d events, want %d", len(pages[2]), total-2*turnPageEventLimit)
	}
	seen := 0
	for _, p := range pages {
		seen += len(p)
	}
	if seen != total {
		t.Fatalf("events across pages = %d, want %d (no loss/overlap)", seen, total)
	}
}

// A short, still-running turn is a single live (unsealed) page.
func TestProjectTurnPagesSinglePageLiveTurn(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("tool-a", "00000003", "item.started", "tool", "claude", "turn-1", "turn-1:item:a", map[string]any{
			"kind": "tool", "name": "Read",
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if proj.Shell["pageCount"].(int) != 1 {
		t.Fatalf("pageCount = %v, want 1", proj.Shell["pageCount"])
	}
	if len(proj.Pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(proj.Pages))
	}
	if proj.Pages[0].Sealed {
		t.Fatalf("single live page sealed = true, want false (turn still running)")
	}
}

func TestProjectTurnPagesMakesQuestionSetSemanticPage(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("tool-a", "00000003", "item.completed", "tool", "claude", "turn-1", "turn-1:item:a", map[string]any{
			"kind": "tool_result", "name": "Read", "output": "x",
		}),
		projectionTestEvent("await", "00000004", "turn.awaiting_input", "runner", "claude", "turn-1", "turn-1:item:ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"questions": []any{
				map[string]any{
					"question":      "Which path?",
					"multiSelect":   false,
					"allowFreeForm": true,
					"options": []any{
						map[string]any{"label": "A"},
						map[string]any{"label": "B"},
					},
				},
				map[string]any{
					"question":    "Deploy after?",
					"multiSelect": false,
					"options": []any{
						map[string]any{"label": "Yes"},
						map[string]any{"label": "No"},
					},
				},
			},
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if got := transcriptMapString(proj.Shell, "status"); got != "needs_input" {
		t.Fatalf("shell status = %q, want needs_input", got)
	}
	if len(proj.Pages) != 2 {
		t.Fatalf("page count = %d, want activity page + question page", len(proj.Pages))
	}
	activityPage := proj.Pages[0]
	if activityPage.Kind != "activity" {
		t.Fatalf("first page kind = %q, want activity", activityPage.Kind)
	}
	if len(activityPage.Entries) != 2 {
		t.Fatalf("activity entries = %d, want prior tool + AskUserQuestion marker: %#v", len(activityPage.Entries), activityPage.Entries)
	}
	marker := activityPage.Entries[1]
	if marker["kind"] != "tool" || marker["toolName"] != "AskUserQuestion" {
		t.Fatalf("activity marker = %#v, want AskUserQuestion tool row", marker)
	}
	if marker["toolStatus"] != "completed" {
		t.Fatalf("marker toolStatus = %v, want completed", marker["toolStatus"])
	}
	questionPage := proj.Pages[1]
	if questionPage.Kind != "question_set" {
		t.Fatalf("question page kind = %q, want question_set", questionPage.Kind)
	}
	if questionPage.QuestionCount != 2 {
		t.Fatalf("question count = %d, want 2", questionPage.QuestionCount)
	}
	if questionPage.Answered {
		t.Fatalf("question page answered = true, want false")
	}
	if questionPage.Sealed {
		t.Fatalf("pending question page sealed = true, want live while the turn needs input")
	}
	if got := defaultTurnActivityPageNumber(proj); got != questionPage.Number {
		t.Fatalf("default page = %d, want pending question page %d", got, questionPage.Number)
	}
}

func TestProjectTurnPagesQuestionSetSealsAfterDurableAnswer(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("await", "00000001", "turn.awaiting_input", "runner", "claude", "turn-1", "turn-1:item:ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"questions": []any{
				map[string]any{
					"question": "Pick one",
					"options":  []any{map[string]any{"label": "A"}},
				},
			},
		}),
		projectionTestEvent("answer", "00000002", "turn.input_answered", "user", "tank", "turn-1", "turn-1:item:ask:answer", map[string]any{
			"question_timeline_id": "turn-1:item:ask",
			"provider_item_id":     "toolu_ask",
			"answers":              map[string]any{"Pick one": []any{"A"}},
		}),
		projectionTestEvent("after", "00000003", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:after", map[string]any{
			"kind": "message", "text": "continuing",
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.Pages) != 3 {
		t.Fatalf("page count = %d, want invocation marker + answered question page + resumed activity page", len(proj.Pages))
	}
	if proj.Pages[0].Kind != "activity" {
		t.Fatalf("first page kind = %q, want activity marker page", proj.Pages[0].Kind)
	}
	if len(proj.Pages[0].Entries) != 1 || proj.Pages[0].Entries[0]["toolName"] != "AskUserQuestion" {
		t.Fatalf("first page entries = %#v, want AskUserQuestion marker", proj.Pages[0].Entries)
	}
	if proj.Pages[1].Kind != "question_set" || !proj.Pages[1].Answered {
		t.Fatalf("second page = kind %q answered %v, want answered question_set", proj.Pages[1].Kind, proj.Pages[1].Answered)
	}
	if proj.Pages[2].Kind != "activity" {
		t.Fatalf("third page kind = %q, want resumed activity", proj.Pages[2].Kind)
	}
}

func TestProjectTurnPagesQuestionFirstStillShowsInvocationMarkerPage(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("await", "00000001", "turn.awaiting_input", "runner", "claude", "turn-1", "turn-1:item:ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"questions": []any{
				map[string]any{
					"question": "Can I proceed?",
					"options":  []any{map[string]any{"label": "Yes"}, map[string]any{"label": "No"}},
				},
			},
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.Pages) != 2 {
		t.Fatalf("page count = %d, want invocation marker page + question page", len(proj.Pages))
	}
	if proj.Pages[0].Kind != "activity" {
		t.Fatalf("first page kind = %q, want activity", proj.Pages[0].Kind)
	}
	if len(proj.Pages[0].Entries) != 1 {
		t.Fatalf("first page entries = %d, want one AskUserQuestion marker: %#v", len(proj.Pages[0].Entries), proj.Pages[0].Entries)
	}
	marker := proj.Pages[0].Entries[0]
	if marker["kind"] != "tool" || marker["toolName"] != "AskUserQuestion" {
		t.Fatalf("first page entry = %#v, want AskUserQuestion tool marker", marker)
	}
	if marker["sourceEventId"] != "await" {
		t.Fatalf("marker sourceEventId = %v, want durable awaiting event id", marker["sourceEventId"])
	}
	if proj.Pages[1].Kind != "question_set" {
		t.Fatalf("second page kind = %q, want question_set", proj.Pages[1].Kind)
	}
	if got := defaultTurnActivityPageNumber(proj); got != 2 {
		t.Fatalf("default page = %d, want pending question page 2", got)
	}
}
