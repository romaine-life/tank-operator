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
