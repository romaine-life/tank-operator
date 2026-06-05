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

func TestProjectTranscriptEventsProjectsTurnUsageForContextChip(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "think for a while",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("started", "003", "turn.started", "runner", "codex", "turn-1", "", nil),
		projectionTestEvent("usage-1", "004", "turn.usage", "runner", "codex", "turn-1", "", map[string]any{
			"usage": map[string]any{
				"input_tokens":  float64(100),
				"output_tokens": float64(25),
				"total_tokens":  float64(125),
			},
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
			"usage": map[string]any{
				"input_tokens":  float64(120),
				"output_tokens": float64(30),
				"total_tokens":  float64(150),
			},
			"usage_observation": map[string]any{
				"usage_source":     "thread.tokenUsage.updated",
				"provider_turn_id": "provider-turn-1",
				"update_count":     float64(2),
			},
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	shell := projection.Entries[1]
	if got, want := shell["kind"], "turn_activity"; got != want {
		t.Fatalf("active tool should project as turn_activity, got %#v", shell)
	}
	activity := transcriptAnyMap(shell["activity"])
	shellUsage := transcriptAnyMap(shell["turnUsage"])
	if got, want := shellUsage["input_tokens"], float64(120); got != want {
		t.Fatalf("shell turnUsage input_tokens = %#v, want %#v in %#v", got, want, shell)
	}
	shellObservation := transcriptAnyMap(shell["usageObservation"])
	if got, want := shellObservation["update_count"], float64(2); got != want {
		t.Fatalf("shell usageObservation update_count = %#v, want %#v in %#v", got, want, shell)
	}
	activityUsage := transcriptAnyMap(activity["turnUsage"])
	if got, want := activityUsage["input_tokens"], float64(120); got != want {
		t.Fatalf("activity turnUsage input_tokens = %#v, want %#v in %#v", got, want, activity)
	}
	activityObservation := transcriptAnyMap(activity["usageObservation"])
	if got, want := activityObservation["usage_source"], "thread.tokenUsage.updated"; got != want {
		t.Fatalf("activity usageObservation source = %#v, want %#v in %#v", got, want, activity)
	}
	body := projection.ActivityBodies["turn-1"]
	var foundUsageRow bool
	for _, entry := range body.Entries {
		if entry["metaKind"] != "turn_usage" {
			continue
		}
		foundUsageRow = true
		usage := transcriptAnyMap(entry["turnUsage"])
		if got, want := usage["input_tokens"], float64(120); got != want {
			t.Fatalf("usage row input_tokens = %#v, want %#v in %#v", got, want, entry)
		}
	}
	if !foundUsageRow {
		t.Fatalf("activity body missing hidden turn_usage row: %#v", body.Entries)
	}
}

func TestProjectTranscriptEventsKeepsSnapshotAndTerminalUsageDistinct(t *testing.T) {
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
			"usage": map[string]any{
				"input_tokens":                float64(2),
				"cache_read_input_tokens":     float64(540_000),
				"cache_creation_input_tokens": float64(800),
				"output_tokens":               float64(120),
			},
			"usage_observation": map[string]any{"usage_source": "claude.message", "terminal_had_usage": false},
		}),
		projectionTestEvent("terminal", "006", "turn.completed", "runner", "claude", "turn-1", "", map[string]any{
			"usage": map[string]any{
				"input_tokens":                float64(266),
				"cache_read_input_tokens":     float64(3_219_249),
				"cache_creation_input_tokens": float64(21_332),
				"output_tokens":               float64(19_380),
			},
			"usage_observation": map[string]any{"usage_source": "claude.result", "terminal_had_usage": true},
			"final_answer":      map[string]any{"timeline_ids": []any{"turn-1:item:answer"}},
		}),
	}

	projection := projectTranscriptEvents(events)
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("expected turn-1 activity body, got %#v", projection.ActivityBodies)
	}
	var snapshot map[string]any
	for _, entry := range body.Entries {
		if entry["metaKind"] == "turn_usage" {
			snapshot = entry
			break
		}
	}
	if snapshot == nil {
		t.Fatalf("activity body missing hidden usage snapshot row: %#v", body.Entries)
	}
	snapshotObservation := transcriptAnyMap(snapshot["usageObservation"])
	if got, want := snapshotObservation["usage_source"], "claude.message"; got != want {
		t.Fatalf("snapshot source = %#v, want %#v in %#v", got, want, snapshot)
	}
	snapshotUsage := transcriptAnyMap(snapshot["turnUsage"])
	if got, want := snapshotUsage["cache_read_input_tokens"], float64(540_000); got != want {
		t.Fatalf("snapshot cache_read_input_tokens = %#v, want %#v in %#v", got, want, snapshot)
	}
	var terminalAnnotated map[string]any
	for _, entry := range projection.Entries {
		if entry["id"] == "turn-1:item:answer" {
			terminalAnnotated = entry
			break
		}
	}
	if terminalAnnotated == nil {
		t.Fatalf("projected entries missing final answer: %#v", projection.Entries)
	}
	terminalObservation := transcriptAnyMap(terminalAnnotated["usageObservation"])
	if got, want := terminalObservation["usage_source"], "claude.result"; got != want {
		t.Fatalf("terminal source = %#v, want %#v in %#v", got, want, terminalAnnotated)
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

// TestProjectTranscriptEventsPromotesAskUserQuestionMessage verifies the
// projection renders the derived assistant question message as the terminal
// main-transcript response while the durable turn.awaiting_input card belongs
// to the separate question turn.
func TestProjectTranscriptEventsPromotesAwaitingInputCard(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "which?",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("invoke", "002", "turn.awaiting_input.invocation", "runner", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
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
		projectionTestEvent("msg", "003", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Which auth method?",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-2",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          "turn-2:item:tool-ask",
				"provider_timeline_id": "turn-1:item:tool-ask",
				"questions": []any{
					map[string]any{"question": "Which auth method?", "allowFreeForm": true},
				},
			},
		}),
		projectionTestEvent("await", "004", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:tool-ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:tool-ask",
			"provider_timeline_id": "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{"question": "Which auth method?", "allowFreeForm": true},
			},
		}),
	}
	projection := projectTranscriptEvents(events)
	var card map[string]any
	for _, body := range projection.ActivityBodies {
		if body.TurnID != "turn-2" {
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
		t.Fatalf("expected awaiting_input question payload in activity body, got bodies: %#v", projection.ActivityBodies)
	}
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want user + assistant question: %#v", got, projection.Entries)
	}
	if projection.Entries[1]["kind"] != "message" || projection.Entries[1]["role"] != "assistant" {
		t.Fatalf("second transcript entry = %#v, want assistant question message", projection.Entries[1])
	}
	if projection.Entries[1]["text"] != "1. Which auth method?" {
		t.Fatalf("assistant question text = %q", projection.Entries[1]["text"])
	}
	meta, _ := card["meta"].(map[string]any)
	if meta["title"] != "I need your input" {
		t.Errorf("card title = %q, want I need your input", meta["title"])
	}
	if got := meta["detail"]; got != "Which auth method?" {
		t.Errorf("card detail = %q, want question text", got)
	}
	if card["turnId"] != "turn-2" {
		t.Errorf("turnId = %v, want turn-2 (the question turn)", card["turnId"])
	}
	awaiting, _ := card["awaitingInput"].(map[string]any)
	if awaiting["askingTurnId"] != "turn-1" {
		t.Errorf("askingTurnId = %v, want turn-1", awaiting["askingTurnId"])
	}
	if awaiting["questionTurnId"] != "turn-2" {
		t.Errorf("questionTurnId = %v, want turn-2", awaiting["questionTurnId"])
	}
	if awaiting["providerItemId"] != "toolu_ask" {
		t.Errorf("providerItemId = %v, want toolu_ask", awaiting["providerItemId"])
	}
	if awaiting["timelineId"] != "turn-2:item:tool-ask" {
		t.Errorf("timelineId = %v, want turn-2:item:tool-ask", awaiting["timelineId"])
	}
	if awaiting["answered"] != false {
		t.Errorf("answered = %v, want false for an unanswered question set", awaiting["answered"])
	}
	if awaiting["questionCount"] != 1 {
		t.Errorf("questionCount = %v, want 1", awaiting["questionCount"])
	}
}

func TestProjectTranscriptEventsAwaitingInputButtonSortsAfterFoldedStartupStatus(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "invoke the ask question tool",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("loading", "002", "session.status", "system", "tank", "", "session:loading", map[string]any{
			"status": "loading",
			"text":   "Session is loading.",
		}),
		projectionTestEvent("ready", "003", "session.status", "system", "tank", "", "session:ready", map[string]any{
			"status": "ready",
			"text":   "Session is ready.",
		}),
		projectionTestEvent("msg", "004", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Which option?",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-2",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          "turn-2:item:tool-ask",
				"provider_timeline_id": "turn-1:item:tool-ask",
				"questions":            []any{map[string]any{"question": "Which option?", "allowFreeForm": true}},
			},
		}),
		projectionTestEvent("await", "005", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:tool-ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:tool-ask",
			"provider_timeline_id": "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{"question": "Which option?", "allowFreeForm": true},
			},
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[0]["kind"] != "message" {
		t.Fatalf("first entry = %#v, want user message", projection.Entries[0])
	}
	if projection.Entries[1]["role"] != "assistant" {
		t.Fatalf("second entry = %#v, want assistant question message", projection.Entries[1])
	}
	for _, entry := range projection.Entries {
		if isProjectionSessionStatus(entry) {
			t.Fatalf("startup status leaked into main transcript: %#v", entry)
		}
	}
}

// TestProjectTranscriptEventsAwaitingInputAnsweredBySameTurnEvent proves the
// question set's "answered" state is derived from durable state — a later
// turn.input_answered event whose question_timeline_id matches the pause — not a
// browser-local flag. A fresh tab opened after the user answered renders the
// resolved question set.
func TestProjectTranscriptEventsAwaitingInputAnsweredBySameTurnEvent(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "decide",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("msg", "002", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Pick one",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-2",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          "turn-2:item:tool-ask",
				"provider_timeline_id": "turn-1:item:tool-ask",
				"questions":            []any{map[string]any{"question": "Pick one", "allowFreeForm": true}},
			},
		}),
		projectionTestEvent("await", "003", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:tool-ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:tool-ask",
			"provider_timeline_id": "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{"question": "Pick one", "allowFreeForm": true},
			},
		}),
		// The question-set answer marker links back to the question's timeline
		// id; the visible answer text is a separate user submission.
		projectionTestEvent("ans", "004", "turn.input_answered", "user", "tank", "turn-2", "turn-2:item:tool-ask:answer", map[string]any{
			"question_timeline_id": "turn-2:item:tool-ask",
			"provider_item_id":     "toolu_ask",
			"answers":              map[string]any{"Pick one": []any{"A"}},
		}),
	}
	projection := projectTranscriptEvents(events)
	awaitingMessage, _ := projection.Entries[1]["awaitingInput"].(map[string]any)
	if awaitingMessage["answered"] != true {
		t.Errorf("assistant awaitingInput.answered = %v, want true", awaitingMessage["answered"])
	}
	for _, body := range projection.ActivityBodies {
		for _, entry := range body.Entries {
			if entry["metaKind"] != "awaiting_input" {
				continue
			}
			meta := entry["meta"].(map[string]any)
			if meta["title"] != "I need your input" {
				t.Errorf("answered card title = %q, want I need your input", meta["title"])
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

func TestProjectTranscriptEventsFoldsSessionLifecycleIntoTurn(t *testing.T) {
	// Create-with-initial-turn race: "Session is loading." lands before the
	// first turn's user message and "Session is ready." just after. Both are
	// operational noise that must fold into turn-1's activity body — the noise
	// bin — never rendering as top-level rows above the user's message.
	events := []map[string]any{
		projectionTestEvent("loading", "001", "session.status", "system", "tank", "", "sess:loading",
			map[string]any{"status": "loading", "text": "Session is loading."}),
		projectionTestEvent("ready", "002", "session.status", "system", "tank", "", "sess:ready",
			map[string]any{"status": "ready", "text": "Session is ready."}),
		projectionTestEvent("u", "003", "user_message.created", "user", "tank", "turn-1", "turn-1:user",
			map[string]any{"text": "hi", "display": map[string]any{"kind": "plain"}}),
		projectionTestEvent("started", "004", "turn.started", "runner", "tank", "turn-1", "",
			map[string]any{"status": "started"}),
		projectionTestEvent("final", "005", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:msg-1",
			map[string]any{"kind": "message", "text": "hello"}),
		projectionTestEvent("terminal", "006", "turn.completed", "runner", "codex", "turn-1", "",
			projectionFinalAnswerPayload("turn-1:item:msg-1")),
	}

	projection := projectTranscriptEvents(events)

	for _, entry := range projection.Entries {
		if isProjectionSessionStatus(entry) {
			t.Fatalf("session lifecycle leaked to conversation altitude: %#v", entry)
		}
	}
	if len(projection.Entries) == 0 || projection.Entries[0]["role"] != "user" {
		t.Fatalf("first top-level row = %#v, want the user message", projection.Entries)
	}
	var userKey, shellKey, shellStartKey string
	userIdx, shellIdx := -1, -1
	for i, entry := range projection.Entries {
		switch {
		case entry["kind"] == "message" && entry["role"] == "user":
			userKey, userIdx = transcriptMapString(entry, "orderKey"), i
		case entry["kind"] == "turn_activity":
			shellKey, shellIdx = transcriptMapString(entry, "orderKey"), i
			if act, ok := entry["activity"].(map[string]any); ok {
				shellStartKey = transcriptMapString(act, "startOrderKey")
			}
		}
	}
	if shellIdx < 0 {
		t.Fatalf("expected a turn_activity shell holding the folded lifecycle: %#v", projection.Entries)
	}
	// The shell carries folded lifecycle whose order keys predate the message; it
	// must still sort after the user message — by entry index, by its orderKey,
	// and by activity.startOrderKey (the field the row store turns into the row
	// cursor that actually orders the durable transcript).
	if shellIdx <= userIdx || shellKey <= userKey || shellStartKey <= userKey {
		t.Fatalf("activity shell must sort after the user message: userKey=%q shellKey=%q startKey=%q (idx %d vs %d)", userKey, shellKey, shellStartKey, userIdx, shellIdx)
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("missing activity body for turn-1")
	}
	folded := 0
	for _, entry := range body.Entries {
		if isProjectionSessionStatus(entry) {
			folded++
		}
	}
	if folded != 2 {
		t.Fatalf("turn-1 body session lifecycle entries = %d, want 2: %#v", folded, body.Entries)
	}
}

func TestProjectTranscriptEventsKeepsFailedSessionBannerPromoted(t *testing.T) {
	// A session.status:failed banner is a failure we surface: it stays a
	// top-level row, never folded into a turn's collapsed activity body.
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user",
			map[string]any{"text": "hi", "display": map[string]any{"kind": "plain"}}),
		projectionTestEvent("started", "002", "turn.started", "runner", "tank", "turn-1", "",
			map[string]any{"status": "started"}),
		projectionTestEvent("failed", "003", "session.status", "system", "tank", "", "sess:failed",
			map[string]any{"status": "failed", "text": "Session failed to start."}),
	}

	projection := projectTranscriptEvents(events)

	found := false
	for _, entry := range projection.Entries {
		if entry["kind"] == "message" && entry["role"] == "system" && entry["severity"] == "error" {
			found = true
			if tid, ok := entry["turnId"]; ok && tid != "" {
				t.Fatalf("failed banner was folded into a turn: %#v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("failed session banner missing from top-level transcript: %#v", projection.Entries)
	}
}

func TestProjectTranscriptEventsKeepsProviderRecoveryBannerPromoted(t *testing.T) {
	// A provider recovery (session.status:ready on a ".../provider/.../status"
	// timeline) is a banner, not startup noise — it carries status=ready but must
	// stay a visible top-level system message, never folded into the active turn.
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user",
			map[string]any{"text": "hi", "display": map[string]any{"kind": "plain"}}),
		projectionTestEvent("started", "002", "turn.started", "runner", "tank", "turn-1", "",
			map[string]any{"status": "started"}),
		projectionTestEvent("recover", "003", "session.status", "system", "tank", "", "session:63:provider:codex:status",
			map[string]any{"status": "ready", "text": "Codex sign-in is back online."}),
	}

	projection := projectTranscriptEvents(events)

	found := false
	for _, entry := range projection.Entries {
		if entry["kind"] == "message" && entry["role"] == "system" &&
			transcriptMapString(entry, "text") == "Codex sign-in is back online." {
			found = true
			if isProjectionSessionStatus(entry) {
				t.Fatalf("provider recovery banner was marked foldable: %#v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("provider recovery banner must stay a top-level system message: %#v", projection.Entries)
	}
}

func TestProjectTranscriptEventsDropsOrphanSessionLifecycle(t *testing.T) {
	// A loading/ready event with no owning turn (a session opened with no message
	// yet, or the per-event materialization path projecting a lone session.status)
	// produces no transcript row — happy-path lifecycle only surfaces by folding
	// into a turn.
	events := []map[string]any{
		projectionTestEvent("ready", "001", "session.status", "system", "tank", "", "sess:ready",
			map[string]any{"status": "ready", "text": "Session is ready."}),
	}
	projection := projectTranscriptEvents(events)
	if len(projection.Entries) != 0 {
		t.Fatalf("orphan session lifecycle should produce no rows, got: %#v", projection.Entries)
	}
}

func TestProjectTranscriptEventsKeepsOrphanFailedBanner(t *testing.T) {
	// A failed banner with no turn (the freshly-created-session failure backfill)
	// stays a top-level error row.
	events := []map[string]any{
		projectionTestEvent("failed", "001", "session.status", "system", "tank", "", "sess:failed",
			map[string]any{"status": "failed", "text": "Session failed to start."}),
	}
	projection := projectTranscriptEvents(events)
	if len(projection.Entries) != 1 || projection.Entries[0]["role"] != "system" || projection.Entries[0]["severity"] != "error" {
		t.Fatalf("orphan failed banner should remain a top-level error row, got: %#v", projection.Entries)
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
