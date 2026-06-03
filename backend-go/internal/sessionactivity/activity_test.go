package sessionactivity

import (
	"testing"
)

// TestDeriveActivitySummaryStartsAtReady checks the no-events case so
// fresh sessions surface the "ready" indicator instead of carrying
// whatever zero-value the struct defaults to.
func TestDeriveActivitySummaryStartsAtReady(t *testing.T) {
	got := DeriveActivitySummary(nil, nil, 0, false)
	if got.Status != "ready" {
		t.Fatalf("status = %q, want ready", got.Status)
	}
	if got.UnreadCount != 0 || got.NeedsInput || got.Failed {
		t.Fatalf("unexpected non-zero fields: %+v", got)
	}
}

// TestDeriveActivitySummaryFoldsTurnLifecycle confirms each event type
// applies the same per-field mutation the sidebar's status pill expects.
// This is the load-bearing fold the persister calls on every chat event;
// regressions here surface as the sidebar getting stuck in stale states.
func TestDeriveActivitySummaryFoldsTurnLifecycle(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.claimed", "turn_id": "turn-1", "order_key": "2"},
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "3"},
		// AskUserQuestion ends the asking turn awaiting input.
		{"type": "turn.awaiting_input", "turn_id": "turn-1", "order_key": "4"},
		// The answer is a brand-new turn; submitting + completing it clears
		// the sticky needs_input.
		{"type": "turn.submitted", "turn_id": "turn-2", "order_key": "5"},
		{"type": "turn.completed", "turn_id": "turn-2", "order_key": "6"},
	}
	got := DeriveActivitySummary(nil, events, 0, false)
	if got.Status != "ready" {
		t.Fatalf("final status after completed = %q, want ready", got.Status)
	}
	if got.ActiveTurnID != nil {
		t.Fatalf("active turn id after completion = %v, want nil", *got.ActiveTurnID)
	}
	if got.NeedsInput {
		t.Fatalf("needs_input stayed sticky after the answer turn completed")
	}
}

func TestDeriveActivitySummaryClaimedIsWorkingState(t *testing.T) {
	got := DeriveActivitySummary(nil, []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.claimed", "turn_id": "turn-1", "order_key": "2"},
	}, 0, false)
	if got.Status != "claimed" {
		t.Fatalf("status after claimed = %q, want claimed", got.Status)
	}
	if got.ActiveTurnID == nil || *got.ActiveTurnID != "turn-1" {
		t.Fatalf("ActiveTurnID after claimed = %#v, want turn-1", got.ActiveTurnID)
	}
}

// TestDeriveActivitySummaryAwaitingInputSetsNeedsInput pins the new
// AskUserQuestion fold: turn.awaiting_input ends the asking turn (no active
// turn) and raises the needs_input indicator until the user answers.
func TestDeriveActivitySummaryAwaitingInputSetsNeedsInput(t *testing.T) {
	got := DeriveActivitySummary(nil, []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "2"},
		{"type": "turn.awaiting_input", "turn_id": "turn-1", "order_key": "3"},
	}, 0, false)
	if got.Status != "needs_input" {
		t.Fatalf("status = %q, want needs_input", got.Status)
	}
	if !got.NeedsInput {
		t.Fatalf("NeedsInput = false, want true after turn.awaiting_input")
	}
	if got.ActiveTurnID != nil {
		t.Fatalf("active turn id = %v, want nil (the asking turn ended)", *got.ActiveTurnID)
	}
}

func TestDeriveActivitySummaryFailedFromPodOverridesStatus(t *testing.T) {
	prior := &ActivitySummary{Status: "ready"}
	got := DeriveActivitySummary(prior, nil, 0, true)
	if !got.Failed {
		t.Fatalf("failed=true should propagate when pod is Failed")
	}
	if got.Status != "error" {
		t.Fatalf("status = %q, want error when failed_from_pod is true", got.Status)
	}
}

// TestActivitySummariesEqualIgnoresUpdatedAt: drift in the informational
// updated_at field shouldn't cause the persister to emit a no-op delta.
// (UpdatedAt is the human "last touched" timestamp; the indicator-state
// fields are the emit-or-skip predicate.)
func TestActivitySummariesEqualIgnoresUpdatedAt(t *testing.T) {
	a := ActivitySummary{Status: "ready"}
	older := "2026-05-16T00:00:00Z"
	newer := "2026-05-16T00:00:01Z"
	a.UpdatedAt = &older
	b := a
	b.UpdatedAt = &newer
	if !ActivitySummariesEqual(a, b) {
		t.Fatalf("summaries differing only in UpdatedAt should compare equal")
	}
}

func TestActivitySummariesEqualDetectsIndicatorChanges(t *testing.T) {
	base := ActivitySummary{Status: "ready"}
	t.Run("status", func(t *testing.T) {
		other := base
		other.Status = "streaming"
		if ActivitySummariesEqual(base, other) {
			t.Fatal("status change should make summaries unequal")
		}
	})
	t.Run("active_turn_id", func(t *testing.T) {
		other := base
		id := "turn-x"
		other.ActiveTurnID = &id
		if ActivitySummariesEqual(base, other) {
			t.Fatal("active_turn_id change should make summaries unequal")
		}
	})
	t.Run("unread_count", func(t *testing.T) {
		other := base
		other.UnreadCount = 5
		if ActivitySummariesEqual(base, other) {
			t.Fatal("unread_count change should make summaries unequal")
		}
	})
	t.Run("failed", func(t *testing.T) {
		other := base
		other.Failed = true
		if ActivitySummariesEqual(base, other) {
			t.Fatal("failed change should make summaries unequal")
		}
	})
}

// TestDeriveActivitySummaryFoldsInterruptRequestedToStopping pins the
// activity fold for the durable stop boundary. A turn.interrupt_requested
// after turn.started transitions status → "stopping" while preserving
// ActiveTurnID (the turn is still mid-flight). The subsequent terminal
// event (turn.interrupted) resolves it to "stopped". See
// scripts/check-stop-request-migration.mjs for the completion contract.
func TestDeriveActivitySummaryFoldsInterruptRequestedToStopping(t *testing.T) {
	requested := DeriveActivitySummary(nil, []map[string]any{
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.interrupt_requested", "turn_id": "turn-1", "order_key": "2"},
	}, 0, false)
	if requested.Status != "stopping" {
		t.Fatalf("status after interrupt_requested = %q, want stopping", requested.Status)
	}
	if requested.ActiveTurnID == nil || *requested.ActiveTurnID != "turn-1" {
		t.Fatalf("ActiveTurnID cleared while stopping; want turn-1, got %#v", requested.ActiveTurnID)
	}
	if requested.Failed {
		t.Fatalf("Failed should not flip on stop request; got %+v", requested)
	}

	terminal := DeriveActivitySummary(nil, []map[string]any{
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.interrupt_requested", "turn_id": "turn-1", "order_key": "2"},
		{"type": "turn.interrupted", "turn_id": "turn-1", "order_key": "3"},
	}, 0, false)
	if terminal.Status != "stopped" {
		t.Fatalf("status after interrupted = %q, want stopped", terminal.Status)
	}
	if terminal.ActiveTurnID != nil {
		t.Fatalf("ActiveTurnID not cleared after stopped; got %#v", terminal.ActiveTurnID)
	}
}

func TestDeriveActivitySummaryIgnoresLateInterruptRequestedAfterTerminal(t *testing.T) {
	tests := []struct {
		name       string
		terminal   map[string]any
		wantStatus string
		wantFailed bool
	}{
		{
			name:       "completed stays ready",
			terminal:   map[string]any{"type": "turn.completed", "turn_id": "turn-1", "order_key": "2"},
			wantStatus: "ready",
		},
		{
			name:       "failed stays error",
			terminal:   map[string]any{"type": "turn.failed", "turn_id": "turn-1", "order_key": "2"},
			wantStatus: "error",
			wantFailed: true,
		},
		{
			name:       "command failed stays error",
			terminal:   map[string]any{"type": "turn.command_failed", "turn_id": "turn-1", "order_key": "2"},
			wantStatus: "error",
			wantFailed: true,
		},
		{
			name:       "interrupted stays stopped",
			terminal:   map[string]any{"type": "turn.interrupted", "turn_id": "turn-1", "order_key": "2"},
			wantStatus: "stopped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stats := DeriveActivitySummaryWithStats(nil, []map[string]any{
				{"type": "turn.started", "turn_id": "turn-1", "order_key": "1"},
				tt.terminal,
				{"type": "turn.interrupt_requested", "turn_id": "turn-1", "order_key": "3"},
			}, 0, false)
			if got.Status != tt.wantStatus {
				t.Fatalf("status after terminal + late interrupt = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Failed != tt.wantFailed {
				t.Fatalf("failed after terminal + late interrupt = %v, want %v", got.Failed, tt.wantFailed)
			}
			if got.ActiveTurnID != nil {
				t.Fatalf("ActiveTurnID after terminal + late interrupt = %#v, want nil", got.ActiveTurnID)
			}
			if got.LastOrderKey == nil || *got.LastOrderKey != "3" {
				t.Fatalf("LastOrderKey after late interrupt = %#v, want 3", got.LastOrderKey)
			}
			if len(stats.LateInterruptIgnoredStatuses) != 1 || stats.LateInterruptIgnoredStatuses[0] != tt.wantStatus {
				t.Fatalf("LateInterruptIgnoredStatuses = %#v, want [%q]", stats.LateInterruptIgnoredStatuses, tt.wantStatus)
			}
		})
	}
}

func TestDeriveActivitySummaryIgnoresLateStartedAfterTerminalForSameTurn(t *testing.T) {
	got := DeriveActivitySummary(nil, []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.interrupted", "turn_id": "turn-1", "order_key": "2"},
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "3"},
	}, 0, false)
	if got.Status != "stopped" {
		t.Fatalf("status after late started = %q, want stopped", got.Status)
	}
	if got.ActiveTurnID != nil {
		t.Fatalf("ActiveTurnID after late started = %#v, want nil", got.ActiveTurnID)
	}
}

func TestIsLifecycleChatEventTypeAllowlist(t *testing.T) {
	for _, allowed := range LifecycleChatEventTypes {
		if !IsLifecycleChatEventType(allowed) {
			t.Fatalf("%q not recognized as lifecycle chat event", allowed)
		}
	}
	if IsLifecycleChatEventType("item.started") {
		t.Fatal("item.started is not a sidebar-indicator chat event type")
	}
	// Migration guard: item.failed used to flip the session pill to
	// "error" on every failed tool call, which left healthy mid-turn
	// sessions pinned red. The session-level error pill is owned by
	// turn-terminal events and pod state. Re-adding item.failed to the
	// activity-fold allowlist re-introduces the bug; this assertion
	// blocks the regression.
	if IsLifecycleChatEventType("item.failed") {
		t.Fatal("item.failed is not a session-level activity event; tool errors are item-scoped, see DeriveActivitySummary docs")
	}
}

// TestDeriveActivitySummaryIgnoresItemFailedMidTurn pins the new
// contract: a tool returning is_error mid-turn does NOT flip the
// session pill to error. The agent typically continues after a failed
// tool call, and the previous behavior (item.failed → Status="error",
// Failed=true) left healthy sessions visually pinned to "Failed" until
// the next turn.submitted reset it.
//
// Session-level error stays owned by turn.failed / turn.command_failed
// (covered by other tests) and by failedFromPod (covered by
// TestDeriveActivitySummaryFailedFromPodOverridesStatus). The per-item
// error badge in the transcript renders independently from the same
// item.failed event on the wire.
func TestDeriveActivitySummaryIgnoresItemFailedMidTurn(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "2"},
		// A tool call errored. Under the old behavior the next two
		// assertions would fail — Status would be "error" and Failed
		// would be true even though the turn is still running.
		{"type": "item.failed", "turn_id": "turn-1", "order_key": "3"},
	}
	midTurn := DeriveActivitySummary(nil, events, 0, false)
	if midTurn.Status != "streaming" {
		t.Fatalf("status after mid-turn item.failed = %q, want streaming (the turn is still running)", midTurn.Status)
	}
	if midTurn.Failed {
		t.Fatalf("Failed should not flip on per-tool errors; got %+v", midTurn)
	}
	if midTurn.ActiveTurnID == nil || *midTurn.ActiveTurnID != "turn-1" {
		t.Fatalf("ActiveTurnID cleared by item.failed; want turn-1, got %#v", midTurn.ActiveTurnID)
	}

	// The turn completes cleanly. Even though an item failed inside
	// it, the session ends at "ready" — the agent handled the tool
	// error and produced a turn.completed.
	completed := DeriveActivitySummary(nil, append(events, map[string]any{
		"type": "turn.completed", "turn_id": "turn-1", "order_key": "4",
	}), 0, false)
	if completed.Status != "ready" {
		t.Fatalf("status after turn.completed (with prior item.failed) = %q, want ready", completed.Status)
	}
	if completed.Failed {
		t.Fatal("Failed should be false after a clean turn.completed even if an item.failed appeared mid-turn")
	}
	if completed.ActiveTurnID != nil {
		t.Fatalf("ActiveTurnID not cleared on turn.completed; got %#v", completed.ActiveTurnID)
	}
}

// TestDeriveActivitySummaryTurnFailedStillErrors is a positive guard for
// the legitimate error path. Item-level errors are filtered out of the
// fold, but turn.failed (durable turn-terminal failure) MUST still
// produce session-level error. If this test ever flips green by setting
// Status to something else, the fix went too far.
func TestDeriveActivitySummaryTurnFailedStillErrors(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.submitted", "turn_id": "turn-1", "order_key": "1"},
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "2"},
		{"type": "turn.failed", "turn_id": "turn-1", "order_key": "3"},
	}
	got := DeriveActivitySummary(nil, events, 0, false)
	if got.Status != "error" {
		t.Fatalf("status after turn.failed = %q, want error", got.Status)
	}
	if !got.Failed {
		t.Fatal("Failed should be true after turn.failed")
	}
	if got.ActiveTurnID != nil {
		t.Fatalf("ActiveTurnID should clear on turn.failed; got %#v", got.ActiveTurnID)
	}
}
