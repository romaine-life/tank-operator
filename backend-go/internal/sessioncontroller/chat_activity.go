// Package sessioncontroller — ChatActivityEmitter bridges chat-event
// upserts into sidebar activity-summary deltas. The session-bus
// persister calls EmitChatActivityDelta after a successful upsert;
// the emitter folds the new event into the running activity summary,
// compares against the prior summary stored on the sessions row's
// activity_summary column, and only writes + publishes when something
// the sidebar would render has changed.
//
// History: pre-Phase-4 (docs/session-list-redesign.md) the prior
// activity came out of the retired lifecycle ledger's LatestActivity
// reader and each delta produced a typed event row. Phase 4 drops
// the ledger entirely; prior activity now comes from the sessions
// row's activity_summary jsonb column, and deltas are direct row
// UPDATEs via RowWriter.
package sessioncontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// ChatActivityEmitter holds the dependencies the chat → sidebar bridge
// needs at steady state.
type ChatActivityEmitter struct {
	Writer     *RowWriter
	ChatEvents store.SessionEventStore
	ReadStates store.ConversationReadStateStore
	Registry   SessionToOwnerResolver
	Rows       RowFetcher
	Wakes      PendingWakeChecker
	Metrics    LifecycleEmitterMetrics
	Scope      string
}

// SessionToOwnerResolver maps a public session id to its owner email
// so the emitter can address the right per-owner subject. Satisfied by
// sessionregistry.Store.
type SessionToOwnerResolver interface {
	OwnerForSession(ctx context.Context, scope, sessionID string) (string, error)
}

// PendingWakeChecker answers whether a session currently has self-scheduled
// work pending — a registered ScheduleWakeup timer or a run_in_background wake
// that has not yet fired, failed, or been cancelled. The emitter uses it to
// fold a would-be-"ready" turn terminal into the non-summoning "scheduled"
// status: a parked agent is mid-(simulated)-turn, not idle, so it must not
// paint the "your turn" dot or fire the turn-complete summon. Satisfied by the
// combined scheduled-wakeup + background-task-wake stores. See
// docs/scheduled-turn-continuity.md.
type PendingWakeChecker interface {
	HasPendingWake(ctx context.Context, scope, sessionID string) (bool, error)
}

// LifecycleEmitterMetrics keeps the activity-delta emitter observable
// without coupling to prometheus directly.
//
// RecordActivityErrorTransition fires when the session pill flips into
// "error" from a non-error prior. The reason label localizes the cause
// so a dashboard can answer "why did this session pill go red":
//   - "pod_failed": the session pod entered the Failed state.
//   - "turn_failed": the runner published a provider/agent failure.
//   - "turn_command_failed": the backend's submit/interrupt command
//     fabric failed durably.
//
// Per docs/quality-timeframes.md "Missing counters for user-trust
// failures." Without this, a future regression that introduces a new
// path-to-error (or the historical item.failed inference) is invisible
// in dashboards until a user complains.
//
// RecordCompaction fires once per newly-observed durable context.compaction,
// labeled by provider and trigger, so a dashboard can answer "how often are
// sessions compacting, automatically vs on /compact". The exact per-session
// total is the durable compaction_count column; this counter is the rate view.
type LifecycleEmitterMetrics interface {
	RecordActivityDelta(emitted bool)
	RecordActivityFailure()
	RecordActivityErrorTransition(reason string)
	RecordActivityLateInterruptIgnored(status string)
	RecordCompaction(provider, trigger string)
}

type noopLifecycleEmitterMetrics struct{}

func (noopLifecycleEmitterMetrics) RecordActivityDelta(_ bool)                  {}
func (noopLifecycleEmitterMetrics) RecordActivityFailure()                      {}
func (noopLifecycleEmitterMetrics) RecordActivityErrorTransition(_ string)      {}
func (noopLifecycleEmitterMetrics) RecordActivityLateInterruptIgnored(_ string) {}
func (noopLifecycleEmitterMetrics) RecordCompaction(_, _ string)                {}

// contextCompactedEventType is the durable Tank event the runner emits when the
// provider summarized earlier context to reclaim window space. It is
// intentionally NOT in sessionactivity.LifecycleChatEventTypes — it must not
// perturb run status, active turn, or unread state — so the emitter handles it
// on a dedicated branch that refreshes the durable compaction count.
const contextCompactedEventType = "context.compacted"

// userMessageCreatedEventType is the durable Tank event written once per human
// back-and-forth submission. Like context.compacted it is intentionally NOT in
// sessionactivity.LifecycleChatEventTypes (it must not perturb run status,
// active turn, or unread state), so the emitter handles it on a dedicated branch
// that refreshes the durable per-session user-message count. Background-task
// wake continuations carry their prompt on turn.submitted, not
// user_message.created, so they never advance this count.
const userMessageCreatedEventType = "user_message.created"

// activityErrorReason picks the label for
// LifecycleEmitterMetrics.RecordActivityErrorTransition. Pod-state
// failures take precedence (a Failed pod is the most-severe cause and
// would also produce a turn-terminal failure if the runner is still
// reachable). Otherwise scan the most-recently-folded events for a
// durable turn-terminal failure type. "unknown" is a defensive fallback
// — if a new path-to-error is introduced without updating this switch,
// it'll show up as unknown on the dashboard rather than silently
// miscounted.
func activityErrorReason(failedFromPod bool, folded []map[string]any) string {
	if failedFromPod {
		return "pod_failed"
	}
	for i := len(folded) - 1; i >= 0; i-- {
		t, _ := folded[i]["type"].(string)
		switch t {
		case "turn.failed":
			return "turn_failed"
		case "turn.command_failed":
			return "turn_command_failed"
		}
	}
	return "unknown"
}

// EmitChatActivityDelta is the sessionbus.LifecycleEmitter contract.
// Returns nil on no-op (delta unchanged); returns an error only on
// store/publish failures the caller should log. The row UPDATE goes
// through RowWriter.RecordTransition so the activity_summary column
// write and the NATS row-update publish happen as one operation.
func (e *ChatActivityEmitter) EmitChatActivityDelta(ctx context.Context, event map[string]any) error {
	if e == nil || event == nil {
		return nil
	}
	metrics := e.Metrics
	if metrics == nil {
		metrics = noopLifecycleEmitterMetrics{}
	}
	eventType, _ := event["type"].(string)
	// Class partition is shared with the persister's per-batch
	// coalescing — see sessionactivity.ChatActivityDeltaClass.
	class := sessionactivity.ChatActivityDeltaClass(eventType)
	if class == "" {
		return nil
	}
	storageKey, _ := event["tank_session_id"].(string)
	publicID, _ := event["session_id"].(string)
	if publicID == "" {
		publicID = strings.TrimSpace(strings.TrimPrefix(storageKey, e.Scope+":"))
	}
	if publicID == "" {
		return nil
	}
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(e.Scope, publicID)
	}

	owner, err := e.Registry.OwnerForSession(ctx, e.Scope, publicID)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: resolve owner for session %q: %w", publicID, err)
	}
	if owner == "" {
		// Session row was deleted (or never registered) — nothing to
		// emit; the sidebar dropped the row on session.deleted.
		return nil
	}
	switch class {
	case sessionactivity.ActivityClassCompaction:
		return e.refreshCompactionCount(ctx, owner, publicID, event)
	case sessionactivity.ActivityClassUserMessage:
		return e.refreshUserMessageCount(ctx, owner, publicID)
	}
	return e.RefreshSessionActivity(ctx, owner, publicID)
}

// refreshCompactionCount recomputes the durable per-session compaction count
// from the append-only session_events ledger and writes it onto the sessions
// row when it advances. context.compacted is delivered at-least-once;
// recompute-and-compare makes a redelivery a no-op — no row_version churn and
// no double-count of the metric. The exact per-session total always lives in
// the durable compaction_count column; the Prometheus counter increments only
// on a genuine advance, labeled by the triggering event's provider and trigger.
func (e *ChatActivityEmitter) refreshCompactionCount(ctx context.Context, owner, publicID string, event map[string]any) error {
	metrics := e.Metrics
	if metrics == nil {
		metrics = noopLifecycleEmitterMetrics{}
	}
	if e.ChatEvents == nil || e.Writer == nil {
		return nil
	}
	count, err := e.ChatEvents.CountContextCompactions(ctx, publicID)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: count compactions for %q: %w", publicID, err)
	}
	// Compare against the durable prior so an at-least-once redelivery that
	// recomputes the same total neither bumps row_version nor double-counts the
	// metric. A missing row means the session was deleted mid-flight.
	prior := int64(-1)
	if e.Rows != nil {
		record, ok, rErr := e.Rows.Get(ctx, owner, publicID)
		if rErr != nil {
			metrics.RecordActivityFailure()
			return fmt.Errorf("chat-activity emitter: read row for compaction %q: %w", publicID, rErr)
		}
		if !ok {
			return nil
		}
		prior = record.CompactionCount
	}
	if prior >= 0 && count <= prior {
		return nil
	}
	transition := Event{
		Email:        owner,
		SessionScope: e.Scope,
		SessionID:    publicID,
		Type:         EventTypeCompactionChanged,
		Payload:      map[string]any{"compaction_count": count},
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	outcome, err := e.Writer.RecordTransition(ctx, transition)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: record compaction transition for %q: %w", publicID, err)
	}
	if outcome == TransitionNoOp {
		return nil
	}
	metrics.RecordCompaction(compactionProviderLabel(event), compactionTriggerLabel(event))
	if outcome == TransitionPublishFailed {
		slog.Warn("chat-activity emitter: compaction row committed but publish failed",
			"session_id", publicID,
			"owner", owner,
			"scope", e.Scope,
		)
	}
	return nil
}

// refreshUserMessageCount recomputes the durable per-session user-message count
// (one per human back-and-forth) from the append-only session_events ledger and
// writes it onto the sessions row when it advances. user_message.created is
// delivered at-least-once; recompute-and-compare makes a redelivery a no-op — no
// row_version churn and no rewrite. The exact per-session total always lives in
// the durable user_message_count column as row metadata. Mirrors
// refreshCompactionCount but emits no metric: the durable column is the
// observable, and a per-message counter would be high-churn / low-signal.
func (e *ChatActivityEmitter) refreshUserMessageCount(ctx context.Context, owner, publicID string) error {
	metrics := e.Metrics
	if metrics == nil {
		metrics = noopLifecycleEmitterMetrics{}
	}
	if e.ChatEvents == nil || e.Writer == nil {
		return nil
	}
	count, err := e.ChatEvents.CountUserMessages(ctx, publicID)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: count user messages for %q: %w", publicID, err)
	}
	// Compare against the durable prior so an at-least-once redelivery that
	// recomputes the same total neither bumps row_version nor rewrites the row.
	// A missing row means the session was deleted mid-flight.
	prior := int64(-1)
	if e.Rows != nil {
		record, ok, rErr := e.Rows.Get(ctx, owner, publicID)
		if rErr != nil {
			metrics.RecordActivityFailure()
			return fmt.Errorf("chat-activity emitter: read row for user-message count %q: %w", publicID, rErr)
		}
		if !ok {
			return nil
		}
		prior = record.UserMessageCount
	}
	if prior >= 0 && count <= prior {
		return nil
	}
	transition := Event{
		Email:        owner,
		SessionScope: e.Scope,
		SessionID:    publicID,
		Type:         EventTypeUserMessageCountChanged,
		Payload:      map[string]any{"user_message_count": count},
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	outcome, err := e.Writer.RecordTransition(ctx, transition)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: record user-message-count transition for %q: %w", publicID, err)
	}
	if outcome == TransitionPublishFailed {
		slog.Warn("chat-activity emitter: user-message-count row committed but publish failed",
			"session_id", publicID,
			"owner", owner,
			"scope", e.Scope,
		)
	}
	return nil
}

// compactionProviderLabel and compactionTriggerLabel clamp the metric labels to
// a bounded set so a malformed runner event cannot blow up cardinality.
// provider rides the durable envelope's "source"; trigger rides
// payload.trigger (Claude distinguishes auto vs a manual /compact; Codex
// reports auto).
func compactionProviderLabel(event map[string]any) string {
	source, _ := event["source"].(string)
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return "other"
	}
}

func compactionTriggerLabel(event map[string]any) string {
	payload, _ := event["payload"].(map[string]any)
	trigger := ""
	if payload != nil {
		trigger, _ = payload["trigger"].(string)
	}
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "auto":
		return "auto"
	case "manual":
		return "manual"
	default:
		return "other"
	}
}

// RefreshSessionActivity recomputes and publishes the session row's activity
// summary from durable chat events plus durable read-state. Chat-event writes
// call this through EmitChatActivityDelta; read-state updates call it directly
// so the sidebar's unread badge clears through the same durable sessions row
// update path instead of waiting for the next turn event.
func (e *ChatActivityEmitter) RefreshSessionActivity(ctx context.Context, owner, publicID string) error {
	if e == nil {
		return nil
	}
	metrics := e.Metrics
	if metrics == nil {
		metrics = noopLifecycleEmitterMetrics{}
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	publicID = strings.TrimSpace(publicID)
	if owner == "" || publicID == "" {
		return nil
	}
	// Read prior activity and pod-status from the sessions row. Phase 4
	// dropped the lifecycle-store LatestActivity / LatestPodStatus
	// reads; the row is the only durable source now.
	var prior *sessionactivity.ActivitySummary
	failedFromPod := false
	if e.Rows != nil {
		record, ok, err := e.Rows.Get(ctx, owner, publicID)
		if err != nil {
			metrics.RecordActivityFailure()
			return fmt.Errorf("chat-activity emitter: read row for %q: %w", publicID, err)
		}
		if !ok {
			return nil
		}
		if len(record.ActivitySummary) > 0 {
			var parsed sessionactivity.ActivitySummary
			if err := json.Unmarshal(record.ActivitySummary, &parsed); err == nil {
				prior = &parsed
			}
		}
		if record.Status == "Failed" {
			failedFromPod = true
		}
	}

	folded, err := e.ChatEvents.LatestLifecycleEvents(ctx, publicID, 50)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: read recent chat events for %q: %w", publicID, err)
	}
	readOrderKey := ""
	if e.ReadStates != nil {
		rs, rErr := e.ReadStates.Get(ctx, owner, publicID)
		if rErr != nil {
			metrics.RecordActivityFailure()
			return fmt.Errorf("chat-activity emitter: read state for %q: %w", publicID, rErr)
		}
		if rs != nil {
			readOrderKey = rs.LastReadOrderKey
		}
	}
	unread, err := e.ChatEvents.UnreadOutputCount(ctx, publicID, readOrderKey)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: unread count for %q: %w", publicID, err)
	}

	next, foldStats := sessionactivity.DeriveActivitySummaryWithStats(prior, folded, unread, failedFromPod)
	for _, status := range foldStats.LateInterruptIgnoredStatuses {
		metrics.RecordActivityLateInterruptIgnored(status)
	}
	resolvedLateInterrupt := false
	next, resolvedLateInterrupt, err = e.resolveStoppingActivityFromTerminal(ctx, publicID, next)
	if err != nil {
		metrics.RecordActivityFailure()
		return err
	}
	if resolvedLateInterrupt {
		metrics.RecordActivityLateInterruptIgnored(next.Status)
	}
	next, err = e.applyScheduledWakeOverride(ctx, publicID, next, foldStats.BackgroundWorkPending)
	if err != nil {
		metrics.RecordActivityFailure()
		return err
	}
	if prior != nil && sessionactivity.ActivitySummariesEqual(*prior, next) {
		metrics.RecordActivityDelta(false)
		return nil
	}

	// Bump the error-transition counter on a non-error → error edge.
	// Reason label localizes the cause: pod state, durable turn-terminal
	// failure event, or backend command-fabric failure. The fold's only
	// other path into "error" is failedFromPod, hence the precedence:
	// pod state wins if both are present in the same emit.
	if next.Status == "error" {
		priorWasError := prior != nil && prior.Status == "error"
		if !priorWasError {
			metrics.RecordActivityErrorTransition(activityErrorReason(failedFromPod, folded))
		}
	}

	summaryPayload, err := json.Marshal(next)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: marshal summary for %q: %w", publicID, err)
	}
	var summaryMap map[string]any
	if err := json.Unmarshal(summaryPayload, &summaryMap); err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: roundtrip summary for %q: %w", publicID, err)
	}

	transition := Event{
		Email:        owner,
		SessionScope: e.Scope,
		SessionID:    publicID,
		Type:         EventTypeActivityChanged,
		Payload:      summaryMap,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	outcome, err := e.Writer.RecordTransition(ctx, transition)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: record transition for %q: %w", publicID, err)
	}
	metrics.RecordActivityDelta(true)
	if outcome == TransitionPublishFailed {
		slog.Warn("chat-activity emitter: publish failed but row committed",
			"session_id", publicID,
			"owner", owner,
			"scope", e.Scope,
		)
	}
	return nil
}

func (e *ChatActivityEmitter) resolveStoppingActivityFromTerminal(
	ctx context.Context,
	publicID string,
	summary sessionactivity.ActivitySummary,
) (sessionactivity.ActivitySummary, bool, error) {
	if summary.Status != "stopping" || summary.ActiveTurnID == nil || strings.TrimSpace(*summary.ActiveTurnID) == "" {
		return summary, false, nil
	}
	terminal, err := e.ChatEvents.FindTurnTerminal(ctx, publicID, strings.TrimSpace(*summary.ActiveTurnID))
	if err != nil {
		return summary, false, fmt.Errorf("chat-activity emitter: terminal lookup for stopping turn %q in session %q: %w",
			strings.TrimSpace(*summary.ActiveTurnID), publicID, err)
	}
	if terminal == nil {
		return summary, false, nil
	}
	switch strings.TrimSpace(fmt.Sprint(terminal["type"])) {
	case "turn.completed":
		summary.Status = "ready"
		summary.Failed = false
	case "turn.failed", "turn.command_failed":
		summary.Status = "error"
		summary.Failed = true
	case "turn.interrupted":
		summary.Status = "stopped"
		summary.Failed = false
	default:
		return summary, false, nil
	}
	summary.ActiveTurnID = nil
	summary.NeedsInput = false
	return summary, true, nil
}

// applyScheduledWakeOverride folds the would-be-"ready" idle state into the
// non-summoning "scheduled" status when the session has self-scheduled work
// pending (a ScheduleWakeup timer or a run_in_background wake). A parked agent
// is mid-(simulated)-turn — it will resume itself on a clock/event — so it must
// not paint the "your turn" idle dot or fire the turn-complete summon. This is
// the emitter sibling of resolveStoppingActivityFromTerminal: a post-fold,
// DB-derived status adjustment.
//
// The override only touches the ready<->scheduled boundary. Any active status
// (submitted/claimed/streaming/needs_input/stopping) and any terminal failure
// (error/stopped, including a Failed pod, which the fold already applied) take
// precedence and are returned untouched without querying — keeping the
// pending-wake lookup lazy. It is bidirectional so a recompute after the wake
// fires/fails/cancels (pending=false) restores "ready". A nil checker (degraded
// boot before Postgres) never strands a session in "scheduled". See
// docs/scheduled-turn-continuity.md.
func (e *ChatActivityEmitter) applyScheduledWakeOverride(
	ctx context.Context,
	publicID string,
	summary sessionactivity.ActivitySummary,
	backgroundWorkPending bool,
) (sessionactivity.ActivitySummary, error) {
	if summary.Status != "ready" && summary.Status != "scheduled" {
		return summary, nil
	}
	// Two independent sources can park a would-be-ready turn into the non-summoning
	// "scheduled" status, unified here so the bidirectional fold stays correct:
	// (1) a Tank-owned wake row (ScheduleWakeup / background-task wake) for
	// Claude/Codex, read from the durable wake tables; (2) the runner's
	// payload.background_work_pending on the latest turn.completed for a
	// self-managing agent (antigravity), which owns and fires its own work and so has
	// no Tank wake row. A nil checker (degraded boot before Postgres) never strands a
	// session in "scheduled" — pending stays false unless the runner annotated it.
	// See docs/scheduled-turn-continuity.md and
	// backend-go/cmd/antigravity-runner/ARCHITECTURE.md.
	pending := backgroundWorkPending
	if !pending && e.Wakes != nil {
		wakePending, err := e.Wakes.HasPendingWake(ctx, e.Scope, publicID)
		if err != nil {
			return summary, fmt.Errorf("chat-activity emitter: pending-wake check for %q: %w", publicID, err)
		}
		pending = wakePending
	}
	if pending && summary.Status == "ready" {
		summary.Status = "scheduled"
	} else if !pending && summary.Status == "scheduled" {
		summary.Status = "ready"
	}
	return summary, nil
}
