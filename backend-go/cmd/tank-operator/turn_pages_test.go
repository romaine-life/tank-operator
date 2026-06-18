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
	if len(proj.FinalAnswerEntries) != 1 {
		t.Fatalf("final answer entries = %d, want 1: %#v", len(proj.FinalAnswerEntries), proj.FinalAnswerEntries)
	}
	if got := transcriptMapString(proj.FinalAnswerEntries[0], "id"); got != lastMsgTimeline {
		t.Fatalf("final answer id = %q, want %q", got, lastMsgTimeline)
	}
	if got := transcriptMapString(proj.FinalAnswerEntries[0], "turnDetailRole"); got != "final_answer" {
		t.Fatalf("final answer role = %q, want final_answer: %#v", got, proj.FinalAnswerEntries[0])
	}
	if collapsible, _ := proj.Collapse["collapsible"].(bool); !collapsible {
		t.Fatalf("collapse.collapsible = %#v, want true: %#v", proj.Collapse["collapsible"], proj.Collapse)
	}
}

func TestProjectTurnPagesFinalAnswerIsServerOwnedAcrossPages(t *testing.T) {
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
	for i := 0; i < turnPageEventLimit+2; i++ {
		events = append(events, projectionTestEvent(
			fmt.Sprintf("tool-%d", i), next(), "item.completed", "tool", "claude", "turn-1",
			fmt.Sprintf("turn-1:item:tool-%d", i), map[string]any{"kind": "tool_result", "name": "Read", "output": "x"},
		))
	}
	const finalID = "turn-1:item:final"
	events = append(events,
		projectionTestEvent("final", next(), "item.completed", "assistant", "claude", "turn-1", finalID, map[string]any{
			"kind": "message", "text": "done",
		}),
		projectionTestEvent("terminal", next(), "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload(finalID)),
	)

	proj := projectTurnPages("turn-1", events)
	if len(proj.Pages) < 2 {
		t.Fatalf("page count = %d, want paged activity", len(proj.Pages))
	}
	if len(proj.FinalAnswerEntries) != 1 {
		t.Fatalf("final answer entries = %#v, want one", proj.FinalAnswerEntries)
	}
	firstPageHasFinal := false
	for _, entry := range proj.Pages[0].Entries {
		if transcriptMapString(entry, "id") == finalID {
			firstPageHasFinal = true
		}
	}
	if firstPageHasFinal {
		t.Fatalf("test fixture expected final answer outside page 1 body")
	}
	if got := transcriptMapString(proj.FinalAnswerEntries[0], "text"); got != "done" {
		t.Fatalf("final answer text = %q, want done", got)
	}
	if got := proj.Collapse["final_answer_count"]; got != 1 {
		t.Fatalf("collapse final_answer_count = %#v, want 1", got)
	}
}

func TestProjectTurnPagesFinalAnswerCarriesTerminalUsage(t *testing.T) {
	finalID := "turn-1:item:final"
	usage := map[string]any{
		"input_tokens":                3560,
		"cache_creation_input_tokens": 40842,
		"cache_read_input_tokens":     21303,
		"output_tokens":               1498,
	}
	observation := map[string]any{
		"usage_source":       "claude.result",
		"terminal_had_usage": true,
	}
	terminalPayload := projectionFinalAnswerPayload(finalID)
	terminalPayload["usage"] = usage
	terminalPayload["usage_observation"] = observation

	proj := projectTurnPages("turn-1", []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "summarize the bears wikipedia article for me",
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("final", "00000003", "item.completed", "assistant", "claude", "turn-1", finalID, map[string]any{
			"kind": "message",
			"text": "Here is the summary.",
		}),
		projectionTestEvent("terminal", "00000004", "turn.completed", "runner", "claude", "turn-1", "", terminalPayload),
	})

	if len(proj.FinalAnswerEntries) != 1 {
		t.Fatalf("final answer entries = %#v, want one", proj.FinalAnswerEntries)
	}
	final := proj.FinalAnswerEntries[0]
	if got := transcriptAnyMap(final["turnUsage"]); got == nil {
		t.Fatalf("final answer missing turnUsage: %#v", final)
	} else if got["cache_read_input_tokens"] != usage["cache_read_input_tokens"] {
		t.Fatalf("final answer turnUsage = %#v, want %#v", got, usage)
	}
	if got := transcriptAnyMap(final["usageObservation"]); got == nil {
		t.Fatalf("final answer missing usageObservation: %#v", final)
	} else if got["usage_source"] != observation["usage_source"] {
		t.Fatalf("final answer usageObservation = %#v, want %#v", got, observation)
	}
}

func TestProjectTurnPagesCompletedTurnFallsBackToLastAssistantAsFinalAnswer(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool", "00000002", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "tool_result", "name": "Bash", "output": "collapse-smoke",
		}),
		projectionTestEvent("final", "00000003", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:final", map[string]any{
			"kind": "message", "text": "Command completed.",
		}),
		projectionTestEvent("terminal", "00000004", "turn.completed", "runner", "claude", "turn-1", "", map[string]any{
			"status": "completed",
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.FinalAnswerEntries) != 1 {
		t.Fatalf("final answer entries = %#v, want fallback final assistant message", proj.FinalAnswerEntries)
	}
	if got := transcriptMapString(proj.FinalAnswerEntries[0], "id"); got != "turn-1:item:final" {
		t.Fatalf("final answer id = %q, want fallback assistant id", got)
	}
	if got := transcriptMapString(proj.FinalAnswerEntries[0], "turnDetailRole"); got != "final_answer" {
		t.Fatalf("final answer role = %q, want final_answer", got)
	}
	if got := proj.Collapse["final_answer_count"]; got != 1 {
		t.Fatalf("collapse final_answer_count = %#v, want 1", got)
	}
	if collapsible, _ := proj.Collapse["collapsible"].(bool); !collapsible {
		t.Fatalf("collapse.collapsible = %#v, want true: %#v", proj.Collapse["collapsible"], proj.Collapse)
	}
	if defaultCollapsed, _ := proj.Collapse["default_collapsed"].(bool); !defaultCollapsed {
		t.Fatalf("collapse.default_collapsed = %#v, want true: %#v", proj.Collapse["default_collapsed"], proj.Collapse)
	}
}

func TestProjectTurnPagesNoFinalAnswerIsNotCollapsible(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool", "00000002", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "tool_result", "name": "Read", "output": "x",
		}),
		projectionTestEvent("terminal", "00000003", "turn.failed", "runner", "claude", "turn-1", "", map[string]any{
			"reason": "provider_error",
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.FinalAnswerEntries) != 0 {
		t.Fatalf("final answer entries = %#v, want none", proj.FinalAnswerEntries)
	}
	if collapsible, _ := proj.Collapse["collapsible"].(bool); collapsible {
		t.Fatalf("collapse.collapsible = true, want false: %#v", proj.Collapse)
	}
	if got := transcriptMapString(proj.Collapse, "reason"); got != "no_final_answer" {
		t.Fatalf("collapse reason = %q, want no_final_answer", got)
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

func TestProjectTurnPagesIncludesUserContextOutsidePageBody(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "diagnose the turn header",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("tool-a", "00000003", "item.started", "tool", "claude", "turn-1", "turn-1:item:a", map[string]any{
			"kind": "tool", "name": "Read",
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if proj.TurnContext == nil {
		t.Fatalf("TurnContext = nil, want projected initiating user message")
	}
	if got := transcriptMapString(proj.TurnContext, "id"); got != "turn-1:user" {
		t.Fatalf("TurnContext id = %q, want turn-1:user: %#v", got, proj.TurnContext)
	}
	if got := transcriptMapString(proj.TurnContext, "text"); got != "diagnose the turn header" {
		t.Fatalf("TurnContext text = %q", got)
	}
	if proj.TurnContext["turnContext"] != true {
		t.Fatalf("TurnContext marker = %#v, want true", proj.TurnContext["turnContext"])
	}
	if len(proj.Pages) != 1 {
		t.Fatalf("page count = %d, want 1", len(proj.Pages))
	}
	for _, entry := range proj.Pages[0].Entries {
		if entry["kind"] == "message" && entry["role"] == "user" {
			t.Fatalf("human user message leaked into activity page body: %#v", proj.Pages[0].Entries)
		}
	}
}

func TestProjectTurnPagesIncludesSystemContextForBackgroundWake(t *testing.T) {
	const prompt = "A background task you started earlier has finished while this session was idle.\n\nTask id: task-ci\nFinal status: completed\n\nReview the task's output and continue from the result."
	events := []map[string]any{
		projectionTestEvent("wake-submitted", "00000001", "turn.submitted", "runner", "tank", "turn_bgtask-task-ci", "", map[string]any{
			"status":  "submitted",
			"source":  "background-task",
			"task_id": "task-ci",
			"prompt":  prompt,
		}),
		projectionTestEvent("wake-tool", "00000002", "item.completed", "tool", "claude", "turn_bgtask-task-ci", "turn_bgtask-task-ci:item:tool", map[string]any{
			"kind": "tool_result", "name": "Bash", "output": "ok",
		}),
	}

	proj := projectTurnPages("turn_bgtask-task-ci", events)
	if proj.TurnContext == nil {
		t.Fatalf("TurnContext = nil, want projected system wake prompt")
	}
	if got := transcriptMapString(proj.TurnContext, "id"); got != "wake-submitted:turn_context" {
		t.Fatalf("TurnContext id = %q, want wake-submitted:turn_context: %#v", got, proj.TurnContext)
	}
	if got := transcriptMapString(proj.TurnContext, "text"); got != prompt {
		t.Fatalf("TurnContext text = %q, want wake prompt", got)
	}
	if got := transcriptMapString(proj.TurnContext, "authorKind"); got != "system" {
		t.Fatalf("TurnContext authorKind = %q, want system: %#v", got, proj.TurnContext)
	}
	if got := transcriptMapString(proj.TurnContext, "turnContextSource"); got != "background-task" {
		t.Fatalf("TurnContext source = %q, want background-task: %#v", got, proj.TurnContext)
	}
	for _, page := range proj.Pages {
		for _, entry := range page.Entries {
			if entry["kind"] == "message" && entry["role"] == "user" {
				t.Fatalf("background wake prompt leaked into activity page body as a user message: %#v", page.Entries)
			}
		}
	}
}

func TestProjectTurnPagesKeepsUserContextAcrossPagedActivity(t *testing.T) {
	var events []map[string]any
	seq := 0
	next := func() string {
		seq++
		return fmt.Sprintf("%08d", seq)
	}
	events = append(events,
		projectionTestEvent("u", next(), "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "keep me visible",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", next(), "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
	)
	for i := 0; i < turnPageEventLimit+1; i++ {
		events = append(events, projectionTestEvent(
			fmt.Sprintf("tool-%d", i), next(), "item.completed", "tool", "claude", "turn-1",
			fmt.Sprintf("turn-1:item:%d", i), map[string]any{"kind": "tool_result", "name": "Read", "output": "x"},
		))
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.Pages) < 2 {
		t.Fatalf("page count = %d, want at least 2", len(proj.Pages))
	}
	if got := transcriptMapString(proj.TurnContext, "text"); got != "keep me visible" {
		t.Fatalf("TurnContext text = %q, want keep me visible", got)
	}
	if len(proj.Pages[1].Entries) == 0 {
		t.Fatalf("second page entries empty, want activity")
	}
}

func TestProjectTurnPagesAskingTurnKeepsInvocationAndQuestionProseTogether(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("tool-a", "00000003", "item.completed", "tool", "claude", "turn-1", "turn-1:item:a", map[string]any{
			"kind": "tool_result", "name": "Read", "output": "x",
		}),
		projectionTestEvent("invoke", "00000004", "turn.awaiting_input.invocation", "runner", "claude", "turn-1", "turn-1:item:ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"timeline_id":      "turn-1:item:ask",
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
		projectionTestEvent("msg", "00000005", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Which path?\n2. Deploy after?",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-2",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          "turn-2:item:ask",
				"provider_timeline_id": "turn-1:item:ask",
				"questions": []any{
					map[string]any{"question": "Which path?"},
					map[string]any{"question": "Deploy after?"},
				},
			},
		}),
	}

	proj := projectTurnPages("turn-1", events)
	if len(proj.Pages) != 1 {
		t.Fatalf("page count = %d, want one asking-turn activity page", len(proj.Pages))
	}
	activityPage := proj.Pages[0]
	if activityPage.Kind != "activity" {
		t.Fatalf("page kind = %q, want activity", activityPage.Kind)
	}
	if len(activityPage.Entries) != 3 {
		t.Fatalf("activity entries = %d, want prior tool + AskUserQuestion marker + question prose: %#v", len(activityPage.Entries), activityPage.Entries)
	}
	marker := activityPage.Entries[1]
	if marker["kind"] != "tool" || marker["toolName"] != "AskUserQuestion" {
		t.Fatalf("activity marker = %#v, want AskUserQuestion tool row", marker)
	}
	if marker["toolStatus"] != "completed" {
		t.Fatalf("marker toolStatus = %v, want completed", marker["toolStatus"])
	}
	target, _ := marker["questionTarget"].(map[string]any)
	if target == nil {
		t.Fatalf("marker questionTarget missing: %#v", marker)
	}
	if target["turnId"] != "turn-2" {
		t.Fatalf("marker questionTarget.turnId = %v, want turn-2", target["turnId"])
	}
	if target["timelineId"] != "turn-2:item:ask" {
		t.Fatalf("marker questionTarget.timelineId = %v, want turn-2:item:ask", target["timelineId"])
	}
	if target["page"] != 1 {
		t.Fatalf("marker questionTarget.page = %v, want 1", target["page"])
	}
	if activityPage.Entries[2]["kind"] != "message" || activityPage.Entries[2]["role"] != "assistant" {
		t.Fatalf("last activity entry = %#v, want assistant question prose", activityPage.Entries[2])
	}
}

func TestProjectTurnPagesQuestionOnlyTurnOwnsOnlyQuestionPages(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("submitted", "00000001", "turn.submitted", "runner", "tank", "turn-2", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("await", "00000002", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:ask",
			"provider_timeline_id": "turn-1:item:ask",
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

	proj := projectTurnPages("turn-2", events)
	if got := transcriptMapString(proj.Shell, "status"); got != "needs_input" {
		t.Fatalf("shell status = %q, want needs_input", got)
	}
	if len(proj.Pages) != 2 {
		t.Fatalf("page count = %d, want one page per question", len(proj.Pages))
	}
	firstQuestionPage := proj.Pages[0]
	if firstQuestionPage.Kind != "question" {
		t.Fatalf("first question page kind = %q, want question", firstQuestionPage.Kind)
	}
	if firstQuestionPage.QuestionIndex != 1 || firstQuestionPage.QuestionCount != 2 {
		t.Fatalf("first question page index/count = %d/%d, want 1/2", firstQuestionPage.QuestionIndex, firstQuestionPage.QuestionCount)
	}
	if firstQuestionPage.QuestionSet != 1 {
		t.Fatalf("first question page set = %d, want 1", firstQuestionPage.QuestionSet)
	}
	if firstQuestionPage.Answered {
		t.Fatalf("question page answered = true, want false")
	}
	if !firstQuestionPage.Sealed {
		t.Fatalf("first pending question page sealed = false, want sealed because the next question page is live")
	}
	if len(firstQuestionPage.Entries) != 1 || firstQuestionPage.Entries[0]["metaKind"] != "awaiting_input" {
		t.Fatalf("first question entries = %#v, want only awaiting_input card", firstQuestionPage.Entries)
	}
	secondQuestionPage := proj.Pages[1]
	if secondQuestionPage.Kind != "question" {
		t.Fatalf("second question page kind = %q, want question", secondQuestionPage.Kind)
	}
	if secondQuestionPage.QuestionIndex != 2 || secondQuestionPage.QuestionCount != 2 {
		t.Fatalf("second question page index/count = %d/%d, want 2/2", secondQuestionPage.QuestionIndex, secondQuestionPage.QuestionCount)
	}
	if secondQuestionPage.QuestionSet != 1 {
		t.Fatalf("second question page set = %d, want 1", secondQuestionPage.QuestionSet)
	}
	if secondQuestionPage.Sealed {
		t.Fatalf("last pending question page sealed = true, want live while the turn needs input")
	}
	if got := defaultTurnActivityPageNumber(proj); got != firstQuestionPage.Number {
		t.Fatalf("default page = %d, want first pending question page %d", got, firstQuestionPage.Number)
	}
}

func TestProjectTurnPagesQuestionOnlyTurnIncludesAskingFinalAnswerContext(t *testing.T) {
	finalMessage := projectionTestEvent("final", "00000003", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:final", map[string]any{
		"kind": "message",
		"text": "I found two deployment paths. The safer one is the staged rollout.",
	})
	finalMessage[questionFinalAnswerContextForTurnField] = "turn-2"

	events := []map[string]any{
		projectionTestEvent("submitted", "00000004", "turn.submitted", "runner", "tank", "turn-2", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("await", "00000005", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:ask",
			"provider_timeline_id": "turn-1:item:ask",
			"asking_turn_final_answer": map[string]any{
				"timeline_ids":      []any{"turn-1:item:final"},
				"provider_item_ids": []any{"assistant:final"},
			},
			"questions": []any{
				map[string]any{"question": "Which path?"},
			},
		}),
		finalMessage,
	}

	proj := projectTurnPages("turn-2", events)
	if len(proj.Pages) != 1 {
		t.Fatalf("page count = %d, want only the question page", len(proj.Pages))
	}
	page := proj.Pages[0]
	if page.Kind != "question" {
		t.Fatalf("page kind = %q, want question", page.Kind)
	}
	if proj.TotalEventCount != 2 {
		t.Fatalf("total event count = %d, want question-turn events only", proj.TotalEventCount)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("page entries = %d, want final message context + awaiting card: %#v", len(page.Entries), page.Entries)
	}
	context := page.Entries[0]
	if context["questionFinalAnswerContext"] != true || context["kind"] != "message" || context["role"] != "assistant" {
		t.Fatalf("first entry = %#v, want assistant final-answer context message", context)
	}
	if got := transcriptMapString(context, "text"); got != "I found two deployment paths. The safer one is the staged rollout." {
		t.Fatalf("context text = %q", got)
	}
	if context["turnId"] != "turn-1" {
		t.Fatalf("context turnId = %v, want asking turn", context["turnId"])
	}
	if page.Entries[1]["metaKind"] != "awaiting_input" {
		t.Fatalf("second entry = %#v, want awaiting input card", page.Entries[1])
	}
}

func TestProjectTurnPagesQuestionOnlyTurnSealsAfterDurableAnswer(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("await", "00000001", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:ask",
			"provider_timeline_id": "turn-1:item:ask",
			"questions": []any{
				map[string]any{
					"question": "Pick one",
					"options":  []any{map[string]any{"label": "A"}},
				},
			},
		}),
		projectionTestEvent("answer", "00000002", "turn.input_answered", "user", "tank", "turn-2", "turn-2:item:ask:answer", map[string]any{
			"question_timeline_id": "turn-2:item:ask",
			"provider_item_id":     "toolu_ask",
			"answers":              map[string]any{"Pick one": []any{"A"}},
		}),
	}

	proj := projectTurnPages("turn-2", events)
	if len(proj.Pages) != 1 {
		t.Fatalf("page count = %d, want only answered question page", len(proj.Pages))
	}
	if proj.Pages[0].Kind != "question" || !proj.Pages[0].Answered {
		t.Fatalf("page = kind %q answered %v, want answered question", proj.Pages[0].Kind, proj.Pages[0].Answered)
	}
}

// TestProjectTurnPagesQuestionOnlyTurnStopFoldsIntoSingleQuestionPage pins #1312:
// Stop drives a question turn to its dismissing turn.interrupted via a preceding
// turn.interrupt_requested on the SAME turn. That pre-terminal marker is not a
// dismissal terminal, so before the fix it broke the pending-question fold and
// spilled the Stop sequence into a spurious trailing "activity" page. A dismissed
// turn defaults to its LAST page, so that extra page opened the Turns view on a
// contextless activity page and stranded the prompt slot on "Prompt context
// unavailable". The Stop sequence must fold onto the one question page.
func TestProjectTurnPagesQuestionOnlyTurnStopFoldsIntoSingleQuestionPage(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("submitted", "00000001", "turn.submitted", "runner", "tank", "turn-2", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("await", "00000002", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:ask", map[string]any{
			"asking_turn_id":   "turn-1",
			"question_turn_id": "turn-2",
			"provider_item_id": "toolu_ask",
			"timeline_id":      "turn-2:item:ask",
			"questions": []any{
				map[string]any{"question": "Pick one", "options": []any{map[string]any{"label": "A"}}},
			},
		}),
		// Stop: interrupt_requested precedes the dismissing interrupted, both on
		// the question turn — the exact sequence the live ledger showed (#1312).
		projectionTestEvent("interrupt-req", "00000003", "turn.interrupt_requested", "runner", "tank", "turn-2", "", map[string]any{"client_nonce": "stop-1"}),
		projectionTestEvent("interrupted", "00000004", "turn.interrupted", "runner", "claude", "turn-2", "", map[string]any{"reason": "question_dismissed_by_stop"}),
	}

	proj := projectTurnPages("turn-2", events)
	if len(proj.Pages) != 1 {
		t.Fatalf("page count = %d, want a single question page; the Stop sequence must not spill a trailing activity page: %#v", len(proj.Pages), proj.Pages)
	}
	page := proj.Pages[0]
	if page.Kind != "question" {
		t.Fatalf("page kind = %q, want question", page.Kind)
	}
	if page.QuestionIndex != 1 || page.QuestionCount != 1 {
		t.Fatalf("page index/count = %d/%d, want 1/1", page.QuestionIndex, page.QuestionCount)
	}
	if page.Answered {
		t.Fatalf("dismissed question must not report answered")
	}
	if got := defaultTurnActivityPageNumber(proj); got != page.Number {
		t.Fatalf("default page = %d, want the question page %d; a trailing activity page would open the Turns view contextless and strand the prompt slot on %q", got, page.Number, "Prompt context unavailable")
	}
	var card map[string]any
	for _, entry := range page.Entries {
		if entry["metaKind"] == "awaiting_input" {
			card = entry
		}
	}
	if card == nil {
		t.Fatalf("no awaiting card on the question page: %#v", page.Entries)
	}
	awaiting := card["awaitingInput"].(map[string]any)
	if dismissed, _ := awaiting["dismissed"].(bool); !dismissed {
		t.Fatalf("awaiting card not dismissed after Stop: %#v", awaiting)
	}
}

func TestProjectTurnPagesLegacySameTurnAwaitingInputStillShowsInvocationMarkerPage(t *testing.T) {
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
	if proj.Pages[1].Kind != "question" {
		t.Fatalf("second page kind = %q, want question", proj.Pages[1].Kind)
	}
	if got := defaultTurnActivityPageNumber(proj); got != 2 {
		t.Fatalf("default page = %d, want pending question page 2", got)
	}
}

// TestProjectTurnPagesChainFinalAnswerIsLastTerminals pins the session-161
// page-layer defect: a parked origin turn's promoted ack ("I'll report when it
// completes") was rendered as the page's final answer below — and visually
// replacing — the later wake content, because final-answer ids were unioned
// across every terminal in the folded chain. The chain's LAST completed
// terminal owns the final answer; when that link promoted nothing, the turn
// has no final answer and no fallback may resurrect the superseded ack.
func TestProjectTurnPagesChainFinalAnswerIsLastTerminals(t *testing.T) {
	mkEvents := func(wakeFinalAnswer bool) []map[string]any {
		wakeTurnID := "turn_bgtask-task-ci"
		wakeTerminalPayload := map[string]any{}
		if wakeFinalAnswer {
			wakeTerminalPayload = projectionFinalAnswerPayload(wakeTurnID + ":item:final")
		}
		return []map[string]any{
			projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
				"text": "Run CI and tell me when it passes.",
			}),
			projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
			projectionTestEvent("task-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
				"task_id": "task-ci", "status": "running", "summary": "CI check",
			}),
			projectionTestEvent("ack", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:ack", map[string]any{
				"kind": "message", "text": "Started. I'll report when it completes.",
			}),
			projectionTestEvent("turn-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:ack")),
			projectionTestEvent("task-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
				"task_id": "task-ci", "status": "completed", "summary": "CI passed",
			}),
			projectionTestEvent("wake-submitted", "007", "turn.submitted", "runner", "tank", wakeTurnID, "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "task-ci", "prompt": "A background task you started earlier has finished."}),
			projectionTestEvent("wake-final", "008", "item.completed", "assistant", "claude", wakeTurnID, wakeTurnID+":item:final", map[string]any{
				"kind": "message", "text": "CI passed. The branch is ready.",
			}),
			projectionTestEvent("wake-terminal", "009", "turn.completed", "runner", "claude", wakeTurnID, "", wakeTerminalPayload),
		}
	}

	// Chain whose last link promoted a real final answer: that answer — never
	// the superseded ack — is the page's final answer.
	projection := projectTurnPages("turn-1", mkEvents(true))
	if got, want := len(projection.FinalAnswerEntries), 1; got != want {
		t.Fatalf("final answer entries = %d, want %d: %#v", got, want, projection.FinalAnswerEntries)
	}
	if got, want := transcriptMapString(projection.FinalAnswerEntries[0], "text"), "CI passed. The branch is ready."; got != want {
		t.Fatalf("final answer text = %q, want wake chain final %q", got, want)
	}

	// Chain whose last link promoted nothing (the empty codex wake replies of
	// session 161): no final answer, and no fallback resurrection of the ack.
	projection = projectTurnPages("turn-1", mkEvents(false))
	if got := len(projection.FinalAnswerEntries); got != 0 {
		t.Fatalf("final answer entries = %d, want none (superseded ack must not resurrect): %#v", got, projection.FinalAnswerEntries)
	}

	// The body still reads chronologically: ack before the wake chip.
	body := projection.Pages[len(projection.Pages)-1].Entries
	ackIdx, promptIdx := -1, -1
	for i, entry := range body {
		if transcriptMapString(entry, "text") == "Started. I'll report when it completes." {
			ackIdx = i
		}
		if isBackgroundWakeChip(entry, "A background task you started earlier has finished.") {
			promptIdx = i
		}
	}
	if ackIdx == -1 || promptIdx == -1 || ackIdx > promptIdx {
		t.Fatalf("page body not chronological (ack=%d chip=%d): %#v", ackIdx, promptIdx, body)
	}
}
