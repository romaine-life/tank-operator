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
	AwayError    bool    `json:"away_error"`
	LastOrderKey *string `json:"last_order_key"`
	UnreadCount  int     `json:"unread_count"`
	UpdatedAt    *string `json:"updated_at"`
}

// ActivityFoldStats reports diagnostic facts observed while deriving the
// summary. The fold result remains the source of truth; stats are for
// bounded observability at the caller.
type ActivityFoldStats struct {
	LateInterruptIgnoredStatuses []string
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
//     event_types that drive sidebar indicators (the turn.* lifecycle set,
//     including turn.awaiting_input).
//     The caller is responsible for the filter — same event-type allowlist
//     the activity handler used. item.failed is intentionally NOT in this
//     set: a single failed tool call is an in-turn signal the agent will
//     usually recover from, not a session-level failure. Session-level
//     failure comes from turn.failed / turn.command_failed (durable turn
//     terminal events) or failedFromPod (pod state). See
//     docs/tank-conversation-protocol.md "State Machine" for the contract.
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
	out, _ := DeriveActivitySummaryWithStats(prior, events, unreadCount, failedFromPod)
	return out
}

// DeriveActivitySummaryWithStats is DeriveActivitySummary plus the bounded
// diagnostic facts needed by the production emitter.
func DeriveActivitySummaryWithStats(prior *ActivitySummary, events []map[string]any, unreadCount int, failedFromPod bool) (ActivitySummary, ActivityFoldStats) {
	out := ActivitySummary{Status: "ready"}
	stats := ActivityFoldStats{}
	if prior != nil {
		out = *prior
	}
	terminalTurns := map[string]bool{}
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
			if terminalTurns[stringField(event, "turn_id")] {
				continue
			}
			out.Status = "submitted"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = false
			out.Failed = false
		case "turn.claimed":
			if terminalTurns[stringField(event, "turn_id")] {
				continue
			}
			out.Status = "claimed"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = false
			out.Failed = false
		case "turn.started":
			if terminalTurns[stringField(event, "turn_id")] {
				continue
			}
			out.Status = "streaming"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = false
			out.Failed = false
		case "turn.completed":
			terminalTurns[stringField(event, "turn_id")] = true
			out.Status = "ready"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = false
		case "turn.failed", "turn.command_failed":
			terminalTurns[stringField(event, "turn_id")] = true
			out.Status = "error"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = true
			// A self-resume continuation (ScheduleWakeup / background-task wake)
			// that fails to fire is an "away error": the agent broke while the
			// user was not driving, so the projection marks it for the same
			// turn-complete summon a normal hand-off gets. Ordinary provider or
			// user-turn failures are not away errors. See
			// docs/scheduled-turn-continuity.md "The summon invariant".
			out.AwayError = isAwayErrorReason(terminalReason(event))
		case "turn.interrupt_requested":
			// Stop has been requested but the turn is still mid-flight;
			// keep ActiveTurnID. A late interrupt after a terminal event is
			// only an audit chip and must not downgrade ready/error/stopped
			// back to stopping.
			if canTransitionToStopping(out.Status) {
				out.Status = "stopping"
				out.NeedsInput = false
				out.Failed = false
			} else {
				stats.LateInterruptIgnoredStatuses = append(stats.LateInterruptIgnoredStatuses, out.Status)
			}
		case "turn.interrupted":
			terminalTurns[stringField(event, "turn_id")] = true
			out.Status = "stopped"
			out.ActiveTurnID = nil
			out.NeedsInput = false
			out.Failed = false
		case "turn.awaiting_input":
			// The agent asked the user a question and paused the same active
			// turn. The next turn.input_answered clears needs_input; a final
			// terminal still owns turn completion.
			out.Status = "needs_input"
			if id := optionalStringField(event, "turn_id"); id != nil {
				out.ActiveTurnID = id
			}
			out.NeedsInput = true
			out.Failed = false
		case "turn.input_answered":
			out.ActiveTurnID = nil
			out.Status = "ready"
			out.NeedsInput = false
			out.Failed = false
		}
	}
	out.UnreadCount = unreadCount
	if failedFromPod {
		out.Failed = true
		if out.Status != "error" && out.Status != "needs_input" {
			out.Status = "error"
		}
	}
	// away_error is meaningful only in the error state; clear it once the session
	// has moved off error so a later genuine "your turn" is not mislabeled as a
	// broken self-resume.
	if out.Status != "error" {
		out.AwayError = false
	}
	return out, stats
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
	if a.AwayError != b.AwayError {
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
// activity-summary deltas. Kept identical to store.LifecycleEventTypes
// (and to the fold cases in DeriveActivitySummary above) so the persister
// filter, the read-side fold, and the test fixtures all stay in sync.
//
// item.failed is intentionally excluded: a tool call returning is_error
// is an in-turn signal the agent typically handles and continues from.
// Folding it into the session pill produced a "permanent error pill on
// a healthy session" bug — see #TBD. The session-level error pill is
// driven by turn.failed / turn.command_failed (durable turn terminal
// events) and by failedFromPod. The per-item error badge in the
// transcript is unchanged and still renders from item.failed events on
// the wire. activity_test.go pins this exclusion as a migration guard;
// re-adding item.failed here will fail TestIsLifecycleChatEventType.
var LifecycleChatEventTypes = []string{
	"turn.submitted",
	"turn.claimed",
	"turn.started",
	"turn.completed",
	"turn.failed",
	"turn.command_failed",
	"turn.interrupt_requested",
	"turn.interrupted",
	"turn.awaiting_input",
	"turn.input_answered",
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

func canTransitionToStopping(status string) bool {
	switch status {
	case "submitted", "claimed", "streaming", "needs_input", "stopping":
		return true
	default:
		return false
	}
}

// AwayError reasons are the turn-terminal failure reasons that mean a
// self-managed continuation broke while the user was not driving the session:
// a ScheduleWakeup timer or a run_in_background wake the orchestrator failed to
// fire. The orchestrator stamps one of these on the durable turn.command_failed
// it emits from the wake fire paths (cmd/tank-operator). They ring the same
// turn-complete bell as a normal hand-off; ordinary provider/user-turn failures
// do not. See docs/scheduled-turn-continuity.md "The summon invariant".
const (
	AwayErrorReasonScheduledWakeup    = "schedule_wakeup_fire_failed"
	AwayErrorReasonBackgroundTaskWake = "background_task_wake_fire_failed"
)

func isAwayErrorReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case AwayErrorReasonScheduledWakeup, AwayErrorReasonBackgroundTaskWake:
		return true
	default:
		return false
	}
}

func terminalReason(event map[string]any) string {
	payload, _ := event["payload"].(map[string]any)
	if payload == nil {
		return ""
	}
	reason, _ := payload["reason"].(string)
	return strings.TrimSpace(reason)
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
