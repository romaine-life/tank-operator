package main

import "testing"

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

func TestProjectTranscriptEventsCompactsInterruptedTurnIntoActivityShell(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "deep dive please",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("note", "002", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:note", map[string]any{
			"kind": "message",
			"text": "Time-check before I rabbit-hole further.",
		}),
		projectionTestEvent("tool-start", "003", "item.started", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "command_execution",
			"name": "Bash",
		}),
		projectionTestEvent("tool-done", "004", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":   "command_execution",
			"output": "ok",
		}),
		projectionTestEvent("interrupt-req", "005", "turn.interrupt_requested", "runner", "tank", "turn-1", "", map[string]any{}),
		projectionTestEvent("terminal", "006", "turn.interrupted", "runner", "tank", "turn-1", "", map[string]any{}),
	}

	projection := projectTranscriptEvents(events)

	kinds := projectedEntryKindList(projection.Entries)
	want := []string{"message", "turn_activity", "meta", "meta"}
	if !stringSlicesEqual(kinds, want) {
		t.Fatalf("entry kinds = %#v, want %#v: %#v", kinds, want, projection.Entries)
	}
	if got, want := projection.Entries[0]["role"], "user"; got != want {
		t.Fatalf("first entry role = %v, want %v", got, want)
	}
	shell := projection.Entries[1]
	activity, ok := shell["activity"].(map[string]any)
	if !ok {
		t.Fatalf("interrupted turn activity shell missing summary: %#v", shell)
	}
	if got, want := activity["status"], "interrupted"; got != want {
		t.Fatalf("activity status = %v, want %v", got, want)
	}
	if activity["active"] == true {
		t.Fatalf("interrupted activity rendered active: %#v", activity)
	}
	if got, want := activity["toolCount"], 1; got != want {
		t.Fatalf("activity toolCount = %v, want %v", got, want)
	}
	if got, want := activity["progressNoteCount"], 1; got != want {
		t.Fatalf("activity progressNoteCount = %v, want %v: assistant prose must live inside the shell", got, want)
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatal("missing activity body for interrupted turn")
	}
	if got, want := len(body.CompactedEntryIDs), 2; got != want {
		t.Fatalf("compacted ids count = %d, want %d: %#v", got, want, body.CompactedEntryIDs)
	}
	for _, id := range body.CompactedEntryIDs {
		if id != "turn-1:item:note" && id != "turn-1:item:tool" {
			t.Fatalf("unexpected compacted id %q; assistant prose and tool must be compacted but meta entries must remain visible", id)
		}
	}
	interruptRequestedMeta := projection.Entries[2]
	if got, want := transcriptMapString(transcriptMap(interruptRequestedMeta, "meta"), "title"), "Stop requested"; got != want {
		t.Fatalf("third entry meta.title = %v, want %v", got, want)
	}
	stopped := projection.Entries[3]
	if got, want := transcriptMapString(transcriptMap(stopped, "meta"), "title"), "Stopped"; got != want {
		t.Fatalf("fourth entry meta.title = %v, want %v", got, want)
	}
	if got, want := transcriptMapString(stopped, "turnTerminalStatus"), "interrupted"; got != want {
		t.Fatalf("terminal annotation = %v, want %v", got, want)
	}
}

func TestProjectTranscriptEventsCompactsFailedTurnIntoActivityShell(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "try this",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool-start", "002", "item.started", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "command_execution",
			"name": "Bash",
		}),
		projectionTestEvent("tool-done", "003", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":   "command_execution",
			"output": "ok",
		}),
		projectionTestEvent("terminal", "004", "turn.failed", "runner", "tank", "turn-1", "", map[string]any{
			"error": map[string]any{"message": "provider returned 500"},
		}),
	}

	projection := projectTranscriptEvents(events)

	kinds := projectedEntryKindList(projection.Entries)
	want := []string{"message", "turn_activity", "meta"}
	if !stringSlicesEqual(kinds, want) {
		t.Fatalf("entry kinds = %#v, want %#v: %#v", kinds, want, projection.Entries)
	}
	shell := projection.Entries[1]
	activity, ok := shell["activity"].(map[string]any)
	if !ok {
		t.Fatalf("failed turn activity shell missing summary: %#v", shell)
	}
	if got, want := activity["status"], "failed"; got != want {
		t.Fatalf("activity status = %v, want %v", got, want)
	}
	if activity["active"] == true {
		t.Fatalf("failed activity rendered active: %#v", activity)
	}
	if got, want := activity["toolCount"], 1; got != want {
		t.Fatalf("activity toolCount = %v, want %v", got, want)
	}
	failedMeta := projection.Entries[2]
	if got, want := transcriptMapString(transcriptMap(failedMeta, "meta"), "title"), "Turn failed"; got != want {
		t.Fatalf("trailing meta title = %v, want %v", got, want)
	}
	if got, want := transcriptMapString(transcriptMap(failedMeta, "meta"), "detail"), "provider returned 500"; got != want {
		t.Fatalf("trailing meta detail = %v, want %v", got, want)
	}
}

func TestProjectTranscriptEventsSkipsActivityShellForBareInterruptedTurn(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "stop immediately",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("interrupt-req", "002", "turn.interrupt_requested", "runner", "tank", "turn-1", "", map[string]any{}),
		projectionTestEvent("terminal", "003", "turn.interrupted", "runner", "tank", "turn-1", "", map[string]any{}),
	}

	projection := projectTranscriptEvents(events)

	for _, entry := range projection.Entries {
		if transcriptMapString(entry, "kind") == "turn_activity" {
			t.Fatalf("bare interrupted turn produced empty activity shell: %#v", projection.Entries)
		}
	}
	if _, ok := projection.ActivityBodies["turn-1"]; ok {
		t.Fatal("bare interrupted turn produced an activity body with no compacted children")
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
	if projection.Entries[0]["id"] == "turn-1:item:note" {
		t.Fatalf("active assistant prose rendered as settled transcript row")
	}
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

func projectedEntryKindList(entries []map[string]any) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, transcriptMapString(entry, "kind"))
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
