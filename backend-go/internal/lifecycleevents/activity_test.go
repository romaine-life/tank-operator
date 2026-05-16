package lifecycleevents

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
		{"type": "turn.started", "turn_id": "turn-1", "order_key": "2"},
		{"type": "tool.approval_requested", "turn_id": "turn-1", "order_key": "3"},
		{"type": "tool.approval_resolved", "turn_id": "turn-1", "order_key": "4"},
		{"type": "turn.completed", "turn_id": "turn-1", "order_key": "5"},
	}
	got := DeriveActivitySummary(nil, events, 0, false)
	if got.Status != "ready" {
		t.Fatalf("final status after completed = %q, want ready", got.Status)
	}
	if got.ActiveTurnID != nil {
		t.Fatalf("active turn id after completion = %v, want nil", *got.ActiveTurnID)
	}
	if got.NeedsInput {
		t.Fatalf("needs_input stayed sticky after resolved approval")
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

func TestIsLifecycleChatEventTypeAllowlist(t *testing.T) {
	for _, allowed := range LifecycleChatEventTypes {
		if !IsLifecycleChatEventType(allowed) {
			t.Fatalf("%q not recognized as lifecycle chat event", allowed)
		}
	}
	if IsLifecycleChatEventType("item.started") {
		t.Fatal("item.started is not a sidebar-indicator chat event type")
	}
}
