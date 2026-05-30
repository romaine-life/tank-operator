package main

import (
	"strings"
	"testing"
)

func TestProjectTranscriptEventsEmitsCollapsedTurnActivityShell(t *testing.T) {
	events := []map[string]any{
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

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[0]["kind"] != "message" || projection.Entries[1]["kind"] != "turn_activity" || projection.Entries[2]["kind"] != "message" {
		t.Fatalf("entry kinds = [%v %v %v], want message/turn_activity/message", projection.Entries[0]["kind"], projection.Entries[1]["kind"], projection.Entries[2]["kind"])
	}
	shell := projection.Entries[1]
	activity, ok := shell["activity"].(map[string]any)
	if !ok {
		t.Fatalf("activity shell missing summary: %#v", shell)
	}
	if activity["toolCount"] != 1 || activity["childCount"] != 2 {
		t.Fatalf("activity summary = %#v, want one tool and two child log entries", activity)
	}
	if activity["lastActivityAt"] == "" {
		t.Fatalf("activity summary missing lastActivityAt: %#v", activity)
	}
	if activity["active"] == true || activity["status"] == "active" {
		t.Fatalf("completed turn activity rendered active: %#v", activity)
	}
	if _, hasChildren := shell["entries"]; hasChildren {
		t.Fatalf("collapsed shell must not inline child entries: %#v", shell)
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("missing lazy activity body for turn-1")
	}
	if got, want := len(body.Entries), 2; got != want {
		t.Fatalf("activity body entries = %d, want %d", got, want)
	}
	if got, want := body.CompactedEntryIDs, []string{"turn-1:item:tool-1"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("compacted ids = %#v, want %#v", got, want)
	}
}

func TestProjectTranscriptEventsCarriesUserAttachments(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "compare these",
			"display": map[string]any{"kind": "plain"},
			"attachments": []any{
				map[string]any{
					"label":   "Screenshot 1",
					"name":    "image.png",
					"kind":    "image",
					"path":    "screenshots/1.png",
					"absPath": "/workspace/screenshots/1.png",
					"size":    float64(123),
				},
			},
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 1; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	attachments, ok := projection.Entries[0]["attachments"].([]map[string]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %#v", projection.Entries[0]["attachments"])
	}
	if got := attachments[0]["label"]; got != "Screenshot 1" {
		t.Fatalf("attachment label = %#v", got)
	}
	if got := attachments[0]["absPath"]; got != "/workspace/screenshots/1.png" {
		t.Fatalf("attachment absPath = %#v", got)
	}
}

func TestProjectTranscriptEventsUsesExplicitFinalAnswerMarker(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "which message is final?",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("prelim", "002", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:prelim", map[string]any{
			"kind": "agent_message",
			"text": "preliminary progress",
		}),
		projectionTestEvent("final", "003", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:final", map[string]any{
			"kind": "agent_message",
			"text": "final answer",
		}),
		projectionTestEvent("terminal", "004", "turn.completed", "runner", "codex", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:final")),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got, want := projection.Entries[1]["kind"], "turn_activity"; got != want {
		t.Fatalf("middle entry kind = %v, want %s", got, want)
	}
	if got, want := projection.Entries[2]["id"], "turn-1:item:final"; got != want {
		t.Fatalf("visible assistant id = %v, want %s", got, want)
	}
	body := projection.ActivityBodies["turn-1"]
	if got, want := body.CompactedEntryIDs, []string{"turn-1:item:prelim"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("compacted ids = %#v, want %#v", got, want)
	}
	if got, want := len(body.Entries), 2; got != want {
		t.Fatalf("activity body entries = %d, want %d", got, want)
	}
}

func TestProjectTranscriptEventsCompactsUnmarkedCompletedAssistantMessages(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "missing marker",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("assistant", "002", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:assistant", map[string]any{
			"kind": "agent_message",
			"text": "unmarked",
		}),
		projectionTestEvent("terminal", "003", "turn.completed", "runner", "codex", "turn-1", "", nil),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[1]["kind"] != "turn_activity" {
		t.Fatalf("unmarked assistant should be compacted into activity: %#v", projection.Entries)
	}
	if _, ok := projection.ActivityBodies["turn-1"]; !ok {
		t.Fatal("missing activity body for unmarked completed turn")
	}
}

func TestProjectTranscriptEventsCollapsesActiveTurnBeforeFinalAnswer(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "keep going",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("note", "003", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:note", map[string]any{
			"kind": "message",
			"text": "working on it",
		}),
		projectionTestEvent("tool", "004", "item.started", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "command_execution",
			"name": "Bash",
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[1]["kind"] != "turn_activity" {
		t.Fatalf("second entry kind = %v, want turn_activity", projection.Entries[1]["kind"])
	}
	activity := projection.Entries[1]["activity"].(map[string]any)
	if activity["active"] != true {
		t.Fatalf("activity summary active = %v, want true", activity["active"])
	}
	if activity["lastActivityAt"] == "" {
		t.Fatalf("active activity summary missing lastActivityAt: %#v", activity)
	}
	if projection.Entries[0]["id"] == "turn-1:item:note" {
		t.Fatalf("active assistant prose rendered as settled transcript row")
	}
}

func TestProjectTranscriptEventsCarriesMidTurnUsageOnActiveActivity(t *testing.T) {
	usage := map[string]any{
		"input_tokens":  float64(100),
		"output_tokens": float64(25),
		"total_tokens":  float64(125),
	}
	usageObservation := map[string]any{
		"usage_source":     "thread.tokenUsage.updated",
		"provider_turn_id": "provider-turn-1",
		"update_count":     float64(1),
	}
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "think for a while",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("started", "003", "turn.started", "runner", "codex", "turn-1", "", nil),
		projectionTestEvent("usage", "004", "turn.usage", "runner", "codex", "turn-1", "", map[string]any{
			"usage":             usage,
			"usage_observation": usageObservation,
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	shell := projection.Entries[1]
	if got, want := shell["kind"], "turn_activity"; got != want {
		t.Fatalf("usage entry should project as active turn_activity, got %#v", shell)
	}
	if got := transcriptAnyMap(shell["turnUsage"]); got["input_tokens"] != float64(100) {
		t.Fatalf("shell turnUsage = %#v, want usage payload", shell["turnUsage"])
	}
	if got := transcriptAnyMap(shell["usageObservation"]); got["usage_source"] != "thread.tokenUsage.updated" {
		t.Fatalf("shell usageObservation = %#v, want observation payload", shell["usageObservation"])
	}
	activity := transcriptAnyMap(shell["activity"])
	if got := transcriptAnyMap(activity["turnUsage"]); got["total_tokens"] != float64(125) {
		t.Fatalf("activity turnUsage = %#v, want usage payload", activity["turnUsage"])
	}
	body := projection.ActivityBodies["turn-1"]
	if got, want := body.CompactedEntryIDs, []string{"turn-usage:turn-1"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("compacted ids = %#v, want %#v", got, want)
	}
	if got := body.Entries[0]["metaKind"]; got != "turn_usage" {
		t.Fatalf("activity body metaKind = %#v, want turn_usage", got)
	}
}

func TestProjectTranscriptEventsKeepsInterruptedTurnActivityOutOfMainTranscript(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "cancel this",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("note", "003", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:note", map[string]any{
			"kind": "message",
			"text": "working on it",
		}),
		projectionTestEvent("tool", "004", "item.started", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":    "command_execution",
			"command": "sleep 30",
		}),
		projectionTestEvent("turn-1:turn.interrupt_requested", "005", "turn.interrupt_requested", "system", "tank", "turn-1", "", nil),
		projectionTestEvent("terminal", "006", "turn.interrupted", "runner", "codex", "turn-1", "", map[string]any{
			"reason": "client_interrupt",
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[0]["kind"] != "message" || projection.Entries[1]["kind"] != "turn_activity" || projection.Entries[2]["kind"] != "meta" {
		t.Fatalf("entry kinds = [%v %v %v], want message/turn_activity/meta", projection.Entries[0]["kind"], projection.Entries[1]["kind"], projection.Entries[2]["kind"])
	}
	activity := projection.Entries[1]["activity"].(map[string]any)
	if activity["status"] != "interrupted" || activity["active"] == true {
		t.Fatalf("activity summary = %#v, want interrupted inactive turn activity", activity)
	}
	if got, want := activity["childCount"], 3; got != want {
		t.Fatalf("activity childCount = %v, want %v", got, want)
	}
	for _, entry := range projection.Entries {
		if entry["id"] == "turn-1:item:note" || entry["id"] == "turn-1:item:tool" || entry["id"] == "turn-1:turn.interrupt_requested" {
			t.Fatalf("activity child leaked into main transcript: %#v", entry)
		}
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("missing activity body for interrupted turn")
	}
	if got, want := body.CompactedEntryIDs, []string{"turn-1:item:note", "turn-1:item:tool", "turn-1:turn.interrupt_requested"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("compacted ids = %#v, want %#v", got, want)
	}
}

func TestProjectTranscriptEventsKeepsFailedTurnActivityOutOfMainTranscript(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "try this",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("note", "003", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:note", map[string]any{
			"kind": "message",
			"text": "attempting",
		}),
		projectionTestEvent("tool", "004", "item.failed", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":  "command_execution",
			"error": "boom",
		}),
		projectionTestEvent("terminal", "005", "turn.failed", "runner", "codex", "turn-1", "", map[string]any{
			"reason": "provider_failure",
			"error":  "provider failed",
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[0]["kind"] != "message" || projection.Entries[1]["kind"] != "turn_activity" || projection.Entries[2]["kind"] != "meta" {
		t.Fatalf("entry kinds = [%v %v %v], want message/turn_activity/meta", projection.Entries[0]["kind"], projection.Entries[1]["kind"], projection.Entries[2]["kind"])
	}
	activity := projection.Entries[1]["activity"].(map[string]any)
	if activity["status"] != "failed" || activity["active"] == true {
		t.Fatalf("activity summary = %#v, want failed inactive turn activity", activity)
	}
	terminal := projection.Entries[2]
	if meta, ok := terminal["meta"].(map[string]any); !ok || meta["title"] != "Turn failed" {
		t.Fatalf("terminal meta = %#v, want Turn failed", terminal)
	}
	for _, entry := range projection.Entries {
		if entry["id"] == "turn-1:item:note" || entry["id"] == "turn-1:item:tool" {
			t.Fatalf("activity child leaked into main transcript: %#v", entry)
		}
	}
}

// TestProjectTranscriptEventsPromotesAskUserQuestionHandoff verifies the
// projection synthesizes a meta-kind `needs_input_announcement` row in the
// main transcript stream whenever an AskUserQuestion tool item is present,
// and that the announcement is kept OUT of the Turn-activity compact so the
// handoff stays visible whether the activity group is open or closed.
//
// Coverage:
//   - announcement row's metaKind, title, and detail map from the question text
//   - announcement.targetTurnId / targetProviderItemId / answered fields exist
//   - announcement orderKey sorts immediately after the underlying tool item
//   - the original tool item is still emitted (the question card lives in
//     Turn activity; the announcement is a separate promotion row)
//   - the announcement is excluded from terminalProjectedActivities and
//     activeProjectedActivities — same opt-out shape as a user message
func TestProjectTranscriptEventsPromotesAskUserQuestionHandoff(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "which?",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("ask-start", "002", "item.started", "tool", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"kind":  "tool",
			"name":  "AskUserQuestion",
			"title": "AskUserQuestion",
			"input": map[string]any{
				"questions": []any{
					map[string]any{
						"question":      "Which auth method?",
						"header":        "Auth",
						"multiSelect":   false,
						"allowFreeForm": true,
						"secret":        false,
						"options": []any{
							map[string]any{"label": "OAuth", "description": "Use OAuth"},
							map[string]any{"label": "API key", "description": "Use API key"},
						},
					},
				},
			},
		}),
		projectionTestEvent("ask-approval", "003", "tool.approval_requested", "tool", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"kind": "needs_input",
			"name": "AskUserQuestion",
			"input": map[string]any{
				"questions": []any{
					map[string]any{
						"question":      "Which auth method?",
						"header":        "Auth",
						"multiSelect":   false,
						"allowFreeForm": true,
						"secret":        false,
						"options": []any{
							map[string]any{"label": "OAuth", "description": "Use OAuth"},
							map[string]any{"label": "API key", "description": "Use API key"},
						},
					},
				},
			},
		}),
	}
	projection := projectTranscriptEvents(events)
	var ann map[string]any
	for _, entry := range projection.Entries {
		if entry["metaKind"] == "needs_input_announcement" {
			ann = entry
			break
		}
	}
	if ann == nil {
		t.Fatalf("expected needs_input_announcement entry, got entries: %#v", projection.Entries)
	}
	meta, _ := ann["meta"].(map[string]any)
	if meta["title"] != "Claude is waiting on you" {
		t.Errorf("announcement title = %q, want Claude is waiting on you", meta["title"])
	}
	if got := meta["detail"]; got != "Which auth method?" {
		t.Errorf("announcement detail = %q, want question text", got)
	}
	announcement, _ := ann["announcement"].(map[string]any)
	if announcement["targetTurnId"] != "turn-1" {
		t.Errorf("targetTurnId = %v, want turn-1", announcement["targetTurnId"])
	}
	if announcement["answered"] != false {
		t.Errorf("answered = %v, want false for unresolved announcement", announcement["answered"])
	}
	if announcement["questionCount"] != 1 {
		t.Errorf("questionCount = %v, want 1", announcement["questionCount"])
	}
	// Announcement orderKey must sort immediately after the tool item's
	// orderKey so historical replay and live streaming agree on placement.
	if !strings.HasSuffix(ann["orderKey"].(string), "~needs_input_announcement") {
		t.Errorf("announcement orderKey = %q, want suffix ~needs_input_announcement", ann["orderKey"])
	}
	// The full canonical question payload (with options) rides on the
	// streamed handoff row so an already-open client renders the live
	// interactive answer form off the durable cursor stream — never from a
	// one-shot /turns/{id}/activity fetch. See docs/features/transcript/contract.md.
	questions, ok := announcement["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("announcement.questions = %#v, want 1 embedded question", announcement["questions"])
	}
	q0, _ := questions[0].(map[string]any)
	if q0["question"] != "Which auth method?" {
		t.Errorf("embedded question text = %v, want Which auth method?", q0["question"])
	}
	if q0["allowFreeForm"] != true {
		t.Errorf("embedded allowFreeForm = %v, want true", q0["allowFreeForm"])
	}
	opts, ok := q0["options"].([]any)
	if !ok || len(opts) != 2 {
		t.Fatalf("embedded options = %#v, want 2 options so the live form can render answer buttons", q0["options"])
	}
	// Unresolved announcements carry no answers map.
	if _, hasAnswers := announcement["answers"]; hasAnswers {
		t.Errorf("unresolved announcement must not carry answers, got %#v", announcement["answers"])
	}
}

// TestProjectTranscriptEventsAnnouncementAnsweredAfterResolution mirrors the
// behavior the SPA depends on for historical context: after the user
// answers, the announcement row keeps rendering with the "Answered" title
// so a scroll-back through chat history shows where Claude paused and
// that the user responded. The answered flag must come from durable
// projection state (item.status == "completed" after the resolved event),
// not from any in-flight React flag.
func TestProjectTranscriptEventsAnnouncementAnsweredAfterResolution(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "decide",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("ask-approval", "002", "tool.approval_requested", "tool", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"kind": "needs_input",
			"name": "AskUserQuestion",
			"input": map[string]any{
				"questions": []any{
					map[string]any{
						"question":      "Pick one",
						"allowFreeForm": true,
					},
				},
			},
		}),
		projectionTestEvent("ask-resolved", "003", "tool.approval_resolved", "tool", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"kind":     "needs_input",
			"resolved": true,
			"answers":  map[string]any{"Pick one": []any{"A"}},
		}),
	}
	projection := projectTranscriptEvents(events)
	for _, entry := range projection.Entries {
		if entry["metaKind"] == "needs_input_announcement" {
			meta := entry["meta"].(map[string]any)
			if meta["title"] != "Answered" {
				t.Errorf("answered announcement title = %q, want Answered", meta["title"])
			}
			ann := entry["announcement"].(map[string]any)
			if ann["answered"] != true {
				t.Errorf("answered = %v, want true after tool.approval_resolved", ann["answered"])
			}
			// The durable answer is mirrored onto the streamed handoff so an
			// open client and any fresh tab render identical selections live,
			// without the one-shot activity fetch.
			answers, ok := ann["answers"].(map[string]any)
			if !ok {
				t.Fatalf("resolved announcement.answers = %#v, want durable answers map", ann["answers"])
			}
			picked, ok := answers["Pick one"].(map[string]any)
			if !ok {
				t.Fatalf("answers[Pick one] = %#v, want answer record", answers["Pick one"])
			}
			labels, ok := picked["labels"].([]string)
			if !ok || len(labels) != 1 || labels[0] != "A" {
				t.Errorf("answers[Pick one].labels = %#v, want [A]", picked["labels"])
			}
			return
		}
	}
	t.Fatalf("missing needs_input_announcement entry after resolution: %#v", projection.Entries)
}

func projectionTestEvent(eventID, orderKey, eventType, actor, source, turnID, timelineID string, payload map[string]any) map[string]any {
	event := map[string]any{
		"event_id":   eventID,
		"order_key":  orderKey,
		"session_id": "63",
		"actor":      actor,
		"source":     source,
		"type":       eventType,
		"created_at": "2026-05-25T00:00:00Z",
		"visibility": "durable",
	}
	if turnID != "" {
		event["turn_id"] = turnID
	}
	if timelineID != "" {
		event["timeline_id"] = timelineID
	}
	if eventType == "user_message.created" || eventType == "turn.submitted" {
		event["client_nonce"] = turnID
	}
	if payload != nil {
		event["payload"] = payload
	}
	return event
}

func projectionFinalAnswerPayload(timelineIDs ...string) map[string]any {
	return map[string]any{
		"final_answer": map[string]any{
			"timeline_ids": timelineIDs,
		},
	}
}
