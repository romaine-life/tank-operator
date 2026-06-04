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

func TestProjectTranscriptEventsSubmittedOnlyTurnGetsActiveShell(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "please investigate",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	shell := projection.Entries[1]
	if shell["kind"] != "turn_activity" {
		t.Fatalf("second entry kind = %v, want turn_activity: %#v", shell["kind"], projection.Entries)
	}
	activity := shell["activity"].(map[string]any)
	if activity["active"] != true || activity["status"] != "active" {
		t.Fatalf("activity summary = %#v, want active shell", activity)
	}
	if activity["startOrderKey"] != "002" || activity["endOrderKey"] != "002" {
		t.Fatalf("activity order keys = %#v, want submitted key only", activity)
	}
	if body := projection.ActivityBodies["turn-1"]; body.Status != "active" || len(body.Entries) != 0 {
		t.Fatalf("activity body = %#v, want active shell without synthetic progress children", body)
	}
}

func TestProjectTranscriptEventsClaimedTurnAdvancesActiveShell(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "please investigate",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("claimed", "003", "turn.claimed", "runner", "claude", "turn-1", "", nil),
	}

	projection := projectTranscriptEvents(events)
	shell := projection.Entries[1]
	if shell["kind"] != "turn_activity" {
		t.Fatalf("second entry kind = %v, want turn_activity: %#v", shell["kind"], projection.Entries)
	}
	activity := shell["activity"].(map[string]any)
	if activity["startOrderKey"] != "002" || activity["endOrderKey"] != "003" {
		t.Fatalf("activity order keys = %#v, want submitted through claimed", activity)
	}
	if body := projection.ActivityBodies["turn-1"]; body.Status != "active" || len(body.Entries) != 0 {
		t.Fatalf("activity body = %#v, want active shell without synthetic progress children", body)
	}
}

func TestProjectTranscriptEventsRecordsContextCompactedAsTurnActivity(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "do a long task",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool-start", "002", "item.started", "tool", "claude", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":  "tool",
			"title": "Bash",
		}),
		projectionTestEvent("compact", "003", "context.compacted", "runner", "claude", "turn-1", "", map[string]any{
			"trigger":    "auto",
			"pre_tokens": float64(158000),
		}),
		projectionTestEvent("tool-done", "004", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":   "tool_result",
			"output": "ok",
		}),
		projectionTestEvent("final", "005", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:msg-1", map[string]any{
			"kind": "message",
			"text": "done",
		}),
		projectionTestEvent("terminal", "006", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:msg-1")),
	}

	projection := projectTranscriptEvents(events)

	// Context compaction is intra-turn noise, not settled conversation: it must
	// NOT appear as a top-level settled transcript row. A standalone
	// context_compacted row in projection.Entries is the promotion bug that made
	// it flash-then-vanish on the turn-detail screen (it showed pre-load, then
	// dropped when the turn's activity children loaded, which excluded it).
	for _, entry := range projection.Entries {
		if entry["metaKind"] == "context_compacted" {
			t.Fatalf("context.compacted leaked into the settled transcript as a top-level row: %#v", entry)
		}
	}

	// It must live in the turn's Turn-activity body, folded into the collapsed
	// shell like any other non-final-answer activity row, and render there as the
	// existing meta system note.
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("turn-1 has no activity body: %#v", projection.ActivityBodies)
	}
	var notice map[string]any
	for _, entry := range body.Entries {
		if entry["metaKind"] == "context_compacted" {
			notice = entry
			break
		}
	}
	if notice == nil {
		t.Fatalf("context.compacted was not recorded as a turn-activity child: %#v", body.Entries)
	}
	if notice["kind"] != "meta" {
		t.Fatalf("context compacted notice kind = %v, want meta", notice["kind"])
	}
	meta, ok := notice["meta"].(map[string]any)
	if !ok || meta["title"] != "Context compacted" {
		t.Fatalf("context compacted meta = %#v, want title 'Context compacted'", notice["meta"])
	}
	if detail, _ := meta["detail"].(string); !strings.Contains(detail, "158k") {
		t.Fatalf("context compacted detail = %q, want compact token count", meta["detail"])
	}

	// Folded into the shell: its id must be among the turn's compacted entry ids
	// so it is collapsed out of the settled transcript and only revealed when the
	// Turn-activity disclosure loads. AskUserQuestion differs only in that its
	// card stays in Turn activity for the same reason — both are turn noise.
	noticeID, _ := notice["id"].(string)
	compacted := false
	for _, id := range body.CompactedEntryIDs {
		if id == noticeID {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatalf("context compacted notice %q was not folded into the turn-activity shell: %#v", noticeID, body.CompactedEntryIDs)
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
	firstUsage := map[string]any{
		"input_tokens":  float64(100),
		"output_tokens": float64(25),
		"total_tokens":  float64(125),
	}
	latestUsage := map[string]any{
		"input_tokens":  float64(120),
		"output_tokens": float64(30),
		"total_tokens":  float64(150),
	}
	usageObservation := map[string]any{
		"usage_source":     "thread.tokenUsage.updated",
		"provider_turn_id": "provider-turn-1",
		"update_count":     float64(2),
	}
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "think for a while",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("started", "003", "turn.started", "runner", "codex", "turn-1", "", nil),
		projectionTestEvent("usage-1", "004", "turn.usage", "runner", "codex", "turn-1", "", map[string]any{
			"usage": firstUsage,
			"usage_observation": map[string]any{
				"usage_source":     "thread.tokenUsage.updated",
				"provider_turn_id": "provider-turn-1",
				"update_count":     float64(1),
			},
		}),
		projectionTestEvent("tool", "005", "item.started", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":    "command_execution",
			"command": "go test ./...",
		}),
		projectionTestEvent("usage-2", "006", "turn.usage", "runner", "codex", "turn-1", "", map[string]any{
			"usage":             latestUsage,
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
	if got := transcriptAnyMap(shell["turnUsage"]); got["input_tokens"] != float64(120) {
		t.Fatalf("shell turnUsage = %#v, want usage payload", shell["turnUsage"])
	}
	if got := transcriptAnyMap(shell["usageObservation"]); got["usage_source"] != "thread.tokenUsage.updated" {
		t.Fatalf("shell usageObservation = %#v, want observation payload", shell["usageObservation"])
	}
	activity := transcriptAnyMap(shell["activity"])
	if got := transcriptAnyMap(activity["turnUsage"]); got["total_tokens"] != float64(150) {
		t.Fatalf("activity turnUsage = %#v, want usage payload", activity["turnUsage"])
	}
	if got := activity["endOrderKey"]; got != "006" {
		t.Fatalf("activity endOrderKey = %#v, want latest usage order key", got)
	}
	body := projection.ActivityBodies["turn-1"]
	if got, want := body.CompactedEntryIDs, []string{"turn-usage:turn-1", "turn-1:item:tool"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("compacted ids = %#v, want %#v", got, want)
	}
	if got := body.Entries[0]["metaKind"]; got != "turn_usage" {
		t.Fatalf("activity body metaKind = %#v, want turn_usage", got)
	}
	if got := body.Entries[0]["orderKey"]; got != "004" {
		t.Fatalf("usage row orderKey = %#v, want first usage order key", got)
	}
	if got := body.Entries[0]["activityEndOrderKey"]; got != "006" {
		t.Fatalf("usage row activityEndOrderKey = %#v, want latest usage order key", got)
	}
	if got := transcriptAnyMap(body.Entries[0]["turnUsage"]); got["total_tokens"] != float64(150) {
		t.Fatalf("usage row turnUsage = %#v, want latest usage payload", body.Entries[0]["turnUsage"])
	}
	if got := body.Entries[1]["id"]; got != "turn-1:item:tool" {
		t.Fatalf("second activity entry id = %#v, want tool after anchored usage row", got)
	}
}

func TestProjectTranscriptEventsKeepsClaudeUsageSnapshotThroughTerminal(t *testing.T) {
	// Claude per-message snapshots (usage_source=claude.message) are the
	// context-occupancy signal; the terminal carries CUMULATIVE usage
	// (claude.result), which sums cache reads across the turn's tool loop.
	// The terminal annotation must NOT overwrite the dedicated turn_usage row
	// with the cumulative usage — doing so collapses the context gauge to ~0
	// once a turn completes and on every reload.
	snapshotLatest := map[string]any{
		"input_tokens":                float64(2),
		"cache_read_input_tokens":     float64(540_000),
		"cache_creation_input_tokens": float64(800),
		"output_tokens":               float64(120),
	}
	cumulativeTerminal := map[string]any{
		"input_tokens":                float64(266),
		"cache_read_input_tokens":     float64(3_219_249),
		"cache_creation_input_tokens": float64(21_332),
		"output_tokens":               float64(19_380),
	}
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "do the thing",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("usage-1", "003", "turn.usage", "runner", "claude", "turn-1", "", map[string]any{
			"usage": map[string]any{
				"input_tokens":                float64(2),
				"cache_read_input_tokens":     float64(100_000),
				"cache_creation_input_tokens": float64(500),
			},
			"usage_observation": map[string]any{"usage_source": "claude.message", "terminal_had_usage": false},
		}),
		projectionTestEvent("answer", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:answer", map[string]any{
			"kind": "message",
			"text": "all done",
		}),
		projectionTestEvent("usage-2", "005", "turn.usage", "runner", "claude", "turn-1", "", map[string]any{
			"usage":             snapshotLatest,
			"usage_observation": map[string]any{"usage_source": "claude.message", "terminal_had_usage": false},
		}),
		projectionTestEvent("terminal", "006", "turn.completed", "runner", "claude", "turn-1", "", map[string]any{
			"usage":             cumulativeTerminal,
			"usage_observation": map[string]any{"usage_source": "claude.result", "terminal_had_usage": true},
			"final_answer":      map[string]any{"timeline_ids": []any{"turn-1:item:answer"}},
		}),
	}

	projection := projectTranscriptEvents(events)
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("expected turn-1 activity body, got %#v", projection.ActivityBodies)
	}
	var usageRow map[string]any
	for _, entry := range body.Entries {
		if entry["metaKind"] == "turn_usage" {
			usageRow = entry
			break
		}
	}
	if usageRow == nil {
		t.Fatalf("expected a turn_usage row in the activity body, got %#v", body.Entries)
	}
	// The snapshot survives the terminal annotation: latest per-message usage,
	// not the cumulative terminal.
	if got := transcriptAnyMap(usageRow["turnUsage"]); got["cache_read_input_tokens"] != float64(540_000) {
		t.Fatalf("usage row turnUsage = %#v, want latest snapshot (cache_read 540000), not the cumulative terminal", usageRow["turnUsage"])
	}
	if got := transcriptAnyMap(usageRow["usageObservation"]); got["usage_source"] != "claude.message" {
		t.Fatalf("usage row usageObservation = %#v, want claude.message, not clobbered to claude.result", usageRow["usageObservation"])
	}

	// The compacted activity shell is the row the session-level context gauge
	// scans (the turn_usage row is folded into it). It must surface the
	// snapshot occupancy, not the cumulative terminal, or a completed Claude
	// turn reads ~0 occupancy.
	var shell map[string]any
	for _, entry := range projection.Entries {
		if entry["kind"] == "turn_activity" && entry["turnId"] == "turn-1" {
			shell = entry
			break
		}
	}
	if shell == nil {
		t.Fatalf("expected a turn_activity shell for turn-1, got %#v", projection.Entries)
	}
	if got := transcriptAnyMap(shell["turnUsage"]); got["cache_read_input_tokens"] != float64(540_000) {
		t.Fatalf("shell turnUsage = %#v, want latest snapshot (cache_read 540000), not the cumulative terminal", shell["turnUsage"])
	}
	if got := transcriptAnyMap(shell["usageObservation"]); got["usage_source"] != "claude.message" {
		t.Fatalf("shell usageObservation = %#v, want claude.message, not the cumulative terminal observation", shell["usageObservation"])
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

// TestProjectTranscriptEventsPromotesAwaitingInputCard verifies the projection
// places a turn.awaiting_input pause inside Turn activity as the interactive
// question card (metaKind "awaiting_input"), anchored at the asking turn's tail,
// and unanswered until a later turn.input_answered arrives. The main transcript
// gets only the Turn activity shell.
func TestProjectTranscriptEventsPromotesAwaitingInputCard(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "which?",
			"display": map[string]any{"kind": "plain"},
		}),
		// The asking turn pauses here: turn.awaiting_input carries the
		// Tank-canonical questions and the AUQ item's ids.
		projectionTestEvent("await", "002", "turn.awaiting_input", "runner", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"timeline_id":      "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{
					"question":      "Which auth method?",
					"header":        "Auth",
					"multiSelect":   false,
					"allowFreeForm": true,
					"options": []any{
						map[string]any{"label": "OAuth", "description": "Use OAuth"},
						map[string]any{"label": "API key", "description": "Use API key"},
					},
				},
			},
		}),
	}
	projection := projectTranscriptEvents(events)
	var card map[string]any
	for _, body := range projection.ActivityBodies {
		if body.TurnID != "turn-1" {
			continue
		}
		for _, entry := range body.Entries {
			if entry["metaKind"] == "awaiting_input" {
				card = entry
				break
			}
		}
	}
	if card == nil {
		t.Fatalf("expected awaiting_input card in activity body, got bodies: %#v", projection.ActivityBodies)
	}
	for _, entry := range projection.Entries {
		if entry["metaKind"] == "awaiting_input" {
			t.Fatalf("awaiting_input card leaked into main transcript: %#v", entry)
		}
	}
	if shell := projection.Entries[1]; shell["kind"] != "turn_activity" {
		t.Fatalf("main transcript entry = %#v, want turn_activity shell", shell)
	}
	meta, _ := card["meta"].(map[string]any)
	if meta["title"] != "Claude is waiting on you" {
		t.Errorf("card title = %q, want Claude is waiting on you", meta["title"])
	}
	if got := meta["detail"]; got != "Which auth method?" {
		t.Errorf("card detail = %q, want question text", got)
	}
	if card["turnId"] != "turn-1" {
		t.Errorf("turnId = %v, want turn-1 (the asking turn)", card["turnId"])
	}
	awaiting, _ := card["awaitingInput"].(map[string]any)
	if awaiting["askingTurnId"] != "turn-1" {
		t.Errorf("askingTurnId = %v, want turn-1", awaiting["askingTurnId"])
	}
	if awaiting["providerItemId"] != "toolu_ask" {
		t.Errorf("providerItemId = %v, want toolu_ask", awaiting["providerItemId"])
	}
	if awaiting["timelineId"] != "turn-1:item:tool-ask" {
		t.Errorf("timelineId = %v, want turn-1:item:tool-ask", awaiting["timelineId"])
	}
	if awaiting["answered"] != false {
		t.Errorf("answered = %v, want false for an unanswered card", awaiting["answered"])
	}
	if awaiting["questionCount"] != 1 {
		t.Errorf("questionCount = %v, want 1", awaiting["questionCount"])
	}
	// Card orderKey must sort immediately after the asking turn's tail so
	// historical replay and live streaming agree on placement.
	if !strings.HasSuffix(card["orderKey"].(string), "~awaiting_input") {
		t.Errorf("card orderKey = %q, want suffix ~awaiting_input", card["orderKey"])
	}
}

// TestProjectTranscriptEventsAwaitingInputAnsweredBySameTurnEvent proves the
// card's "answered" state is derived from durable state — a later
// turn.input_answered event whose question_timeline_id matches the pause — not a
// browser-local flag. A fresh tab opened after the user answered renders the
// resolved card.
func TestProjectTranscriptEventsAwaitingInputAnsweredBySameTurnEvent(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "decide",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("await", "002", "turn.awaiting_input", "runner", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"timeline_id":      "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{"question": "Pick one", "allowFreeForm": true},
			},
		}),
		// The user's answer is recorded on the same turn and links back to the
		// paused question's timeline id.
		projectionTestEvent("ans", "003", "turn.input_answered", "user", "tank", "turn-1", "turn-1:item:tool-ask:answer", map[string]any{
			"question_timeline_id": "turn-1:item:tool-ask",
			"provider_item_id":     "toolu_ask",
			"answers":              map[string]any{"Pick one": []any{"A"}},
		}),
	}
	projection := projectTranscriptEvents(events)
	for _, body := range projection.ActivityBodies {
		for _, entry := range body.Entries {
			if entry["metaKind"] != "awaiting_input" {
				continue
			}
			meta := entry["meta"].(map[string]any)
			if meta["title"] != "Answered" {
				t.Errorf("answered card title = %q, want Answered", meta["title"])
			}
			awaiting := entry["awaitingInput"].(map[string]any)
			if awaiting["answered"] != true {
				t.Errorf("answered = %v, want true after turn.input_answered", awaiting["answered"])
			}
			answers := awaiting["answers"].(map[string]any)
			if got := answers["Pick one"].([]any)[0]; got != "A" {
				t.Errorf("answers = %#v, want Pick one=A", answers)
			}
			return
		}
	}
	t.Fatalf("missing awaiting_input card: %#v", projection.ActivityBodies)
}

func TestProjectTranscriptEventsPopulatesActivityBodiesForTurnsWithoutCompactedEntries(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "hello",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("final", "002", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:final", map[string]any{
			"kind": "agent_message",
			"text": "hi there",
		}),
		projectionTestEvent("terminal", "003", "turn.completed", "runner", "codex", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:final")),
	}

	projection := projectTranscriptEvents(events)

	// We expect the main timeline entries to NOT contain a turn_activity shell.
	// So we should have exactly 2 entries (user message and assistant message).
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got, want := projection.Entries[0]["kind"], "message"; got != want {
		t.Fatalf("first entry kind = %v, want %s", got, want)
	}
	if got, want := projection.Entries[1]["kind"], "message"; got != want {
		t.Fatalf("second entry kind = %v, want %s", got, want)
	}

	// We expect the activity body for the turn to be populated.
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatal("missing activity body for turn-1")
	}
	if got, want := len(body.Entries), 1; got != want {
		t.Fatalf("activity body entries = %d, want %d", got, want)
	}
	if got, want := body.Entries[0]["id"], "turn-1:item:final"; got != want {
		t.Fatalf("activity body entry id = %v, want %s", got, want)
	}
	if got, want := len(body.CompactedEntryIDs), 0; got != want {
		t.Fatalf("compacted ids count = %d, want %d", got, want)
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
