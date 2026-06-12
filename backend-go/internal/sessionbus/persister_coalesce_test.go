package sessionbus

import (
	"testing"
)

func coalesceIn(eventType, orderKey string, inserted bool) *inflightSessionEvent {
	return &inflightSessionEvent{
		inserted: inserted,
		event: map[string]any{
			"type":      eventType,
			"order_key": orderKey,
		},
	}
}

// TestCoalesceActivityEventsLastPerClass pins the #1077-item-7 contract: a
// flood batch produces exactly one activity emit per refresh class — the
// LAST inserted event of that class — never one emit per event. The
// derivation each emit triggers (RefreshSessionActivity's unread counts,
// LatestLifecycleEvents fold) recomputes from durable state, so the last
// event subsumes the rest of its class.
func TestCoalesceActivityEventsLastPerClass(t *testing.T) {
	batch := []*inflightSessionEvent{
		coalesceIn("turn.submitted", "001", true),
		coalesceIn("item.completed", "002", true), // not an activity class: no emit
		coalesceIn("context.compacted", "003", true),
		coalesceIn("turn.claimed", "004", true),
		coalesceIn("user_message.created", "005", true),
		coalesceIn("turn.completed", "006", true),
		coalesceIn("turn.failed", "007", false), // duplicate insert: must not emit
	}
	got := coalesceActivityEvents(batch)
	if len(got) != 3 {
		t.Fatalf("coalesced emits = %d, want 3 (one per class present): %v", len(got), got)
	}
	// Deterministic class order: lifecycle, compaction, user_message.
	wantOrderKeys := []string{"006", "003", "005"}
	for i, want := range wantOrderKeys {
		if gotKey, _ := got[i]["order_key"].(string); gotKey != want {
			t.Fatalf("emit[%d] order_key = %q, want %q (last-of-class in emit order)", i, got[i]["order_key"], want)
		}
	}
}

// TestCoalesceActivityEventsDuplicateOnlyBatchEmitsNothing — a batch of
// redelivered (inserted=false) events keeps the pre-coalescing behavior of
// skipping every side effect.
func TestCoalesceActivityEventsDuplicateOnlyBatchEmitsNothing(t *testing.T) {
	batch := []*inflightSessionEvent{
		coalesceIn("turn.completed", "001", false),
		coalesceIn("context.compacted", "002", false),
	}
	if got := coalesceActivityEvents(batch); len(got) != 0 {
		t.Fatalf("duplicate-only batch coalesced to %d emits, want 0", len(got))
	}
}

// TestCoalesceActivityEventsNonActivityBatchEmitsNothing — item/stream flood
// events (the #1051 class) never reach the emitter; the activity classes are
// the ONLY emit triggers. Returning "" from the shared classifier and
// skipping the emit are equivalent because the emitter's gate is the same
// classifier.
func TestCoalesceActivityEventsNonActivityBatchEmitsNothing(t *testing.T) {
	batch := []*inflightSessionEvent{
		coalesceIn("item.started", "001", true),
		coalesceIn("item.completed", "002", true),
		coalesceIn("shell_task.updated", "003", true),
	}
	if got := coalesceActivityEvents(batch); len(got) != 0 {
		t.Fatalf("non-activity batch coalesced to %d emits, want 0", len(got))
	}
}
