package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
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

func TestProjectTranscriptEventsProjectsOriginSessionAvatarID(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "fix the avatar bug",
		}),
	}
	events[0]["origin_session_id"] = "42"
	events[0]["origin_session_avatar_id"] = "jp1-grant"

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 1; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	entry := projection.Entries[0]
	if got, want := entry["originSessionId"], "42"; got != want {
		t.Fatalf("originSessionId = %v, want %q", got, want)
	}
	if got, want := entry["originSessionAvatarId"], "jp1-grant"; got != want {
		t.Fatalf("originSessionAvatarId = %v, want %q", got, want)
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

func TestProjectTranscriptEventsPreservesSubmittedSourceOnTurnShell(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "Break-glass approval granted.",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{
			"status": "submitted",
			"source": string(conversation.TurnSubmittedSourceBreakGlassApproval),
		}),
		projectionTestEvent("tool-start", "003", "item.started", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":    "command_execution",
			"command": "request_git_break_glass",
		}),
		projectionTestEvent("tool-done", "004", "item.completed", "tool", "codex", "turn-1", "turn-1:item:tool", map[string]any{
			"kind":   "command_execution",
			"output": "activated",
		}),
		projectionTestEvent("final", "005", "item.completed", "assistant", "codex", "turn-1", "turn-1:item:final", map[string]any{
			"kind": "message",
			"text": "Break-glass tools are active.",
		}),
		projectionTestEvent("terminal", "006", "turn.completed", "runner", "codex", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:final")),
	}

	projection := projectTranscriptEvents(events)
	var shell map[string]any
	for _, entry := range projection.Entries {
		if entry["kind"] == "turn_activity" {
			shell = entry
			break
		}
	}
	if shell == nil {
		t.Fatalf("no turn_activity shell projected: %#v", projection.Entries)
	}
	if got, want := shell["submittedSource"], string(conversation.TurnSubmittedSourceBreakGlassApproval); got != want {
		t.Fatalf("shell submittedSource = %v, want %q: %#v", got, want, shell)
	}
	activity, _ := shell["activity"].(map[string]any)
	if got, want := activity["submittedSource"], string(conversation.TurnSubmittedSourceBreakGlassApproval); got != want {
		t.Fatalf("activity submittedSource = %v, want %q: %#v", got, want, activity)
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

func TestProjectTranscriptEventsSurfacesPerTurnModel(t *testing.T) {
	// The per-turn run config stamped on user_message.created (the model/effort
	// the turn ran on) survives the projection onto the user-message entry, so
	// the Turns surface can show which model answered each turn even after a
	// mid-session re-pin — the composer chip only reflects the next turn.
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "do work",
			"display": map[string]any{"kind": "plain"},
			"model":   "claude-opus-4-8",
			"effort":  "high",
		}),
	}
	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 1; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	msg := projection.Entries[0]
	if msg["kind"] != "message" || msg["role"] != "user" {
		t.Fatalf("entry[0] = %#v, want user message", msg)
	}
	if got, _ := msg["model"].(string); got != "claude-opus-4-8" {
		t.Fatalf("user message model = %q, want claude-opus-4-8", got)
	}
	if got, _ := msg["effort"].(string); got != "high" {
		t.Fatalf("user message effort = %q, want high", got)
	}
}

func TestTurnActivityShellCarriesPerTurnModel(t *testing.T) {
	// The per-turn model/effort is also copied onto the turn-activity shell —
	// the reliable per-turn carrier the frontend reads when a turn-page
	// deep-link loads the shell but not the full transcript.
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "do work",
			"display": map[string]any{"kind": "plain"},
			"model":   "claude-opus-4-8",
			"effort":  "high",
		}),
		projectionTestEvent("tool-start", "002", "item.started", "tool", "claude", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":    "command_execution",
			"command": "go test ./...",
		}),
		projectionTestEvent("tool-done", "003", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool-1", map[string]any{
			"kind":   "command_execution",
			"output": "ok",
		}),
		projectionTestEvent("final", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:msg-1", map[string]any{
			"kind": "message",
			"text": "done",
		}),
		projectionTestEvent("terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:msg-1")),
	}
	projection := projectTranscriptEvents(events)
	var shell map[string]any
	for _, e := range projection.Entries {
		if e["kind"] == "turn_activity" {
			shell = e
			break
		}
	}
	if shell == nil {
		t.Fatalf("no turn_activity shell projected: %#v", projection.Entries)
	}
	if got, _ := shell["model"].(string); got != "claude-opus-4-8" {
		t.Fatalf("shell model = %q, want claude-opus-4-8", got)
	}
	if got, _ := shell["effort"].(string); got != "high" {
		t.Fatalf("shell effort = %q, want high", got)
	}
	// The model also rides the activity summary — the carrier the frontend
	// turn-summary normalizer preserves through the row-merge path.
	activity, _ := shell["activity"].(map[string]any)
	if activity == nil {
		t.Fatalf("shell missing activity summary: %#v", shell)
	}
	if got, _ := activity["model"].(string); got != "claude-opus-4-8" {
		t.Fatalf("shell activity model = %q, want claude-opus-4-8", got)
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

func TestAnnotateProjectionTerminalScopesUsageToFinalAnswer(t *testing.T) {
	usage := map[string]any{"input_tokens": float64(120), "output_tokens": float64(30)}
	observation := map[string]any{"usage_source": "thread.tokenUsage.updated"}
	terminal := turnTerminalProjection{
		TurnID:           "turn-1",
		Status:           "completed",
		Usage:            usage,
		UsageObservation: observation,
		FinalAnswerIDs:   map[string]bool{"turn-1:item:answer": true},
	}
	progress := annotateProjectionTerminal(map[string]any{
		"id":     "turn-1:item:progress",
		"kind":   "message",
		"role":   "assistant",
		"turnId": "turn-1",
	}, map[string]turnTerminalProjection{"turn-1": terminal})
	if progress["turnUsage"] != nil {
		t.Fatalf("progress row inherited terminal usage: %#v", progress)
	}
	tool := annotateProjectionTerminal(map[string]any{
		"id":     "turn-1:item:tool",
		"kind":   "tool",
		"turnId": "turn-1",
	}, map[string]turnTerminalProjection{"turn-1": terminal})
	if tool["turnUsage"] != nil {
		t.Fatalf("tool row inherited terminal usage: %#v", tool)
	}
	answer := annotateProjectionTerminal(map[string]any{
		"id":     "turn-1:item:answer",
		"kind":   "message",
		"role":   "assistant",
		"turnId": "turn-1",
	}, map[string]turnTerminalProjection{"turn-1": terminal})
	if got := answer["turnUsage"]; !reflect.DeepEqual(got, usage) {
		t.Fatalf("final answer turnUsage = %#v, want %#v in %#v", got, usage, answer)
	}
	if got := answer["usageObservation"]; !reflect.DeepEqual(got, observation) {
		t.Fatalf("final answer usageObservation = %#v, want %#v in %#v", got, observation, answer)
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
		projectionTestEvent("submitted", "001a", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("invoke", "002", "turn.awaiting_input.invocation", "runner", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-1:item:tool-ask",
			"question_turn_id":     "turn-2",
			"question_timeline_id": "turn-2:item:tool-ask",
			"question_page":        1,
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
		projectionTestEvent("usage", "005", "turn.usage", "runner", "claude", "turn-1", "", map[string]any{
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 8},
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
	var invocation map[string]any
	for _, body := range projection.ActivityBodies {
		if body.TurnID != "turn-1" {
			continue
		}
		for _, entry := range body.Entries {
			if entry["toolName"] == "AskUserQuestion" {
				invocation = entry
				break
			}
		}
	}
	if invocation == nil {
		t.Fatalf("expected AskUserQuestion invocation marker in asking turn body, got bodies: %#v", projection.ActivityBodies)
	}
	target, _ := invocation["questionTarget"].(map[string]any)
	if target == nil {
		t.Fatalf("invocation questionTarget missing: %#v", invocation)
	}
	if target["turnId"] != "turn-2" {
		t.Errorf("questionTarget.turnId = %v, want turn-2", target["turnId"])
	}
	if target["timelineId"] != "turn-2:item:tool-ask" {
		t.Errorf("questionTarget.timelineId = %v, want turn-2:item:tool-ask", target["timelineId"])
	}
	if target["page"] != 1 {
		t.Errorf("questionTarget.page = %v, want 1", target["page"])
	}
	if got, want := len(projection.Entries), 4; got != want {
		t.Fatalf("projected entries = %d, want user + asking turn shell + assistant question + question turn shell: %#v", got, projection.Entries)
	}
	askingShell := projection.Entries[1]
	if askingShell["kind"] != "turn_activity" || askingShell["turnId"] != "turn-1" {
		t.Fatalf("second transcript entry = %#v, want completed asking turn activity shell", askingShell)
	}
	askingActivity, _ := askingShell["activity"].(map[string]any)
	if askingActivity["status"] != "completed" || askingActivity["active"] == true {
		t.Fatalf("asking shell activity = %#v, want completed inactive", askingActivity)
	}
	if projection.Entries[2]["kind"] != "message" || projection.Entries[2]["role"] != "assistant" {
		t.Fatalf("third transcript entry = %#v, want assistant question message", projection.Entries[2])
	}
	if projection.Entries[2]["text"] != "1. Which auth method?" {
		t.Fatalf("assistant question text = %q", projection.Entries[2]["text"])
	}
	shell := projection.Entries[3]
	if shell["kind"] != "turn_activity" || shell["turnId"] != "turn-2" {
		t.Fatalf("fourth transcript entry = %#v, want question turn activity shell", shell)
	}
	activity, _ := shell["activity"].(map[string]any)
	if activity["status"] != "needs_input" {
		t.Fatalf("question shell status = %v, want needs_input", activity["status"])
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

func TestProjectTranscriptEventsAskUserQuestionHandoffStopsActiveAskingTurn(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("u", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "ask me",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("started", "002", "turn.started", "runner", "tank", "turn-1", "", map[string]any{"status": "started"}),
		projectionTestEvent("invoke", "003", "turn.awaiting_input.invocation", "runner", "claude", "turn-1", "turn-1:item:tool-ask", map[string]any{
			"provider_item_id": "toolu_ask",
			"timeline_id":      "turn-1:item:tool-ask",
			"questions": []any{
				map[string]any{"question": "Which animal?", "allowFreeForm": true},
			},
		}),
		projectionTestEvent("msg", "004", "assistant_message.created", "assistant", "claude", "turn-1", "turn-1:assistant_question:ask", map[string]any{
			"text":    "1. Which animal?",
			"display": map[string]any{"kind": "ask_user_question"},
			"awaiting_input": map[string]any{
				"asking_turn_id":       "turn-1",
				"question_turn_id":     "turn-2",
				"provider_item_id":     "toolu_ask",
				"timeline_id":          "turn-2:item:tool-ask",
				"provider_timeline_id": "turn-1:item:tool-ask",
				"questions":            []any{map[string]any{"question": "Which animal?", "allowFreeForm": true}},
			},
		}),
		projectionTestEvent("usage", "005", "turn.usage", "runner", "claude", "turn-1", "", map[string]any{
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 8},
		}),
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want user + completed shell + assistant question: %#v", got, projection.Entries)
	}
	shell := projection.Entries[1]
	if shell["kind"] != "turn_activity" || shell["turnId"] != "turn-1" {
		t.Fatalf("second transcript entry = %#v, want asking turn activity shell", shell)
	}
	activity, _ := shell["activity"].(map[string]any)
	if activity["status"] != "completed" || activity["active"] == true {
		t.Fatalf("asking shell activity = %#v, want completed inactive", activity)
	}
	if projection.Entries[2]["kind"] != "message" || projection.Entries[2]["role"] != "assistant" {
		t.Fatalf("third transcript entry = %#v, want assistant question message", projection.Entries[2])
	}
	if projection.Entries[2]["text"] != "1. Which animal?" {
		t.Fatalf("assistant question text = %q", projection.Entries[2]["text"])
	}
	for _, id := range shell["activityIds"].([]string) {
		if id == "turn-1:assistant_question:ask" {
			t.Fatalf("assistant question message was compacted into asking shell: %#v", shell)
		}
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
	if got, want := len(projection.Entries), 4; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if projection.Entries[0]["kind"] != "message" {
		t.Fatalf("first entry = %#v, want user message", projection.Entries[0])
	}
	if projection.Entries[1]["kind"] != "turn_activity" || projection.Entries[1]["turnId"] != "turn-1" {
		t.Fatalf("second entry = %#v, want completed asking turn activity shell", projection.Entries[1])
	}
	askingActivity, _ := projection.Entries[1]["activity"].(map[string]any)
	if askingActivity["status"] != "completed" || askingActivity["active"] == true {
		t.Fatalf("asking activity = %#v, want completed inactive", askingActivity)
	}
	if projection.Entries[2]["role"] != "assistant" {
		t.Fatalf("third entry = %#v, want assistant question message", projection.Entries[2])
	}
	if projection.Entries[3]["kind"] != "turn_activity" || projection.Entries[3]["turnId"] != "turn-2" {
		t.Fatalf("fourth entry = %#v, want question turn activity shell", projection.Entries[3])
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
		// The answer marker links back to the question's timeline
		// id; the visible answer text is a separate user submission.
		projectionTestEvent("ans", "004", "turn.input_answered", "user", "tank", "turn-2", "turn-2:item:tool-ask:answer", map[string]any{
			"question_timeline_id": "turn-2:item:tool-ask",
			"provider_item_id":     "toolu_ask",
			"answers":              map[string]any{"Pick one": []any{"A"}},
		}),
	}
	projection := projectTranscriptEvents(events)
	var awaitingMessage map[string]any
	for _, entry := range projection.Entries {
		if entry["role"] != "assistant" {
			continue
		}
		awaitingMessage, _ = entry["awaitingInput"].(map[string]any)
		break
	}
	if awaitingMessage == nil {
		t.Fatalf("missing assistant awaitingInput message: %#v", projection.Entries)
	}
	if awaitingMessage["answered"] != true {
		t.Errorf("assistant awaitingInput.answered = %v, want true", awaitingMessage["answered"])
	}
	body, ok := projection.ActivityBodies["turn-2"]
	if !ok {
		t.Fatalf("missing question turn activity body: %#v", projection.ActivityBodies)
	}
	if body.Status != "answered" {
		t.Fatalf("question turn status = %q, want answered", body.Status)
	}
	if body.Summary["active"] == true {
		t.Fatalf("question turn activity stayed active after answer: %#v", body.Summary)
	}
	for _, entry := range projection.Entries {
		if strings.HasPrefix(transcriptMapString(entry, "id"), "turn-terminal:") {
			t.Fatalf("answered question turn rendered terminal meta row: %#v", entry)
		}
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

func TestProjectTranscriptEventsKeepsBackgroundTaskWakeMechanicsOutOfMainTranscript(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("submitted", "001", "turn.submitted", "runner", "tank", "turn_bgtask-bocpzxcm3", "", map[string]any{"status": "submitted", "source": "background-task", "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("tool", "002", "item.completed", "tool", "claude", "turn_bgtask-bocpzxcm3", "turn_bgtask-bocpzxcm3:item:tool-1", map[string]any{
			"kind":   "tool_result",
			"title":  "BashOutput",
			"output": "tests still running",
		}),
		projectionTestEvent("terminal", "003", "turn.completed", "runner", "claude", "turn_bgtask-bocpzxcm3", "", nil),
	}

	projection := projectTranscriptEvents(events)
	// This wake turn has no derivable originating turn (no shell_task lineage
	// in the ledger), so it cannot fold anywhere. Fail-soft: its collapsed
	// activity shell survives as the body's container — content is never
	// dropped without a surviving home — while the wake prompt and tool
	// mechanics stay inside the body, never as settled transcript rows.
	if got, want := len(projection.Entries), 1; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got := transcriptMapString(projection.Entries[0], "kind"); got != "turn_activity" {
		t.Fatalf("orphan wake container kind = %q, want turn_activity shell: %#v", got, projection.Entries[0])
	}
	if got, want := transcriptMapString(projection.Entries[0], "turnId"), "turn_bgtask-bocpzxcm3"; got != want {
		t.Fatalf("orphan wake shell turnId = %q, want %q", got, want)
	}
	body, ok := projection.ActivityBodies["turn_bgtask-bocpzxcm3"]
	if !ok {
		t.Fatalf("background-task wake did not remain available in Turn activity: %#v", projection.ActivityBodies)
	}
	if got, want := len(body.Entries), 2; got != want {
		t.Fatalf("activity body entries = %d, want %d: %#v", got, want, body.Entries)
	}
	if !isBackgroundWakeChip(body.Entries[0], "A background task you started earlier has finished.") {
		t.Fatalf("first activity entry is not the wake meta chip: %#v", body.Entries[0])
	}
	if got := transcriptMapString(body.Entries[0], "kind"); got != "meta" {
		t.Fatalf("wake boundary kind = %q, want meta chip — the agent-directed prompt must not render as a user bubble: %#v", got, body.Entries[0])
	}
}

func TestProjectTranscriptEventsPromotesFinalAnswerFromBackgroundTaskWake(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("submitted", "001", "turn.submitted", "runner", "tank", "turn_bgtask-bocpzxcm3", "", map[string]any{"status": "submitted", "source": "background-task", "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("tool", "002", "item.completed", "tool", "claude", "turn_bgtask-bocpzxcm3", "turn_bgtask-bocpzxcm3:item:tool-1", map[string]any{
			"kind":   "tool_result",
			"title":  "BashOutput",
			"output": "tests passed",
		}),
		projectionTestEvent("final", "003", "item.completed", "assistant", "claude", "turn_bgtask-bocpzxcm3", "turn_bgtask-bocpzxcm3:item:msg-1", map[string]any{
			"kind": "message",
			"text": "CI passed. The branch is ready.",
		}),
		projectionTestEvent("terminal", "004", "turn.completed", "runner", "claude", "turn_bgtask-bocpzxcm3", "", projectionFinalAnswerPayload("turn_bgtask-bocpzxcm3:item:msg-1")),
	}

	projection := projectTranscriptEvents(events)
	// No derivable originating turn (no task lineage), so the wake keeps its
	// own shell as the body's container; the explicitly promoted final answer
	// is the only settled message row.
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got := transcriptMapString(projection.Entries[0], "kind"); got != "turn_activity" {
		t.Fatalf("first entry kind = %q, want orphan wake shell: %#v", got, projection.Entries[0])
	}
	entry := projection.Entries[1]
	if entry["kind"] != "message" || entry["role"] != "assistant" {
		t.Fatalf("background wake final answer should be the only settled message row, got: %#v", entry)
	}
	body, ok := projection.ActivityBodies["turn_bgtask-bocpzxcm3"]
	if !ok {
		t.Fatalf("background-task wake did not remain available in Turn activity: %#v", projection.ActivityBodies)
	}
	if got, want := len(body.Entries), 3; got != want {
		t.Fatalf("activity body entries = %d, want %d: %#v", got, want, body.Entries)
	}
	if !isBackgroundWakeChip(body.Entries[0], "A background task you started earlier has finished.") {
		t.Fatalf("first activity entry is not the wake meta chip: %#v", body.Entries[0])
	}
}

func TestProjectTranscriptEventsHidesBackgroundContinuationTurnFromMainTranscript(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "Run CI and tell me when it passes.",
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("task-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id":     "task-ci",
			"status":      "running",
			"summary":     "CI check",
			"description": "Waiting for CI",
		}),
		projectionTestEvent("waiting-final", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:waiting", map[string]any{
			"kind": "message",
			"text": "I will wait for CI and check back when it finishes.",
		}),
		projectionTestEvent("turn-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:waiting")),
		projectionTestEvent("task-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": "task-ci",
			"status":  "completed",
			"summary": "CI passed",
			"output":  "All checks passed.",
		}),
		projectionTestEvent("wake-submitted", "007", "turn.submitted", "runner", "tank", "turn_bgtask-task-ci", "", map[string]any{"status": "submitted", "source": "background-task", "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("wake-final", "008", "item.completed", "assistant", "claude", "turn_bgtask-task-ci", "turn_bgtask-task-ci:item:final", map[string]any{
			"kind": "message",
			"text": "CI passed. The branch is ready.",
		}),
		projectionTestEvent("wake-terminal", "009", "turn.completed", "runner", "claude", "turn_bgtask-task-ci", "", projectionFinalAnswerPayload("turn_bgtask-task-ci:item:final")),
	}

	projection := projectTranscriptEvents(events)
	// The parked origin turn keeps its activity shell — parked is a state on
	// the shell, not grounds for suppression (the session-161 annihilation).
	// Settled rows: user message, origin shell, folded wake final answer.
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got, want := projection.Entries[0]["role"], "user"; got != want {
		t.Fatalf("first entry role = %#v, want %#v: %#v", got, want, projection.Entries[0])
	}
	if got := transcriptMapString(projection.Entries[0], "text"); !strings.Contains(got, "Run CI") {
		t.Fatalf("first entry text = %q, want original user message", got)
	}
	if got := transcriptMapString(projection.Entries[1], "kind"); got != "turn_activity" {
		t.Fatalf("second entry kind = %q, want parked origin shell: %#v", got, projection.Entries[1])
	}
	if got, want := transcriptMapString(projection.Entries[1], "turnId"), "turn-1"; got != want {
		t.Fatalf("origin shell turnId = %q, want %q", got, want)
	}
	if shellActivity, ok := projection.Entries[1]["activity"].(map[string]any); !ok || shellActivity["continuation"] != true {
		t.Fatalf("parked origin shell must carry continuation state: %#v", projection.Entries[1])
	}
	if got, want := projection.Entries[2]["role"], "assistant"; got != want {
		t.Fatalf("third entry role = %#v, want %#v: %#v", got, want, projection.Entries[2])
	}
	if got := transcriptMapString(projection.Entries[2], "text"); got != "CI passed. The branch is ready." {
		t.Fatalf("third entry text = %q, want wake final answer", got)
	}
	if got, want := transcriptMapString(projection.Entries[2], "turnId"), "turn-1"; got != want {
		t.Fatalf("wake final projected turnId = %q, want parent %q: %#v", got, want, projection.Entries[2])
	}
	if got, want := transcriptMapString(projection.Entries[2], "backendTurnId"), "turn_bgtask-task-ci"; got != want {
		t.Fatalf("wake final backendTurnId = %q, want wake turn %q: %#v", got, want, projection.Entries[2])
	}
	for _, entry := range projection.Entries {
		text := transcriptMapString(entry, "text")
		if strings.Contains(text, "wait for CI") ||
			strings.Contains(text, "background task you started earlier") ||
			entry["kind"] == "background_task" {
			t.Fatalf("continuation mechanics leaked into main transcript: %#v", entry)
		}
		if entry["kind"] == "turn_activity" && transcriptMapString(entry, "turnId") != "turn-1" {
			t.Fatalf("wake turn leaked its own activity shell into main transcript: %#v", entry)
		}
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("continuation turn did not remain available in Turn activity: %#v", projection.ActivityBodies)
	}
	if got, want := len(body.Entries), 4; got != want {
		t.Fatalf("continuation activity entries = %d, want %d: %#v", got, want, body.Entries)
	}
	if got, want := len(body.CompactedEntryIDs), 3; got != want {
		t.Fatalf("continuation compacted entries = %d, want %d: %#v", got, want, body.CompactedEntryIDs)
	}
	if _, ok := projection.ActivityBodies["turn_bgtask-task-ci"]; ok {
		t.Fatalf("wake continuation should be folded into parent activity body: %#v", projection.ActivityBodies)
	}
	foundWakeFinal := false
	foundWakePrompt := false
	for _, entry := range body.Entries {
		if isBackgroundWakeChip(entry, "A background task you started earlier has finished.") {
			foundWakePrompt = true
			if got, want := transcriptMapString(entry, "turnId"), "turn-1"; got != want {
				t.Fatalf("wake chip turnId = %q, want parent %q: %#v", got, want, entry)
			}
			if got, want := transcriptMapString(entry, "backendTurnId"), "turn_bgtask-task-ci"; got != want {
				t.Fatalf("wake chip backendTurnId = %q, want %q: %#v", got, want, entry)
			}
		}
		if transcriptMapString(entry, "text") == "CI passed. The branch is ready." {
			foundWakeFinal = true
			if got, want := transcriptMapString(entry, "turnId"), "turn-1"; got != want {
				t.Fatalf("wake activity entry turnId = %q, want parent %q: %#v", got, want, entry)
			}
		}
	}
	if !foundWakeFinal {
		t.Fatalf("parent activity body missing wake final: %#v", body.Entries)
	}
	if !foundWakePrompt {
		t.Fatalf("parent activity body missing wake chip: %#v", body.Entries)
	}
}

func TestProjectTranscriptEventsFoldsHashedBackgroundWakeTurnIntoParent(t *testing.T) {
	taskID := "task:ci:with:colon"
	wakeTurnID := conversation.TurnIDForClientNonce(pgstore.BackgroundTaskWakeClientNonce(taskID))
	if wakeTurnID == "turn_bgtask-"+taskID {
		t.Fatalf("test task id should exercise hashed wake nonce, got %q", wakeTurnID)
	}
	events := []map[string]any{
		projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "Run CI and tell me when it passes.",
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("task-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": taskID,
			"status":  "running",
			"summary": "CI check",
		}),
		projectionTestEvent("waiting-final", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:waiting", map[string]any{
			"kind": "message",
			"text": "I will wait for CI and check back when it finishes.",
		}),
		projectionTestEvent("turn-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:waiting")),
		projectionTestEvent("task-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": taskID,
			"status":  "completed",
			"summary": "CI passed",
		}),
		projectionTestEvent("wake-submitted", "007", "turn.submitted", "runner", "tank", wakeTurnID, "", map[string]any{"status": "submitted", "source": "background-task", "task_id": taskID, "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("wake-final", "008", "item.completed", "assistant", "claude", wakeTurnID, wakeTurnID+":item:final", map[string]any{
			"kind": "message",
			"text": "CI passed. The branch is ready.",
		}),
		projectionTestEvent("wake-terminal", "009", "turn.completed", "runner", "claude", wakeTurnID, "", projectionFinalAnswerPayload(wakeTurnID+":item:final")),
	}

	projection := projectTranscriptEvents(events)
	// user message, parked origin shell, folded wake final answer.
	if got, want := len(projection.Entries), 3; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	if got := transcriptMapString(projection.Entries[1], "kind"); got != "turn_activity" {
		t.Fatalf("second entry kind = %q, want parked origin shell: %#v", got, projection.Entries[1])
	}
	if got, want := transcriptMapString(projection.Entries[2], "turnId"), "turn-1"; got != want {
		t.Fatalf("wake final projected turnId = %q, want parent %q: %#v", got, want, projection.Entries[2])
	}
	if got, want := transcriptMapString(projection.Entries[2], "backendTurnId"), wakeTurnID; got != want {
		t.Fatalf("wake final backendTurnId = %q, want wake turn %q: %#v", got, want, projection.Entries[2])
	}
	if _, ok := projection.ActivityBodies[wakeTurnID]; ok {
		t.Fatalf("hashed wake body should be folded into parent: %#v", projection.ActivityBodies)
	}
	if _, ok := projection.ActivityBodies["turn-1"]; !ok {
		t.Fatalf("parent body missing after hashed wake fold: %#v", projection.ActivityBodies)
	}
}

// TestProjectTranscriptEventsCollapsesChainedBackgroundWakeIntoOriginTurn covers
// the wake-of-a-wake chain seen in session 655 / turn 56: a background task
// started in a real turn wakes a continuation turn, and that wake turn itself
// launches another background task whose terminal wakes a further continuation
// turn. The whole chain must collapse into the one originating real turn — no
// intermediate wake turn may surface as a standalone user-visible turn, and a
// duplicated wake fire (a stale-claim re-submit) must not stack a second
// identical system-user prompt.
func TestProjectTranscriptEventsCollapsesChainedBackgroundWakeIntoOriginTurn(t *testing.T) {
	wakeTurnX := conversation.TurnIDForClientNonce(pgstore.BackgroundTaskWakeClientNonce("taskx"))
	wakeTurnY := conversation.TurnIDForClientNonce(pgstore.BackgroundTaskWakeClientNonce("tasky"))
	const promptX = "A background task you started earlier has finished. [taskx]"
	const promptY = "A background task you started earlier has finished. [tasky]"
	events := []map[string]any{
		projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "Run CI and tell me when it passes.",
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("x-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:x", map[string]any{
			"task_id": "taskx", "status": "running", "summary": "first task",
		}),
		projectionTestEvent("waiting-1", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:waiting1", map[string]any{
			"kind": "message", "text": "I will wait for the first task.",
		}),
		projectionTestEvent("turn-1-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:waiting1")),
		projectionTestEvent("x-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:x", map[string]any{
			"task_id": "taskx", "status": "completed", "summary": "first done",
		}),
		// Wake for taskx — itself launches tasky.
		projectionTestEvent("x-wake", "007", "turn.submitted", "runner", "tank", wakeTurnX, "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "taskx", "prompt": promptX}),
		projectionTestEvent("y-started", "008", "shell_task.started", "tool", "claude", wakeTurnX, wakeTurnX+":task:y", map[string]any{
			"task_id": "tasky", "status": "running", "summary": "second task",
		}),
		projectionTestEvent("waiting-2", "009", "item.completed", "assistant", "claude", wakeTurnX, wakeTurnX+":item:waiting2", map[string]any{
			"kind": "message", "text": "I will wait for the second task.",
		}),
		projectionTestEvent("x-wake-terminal", "010", "turn.completed", "runner", "claude", wakeTurnX, "", projectionFinalAnswerPayload(wakeTurnX+":item:waiting2")),
		projectionTestEvent("y-exited", "011", "shell_task.exited", "tool", "claude", wakeTurnX, wakeTurnX+":task:y", map[string]any{
			"task_id": "tasky", "status": "completed", "summary": "second done",
		}),
		// Wake for tasky — fired twice (stale-claim re-submit). Only one prompt.
		projectionTestEvent("y-wake", "012", "turn.submitted", "runner", "tank", wakeTurnY, "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "tasky", "prompt": promptY}),
		projectionTestEvent("y-wake-dup", "013", "turn.submitted", "runner", "tank", wakeTurnY, "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "tasky", "prompt": promptY}),
		projectionTestEvent("y-final", "014", "item.completed", "assistant", "claude", wakeTurnY, wakeTurnY+":item:final", map[string]any{
			"kind": "message", "text": "Both tasks passed. The branch is ready.",
		}),
		projectionTestEvent("y-wake-terminal", "015", "turn.completed", "runner", "claude", wakeTurnY, "", projectionFinalAnswerPayload(wakeTurnY+":item:final")),
	}

	projection := projectTranscriptEvents(events)

	// Main transcript: the original user message plus the single true final
	// answer, attributed to the origin turn while retaining the wake backend id.
	if got := transcriptMapString(projection.Entries[0], "role"); got != "user" {
		t.Fatalf("first entry role = %q, want user: %#v", got, projection.Entries[0])
	}
	last := projection.Entries[len(projection.Entries)-1]
	if got, want := transcriptMapString(last, "text"), "Both tasks passed. The branch is ready."; got != want {
		t.Fatalf("last entry text = %q, want true final answer: %#v", got, projection.Entries)
	}
	if got, want := transcriptMapString(last, "turnId"), "turn-1"; got != want {
		t.Fatalf("final answer turnId = %q, want origin %q: %#v", got, want, last)
	}
	if got, want := transcriptMapString(last, "backendTurnId"), wakeTurnY; got != want {
		t.Fatalf("final answer backendTurnId = %q, want %q: %#v", got, want, last)
	}
	for _, entry := range projection.Entries {
		// The parked origin turn keeps its own shell; no wake turn in the
		// chain may leak one.
		if entry["kind"] == "turn_activity" && transcriptMapString(entry, "turnId") != "turn-1" {
			t.Fatalf("chained wake leaked an activity shell into the main transcript: %#v", entry)
		}
		text := transcriptMapString(entry, "text")
		if strings.Contains(text, "background task you started earlier") || strings.Contains(text, "I will wait") {
			t.Fatalf("chained wake mechanics leaked into main transcript: %#v", entry)
		}
	}

	// No intermediate wake turn may surface as its own activity body; the whole
	// chain folds into the origin turn.
	if _, ok := projection.ActivityBodies[wakeTurnX]; ok {
		t.Fatalf("intermediate wake turn %s surfaced standalone: %#v", wakeTurnX, projection.ActivityBodies)
	}
	if _, ok := projection.ActivityBodies[wakeTurnY]; ok {
		t.Fatalf("chained wake turn %s surfaced standalone: %#v", wakeTurnY, projection.ActivityBodies)
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("origin turn lost its activity body after chained fold: %#v", projection.ActivityBodies)
	}

	// Both distinct wake prompts fold in (once each), system-authored and owned
	// by the origin turn; the duplicated tasky fire does NOT stack a second copy.
	countX, countY := 0, 0
	for _, entry := range body.Entries {
		switch {
		case isBackgroundWakeChip(entry, promptX):
			countX++
			assertChainedWakePrompt(t, entry, wakeTurnX)
		case isBackgroundWakeChip(entry, promptY):
			countY++
			assertChainedWakePrompt(t, entry, wakeTurnY)
		}
	}
	if countX != 1 {
		t.Fatalf("taskx wake prompt count = %d, want 1: %#v", countX, body.Entries)
	}
	if countY != 1 {
		t.Fatalf("tasky wake prompt count = %d, want 1 (duplicate fire must dedupe): %#v", countY, body.Entries)
	}
}

// TestProjectSessionBackgroundTasksListsRunningAndCompleted covers the
// session-level Background feed: the durable shell_task.* lifecycle projects to
// first-class background_task entries (running and completed), the data the
// Background screen renders instead of filtering the main transcript rows (which
// never contain background_task entries).
func TestProjectSessionBackgroundTasksListsRunningAndCompleted(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("a-start", "001", "shell_task.started", "tool", "claude", "turn-1", "turn-1:shell_task:a", map[string]any{
			"task_id": "taska", "status": "running", "summary": "watcher",
		}),
		projectionTestEvent("b-start", "002", "shell_task.started", "tool", "claude", "turn-1", "turn-1:shell_task:b", map[string]any{
			"task_id": "taskb", "status": "running", "summary": "sleep 180",
		}),
		projectionTestEvent("b-exit", "003", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:shell_task:b", map[string]any{
			"task_id": "taskb", "status": "completed", "summary": "done",
		}),
	}
	tasks := projectSessionBackgroundTasks(events)
	if got, want := len(tasks), 2; got != want {
		t.Fatalf("background tasks = %d, want %d: %#v", got, want, tasks)
	}
	byID := map[string]map[string]any{}
	for _, task := range tasks {
		if got := transcriptMapString(task, "kind"); got != "background_task" {
			t.Fatalf("entry kind = %q, want background_task: %#v", got, task)
		}
		byID[transcriptMapString(task, "taskId")] = task
	}
	if got := transcriptMapString(byID["taska"], "taskStatus"); got != "running" {
		t.Fatalf("taska status = %q, want running: %#v", got, byID["taska"])
	}
	if got := transcriptMapString(byID["taskb"], "taskStatus"); got != "completed" {
		t.Fatalf("taskb status = %q, want completed: %#v", got, byID["taskb"])
	}
}

func TestProjectTranscriptEventsProjectsScheduledWakeupLifecycleForBackground(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("wake-scheduled", "001", "scheduled_wakeup.updated", "system", "tank", "turn_schedule_wakeup-wakeup_123", "scheduled-wakeup:wakeup_123", map[string]any{
			"kind":              "scheduled_wakeup",
			"wakeup_id":         "wakeup_123",
			"status":            "scheduled",
			"prompt":            "check CI",
			"scheduled_turn_id": "turn_schedule_wakeup-wakeup_123",
			"provider_item_id":  "toolu_wake",
			"scheduled_at":      "2026-06-03T15:20:00Z",
			"due_at":            "2026-06-03T15:25:00Z",
			"attempt_count":     float64(0),
		}),
		projectionTestEvent("wake-fired", "002", "scheduled_wakeup.updated", "system", "tank", "turn_schedule_wakeup-wakeup_123", "scheduled-wakeup:wakeup_123", map[string]any{
			"kind":              "scheduled_wakeup",
			"wakeup_id":         "wakeup_123",
			"status":            "fired",
			"prompt":            "check CI",
			"scheduled_turn_id": "turn_schedule_wakeup-wakeup_123",
			"provider_item_id":  "toolu_wake",
			"scheduled_at":      "2026-06-03T15:20:00Z",
			"due_at":            "2026-06-03T15:25:00Z",
			"attempt_count":     float64(1),
			"fired_turn_id":     "turn_schedule_wakeup-wakeup_123",
		}),
	}
	for _, event := range events {
		event["client_nonce"] = "schedule_wakeup-wakeup_123"
	}

	pendingProjection := projectTranscriptEvents(events[:1])
	if got, want := len(pendingProjection.Entries), 1; got != want {
		t.Fatalf("pending projected entries = %d, want %d: %#v", got, want, pendingProjection.Entries)
	}
	pending := pendingProjection.Entries[0]
	if got := transcriptMapString(pending, "taskStatus"); got != "running" {
		t.Fatalf("pending taskStatus = %q, want running: %#v", got, pending)
	}
	if got := transcriptMapString(pending, "taskSummary"); got != "Timer scheduled" {
		t.Fatalf("pending taskSummary = %q, want Timer scheduled: %#v", got, pending)
	}
	if got, _ := pending["backgroundOnly"].(bool); !got {
		t.Fatalf("pending backgroundOnly = %#v, want true: %#v", pending["backgroundOnly"], pending)
	}

	projection := projectTranscriptEvents(events)
	if got, want := len(projection.Entries), 1; got != want {
		t.Fatalf("projected entries = %d, want %d: %#v", got, want, projection.Entries)
	}
	entry := projection.Entries[0]
	if got := transcriptMapString(entry, "kind"); got != "background_task" {
		t.Fatalf("entry kind = %q, want background_task: %#v", got, entry)
	}
	if got, _ := entry["backgroundOnly"].(bool); !got {
		t.Fatalf("backgroundOnly = %#v, want true: %#v", entry["backgroundOnly"], entry)
	}
	if got := transcriptMapString(entry, "taskKind"); got != "scheduled_wakeup" {
		t.Fatalf("taskKind = %q, want scheduled_wakeup: %#v", got, entry)
	}
	if got := transcriptMapString(entry, "taskStatus"); got != "completed" {
		t.Fatalf("taskStatus = %q, want completed: %#v", got, entry)
	}
	if got := transcriptMapString(entry, "taskSummary"); got != "Timer fired" {
		t.Fatalf("taskSummary = %q, want Timer fired: %#v", got, entry)
	}
	if got := transcriptMapString(entry, "taskCommand"); got != "check CI" {
		t.Fatalf("taskCommand = %q, want prompt: %#v", got, entry)
	}
	if got := transcriptMapString(entry, "wakeupDueAt"); got != "2026-06-03T15:25:00Z" {
		t.Fatalf("wakeupDueAt = %q, want due timestamp: %#v", got, entry)
	}
	if got := transcriptMapString(entry, "wakeupFiredTurnId"); got != "turn_schedule_wakeup-wakeup_123" {
		t.Fatalf("wakeupFiredTurnId = %q, want fired turn: %#v", got, entry)
	}
}

func assertChainedWakePrompt(t *testing.T, entry map[string]any, wakeTurnID string) {
	t.Helper()
	if got, want := transcriptMapString(entry, "metaKind"), "background_task_wake"; got != want {
		t.Fatalf("wake chip metaKind = %q, want %q: %#v", got, want, entry)
	}
	if got, want := transcriptMapString(entry, "turnId"), "turn-1"; got != want {
		t.Fatalf("wake chip turnId = %q, want origin turn-1: %#v", got, entry)
	}
	if got, want := transcriptMapString(entry, "backendTurnId"), wakeTurnID; got != want {
		t.Fatalf("wake chip backendTurnId = %q, want %q: %#v", got, want, entry)
	}
}

func TestProjectTranscriptEventsHidesFailedBackgroundWakeContinuationFromMainTranscript(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "Run CI and tell me when it passes.",
		}),
		projectionTestEvent("submitted", "002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("task-started", "003", "shell_task.started", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": "task-ci",
			"status":  "running",
			"summary": "CI check",
		}),
		projectionTestEvent("waiting-final", "004", "item.completed", "assistant", "claude", "turn-1", "turn-1:item:waiting", map[string]any{
			"kind": "message",
			"text": "I will wait for CI and check back when it finishes.",
		}),
		projectionTestEvent("turn-terminal", "005", "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload("turn-1:item:waiting")),
		projectionTestEvent("task-exited", "006", "shell_task.exited", "tool", "claude", "turn-1", "turn-1:task:ci", map[string]any{
			"task_id": "task-ci",
			"status":  "completed",
			"summary": "CI passed",
		}),
		projectionTestEvent("wake-submitted", "007", "turn.submitted", "runner", "tank", "turn_bgtask-task-ci", "", map[string]any{"status": "submitted", "source": "background-task", "task_id": "task-ci", "prompt": "A background task you started earlier has finished."}),
		projectionTestEvent("wake-progress", "008", "item.completed", "assistant", "claude", "turn_bgtask-task-ci", "turn_bgtask-task-ci:item:progress", map[string]any{
			"kind": "message",
			"text": "I saw the task output but then hit an error.",
		}),
		projectionTestEvent("wake-failed", "009", "turn.failed", "runner", "claude", "turn_bgtask-task-ci", "", map[string]any{
			"error":  "provider rate limit",
			"reason": "provider_rate_limit",
		}),
	}

	projection := projectTranscriptEvents(events)
	// The failed wake's prose never settles; the user row plus the parked
	// origin turn's shell (the durable home of the wake's failure context)
	// are the only settled rows.
	if got, want := len(projection.Entries), 2; got != want {
		t.Fatalf("projected entries = %d, want user row + parked origin shell: %#v", got, projection.Entries)
	}
	if got, want := projection.Entries[0]["role"], "user"; got != want {
		t.Fatalf("first row role = %#v, want %#v: %#v", got, want, projection.Entries[0])
	}
	if got := transcriptMapString(projection.Entries[1], "kind"); got != "turn_activity" {
		t.Fatalf("second entry kind = %q, want parked origin shell: %#v", got, projection.Entries[1])
	}
	if got, want := transcriptMapString(projection.Entries[1], "turnId"), "turn-1"; got != want {
		t.Fatalf("origin shell turnId = %q, want %q", got, want)
	}
	body, ok := projection.ActivityBodies["turn-1"]
	if !ok {
		t.Fatalf("parent activity body missing: %#v", projection.ActivityBodies)
	}
	if _, ok := projection.ActivityBodies["turn_bgtask-task-ci"]; ok {
		t.Fatalf("wake body should fold into parent: %#v", projection.ActivityBodies)
	}
	foundWakePrompt := false
	foundFailedMeta := false
	for _, entry := range body.Entries {
		if isBackgroundWakeChip(entry, "A background task you started earlier has finished.") {
			foundWakePrompt = true
		}
		if transcriptMapString(entry, "kind") == "meta" && transcriptMapString(transcriptMap(entry, "meta"), "title") == "Turn failed" {
			foundFailedMeta = true
			if got, want := transcriptMapString(entry, "turnId"), "turn-1"; got != want {
				t.Fatalf("failed meta turnId = %q, want parent: %#v", got, entry)
			}
			if got, want := transcriptMapString(entry, "backendTurnId"), "turn_bgtask-task-ci"; got != want {
				t.Fatalf("failed meta backendTurnId = %q, want wake turn: %#v", got, entry)
			}
		}
	}
	if !foundWakePrompt || !foundFailedMeta {
		t.Fatalf("parent body missing wake prompt or failed meta: prompt=%v failed=%v entries=%#v", foundWakePrompt, foundFailedMeta, body.Entries)
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

func isBackgroundWakeChip(entry map[string]any, wantPrompt string) bool {
	if transcriptMapString(entry, "metaKind") != "background_task_wake" {
		return false
	}
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		return false
	}
	got, _ := payload["prompt"].(string)
	return got == wantPrompt
}
