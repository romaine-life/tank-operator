// Package sessionactivity is the chat-event → sidebar-indicator fold:
// the per-session "what state is the conversation in right now"
// summary rendered by the sidebar's per-row chips and dots. The
// session-bus persister calls DeriveActivitySummary after upserting
// a chat event so the SessionController's chat-activity emitter can
// decide whether to update the sessions row's activity_summary
// column.
//
// History: this fold used to live alongside the durable typed-event
// ledger that docs/session-list-redesign.md retires. Phase 4 lifted
// the activity types into their own package so the chat-activity
// logic stays while the ledger goes away.
package sessionactivity

import (
	"strings"
)

// ActivitySummary is the per-session fold the sidebar renders.
type ActivitySummary struct {
	Status       string  `json:"status"`
	ActiveTurnID *string `json:"active_turn_id"`
	NeedsInput   bool    `json:"needs_input"`
	Failed       bool    `json:"failed"`
	LastOrderKey *string `json:"last_order_key"`
	UnreadCount  int     `json:"unread_count"`
	UpdatedAt    *string `json:"updated_at"`
}

// DeriveActivitySummary applies the chat-event lifecycle fold the sidebar
// used to compute on every poll of the retired activity-polling
// endpoint. Called by the session-bus persister after upserting a chat
// event so it can decide whether to emit a session.activity_changed
// lifecycle row.
//
// Inputs:
//   - prior: the previous summary (the value last written into a
//     session.activity_changed payload) or nil if none yet.
//   - events: chat events in ascending order_key, scoped to the lifecycle
//     event_types that drive sidebar indicators (turn.* + tool.approval_*
//     + item.failed). The caller is responsible for the filter — same
//     event-type allowlist the activity handler used.
//   - unreadCount: separately queried (DISTINCT timeline_id / turn_id
//     counts past the read cursor) and passed in. The persister updates
//     this on every emit; the SSE consumer takes it from the latest
//     activity_changed payload.
//   - failedFromPod: true when the durable pod status is "Failed". Pod-
//     state failures land in their own session.pod_failed events, but the
//     activity summary still surfaces failed=true so the sidebar pill
//     and the chat-row error indicator stay consistent.
//
// Returns the new summary. The caller compares it to prior; identical
// summaries skip the row write (avoids storms of no-op rows on every
// keystroke-level chat event).
func DeriveActivitySummary(prior *ActivitySummary, events []map[string]any, unreadCount int, failedFromPod bool) ActivitySummary {
	out := ActivitySummary{Status: "ready"}
	if prior != nil {
		out = *prior
	}
	for _, event := range events {
		orderKey := stringField(event, "order_key")
		if orderKey != "" {
			out.LastOrderKey = stringPtr(orderKey)
		}
		if updatedAt := firstStringField(event, "created_at", "written_at", "timestamp", "time"); updatedAt != "" {
			out.UpdatedAt = stringPtr(updatedAt)
		}
		switch stringField(event, "type") {
		case "turn.submitted":
			out.Status = "submitted"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = false
			out.Failed = false
		case "turn.started":
			out.Status = "streaming"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = false
			out.Failed = false
		case "turn.completed":
			out.Status = "ready"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = false
		case "turn.failed", "turn.command_failed":
			out.Status = "error"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = true
		case "turn.interrupt_requested":
			// Stop has been requested but the turn is still mid-flight;
			// keep ActiveTurnID. The terminal event (turn.interrupted /
			// completed / failed / command_failed) resolves this later.
			out.Status = "stopping"
			out.NeedsInput = false
			out.Failed = false
		case "turn.interrupted":
			out.Status = "stopped"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = false
		case "item.failed":
			out.Status = "error"
			out.Failed = true
		case "tool.approval_requested":
			out.Status = "needs_input"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = true
		case "tool.approval_resolved":
			out.NeedsInput = false
			if out.ActiveTurnID != nil {
				out.Status = "streaming"
			} else {
				out.Status = "ready"
			}
		}
	}
	out.UnreadCount = unreadCount
	if failedFromPod {
		out.Failed = true
		if out.Status != "error" && out.Status != "needs_input" {
			out.Status = "error"
		}
	}
	return out
}

// ActivitySummariesEqual is the persister's emit-or-skip predicate. Two
// summaries that compare equal would produce identical sidebar pills, so
// the persister skips the lifecycle-row write.
func ActivitySummariesEqual(a, b ActivitySummary) bool {
	if a.Status != b.Status {
		return false
	}
	if !stringPtrEqual(a.ActiveTurnID, b.ActiveTurnID) {
		return false
	}
	if a.NeedsInput != b.NeedsInput {
		return false
	}
	if a.Failed != b.Failed {
		return false
	}
	if !stringPtrEqual(a.LastOrderKey, b.LastOrderKey) {
		return false
	}
	if a.UnreadCount != b.UnreadCount {
		return false
	}
	// UpdatedAt is informational — two summaries with the same indicator
	// state should compare equal even if UpdatedAt drifted by a few ms.
	return true
}

// LifecycleChatEventTypes is the chat event_type allowlist that drives
// activity-summary deltas. Kept identical to the prior
// store.LifecycleEventTypes (and to the fold cases in DeriveActivitySummary
// above) so the persister filter, the read-side fold, and the test
// fixtures all stay in sync.
var LifecycleChatEventTypes = []string{
	"turn.submitted",
	"turn.started",
	"turn.completed",
	"turn.failed",
	"turn.command_failed",
	"turn.interrupt_requested",
	"turn.interrupted",
	"item.failed",
	"tool.approval_requested",
	"tool.approval_resolved",
}

// IsLifecycleChatEventType is a sugar wrapper used by the persister's
// pre-filter.
func IsLifecycleChatEventType(eventType string) bool {
	for _, allowed := range LifecycleChatEventTypes {
		if eventType == allowed {
			return true
		}
	}
	return false
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringField(m, key); value != "" {
			return value
		}
	}
	return ""
}

func optionalStringField(m map[string]any, key string) *string {
	if value := stringField(m, key); value != "" {
		return stringPtr(value)
	}
	return nil
}

func stringPtr(value string) *string {
	v := value
	return &v
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
