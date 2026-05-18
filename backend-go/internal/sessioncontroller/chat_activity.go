// Package sessioncontroller consolidates the three writers that
// previously fed session_lifecycle_events independently — the
// pod-informer (K8s watch loop), the chat-activity emitter (chat-event
// persister hook), and Manager's user-action transitions — into one
// package that owns the durable row write, the lifecycle-ledger
// append, and the NATS publish on the per-(owner, scope) session-list
// subject. See docs/session-list-redesign.md for the long-form
// rationale and the four-phase migration plan this package is the
// Phase 1 vehicle for.
package sessioncontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// ChatActivityEmitter bridges chat-event upserts into sidebar
// session.activity_changed deltas. The session-bus persister calls
// EmitChatActivityDelta after a successful upsert; the emitter folds
// the new event into the running activity summary, compares against the
// previously-emitted summary for that session, and only writes +
// publishes when something the sidebar would render has changed.
type ChatActivityEmitter struct {
	Writer     *RowWriter
	ChatEvents store.SessionEventStore
	ReadStates store.ConversationReadStateStore
	Registry   SessionToOwnerResolver
	Metrics    LifecycleEmitterMetrics
	Scope      string
}

// SessionToOwnerResolver maps a public session id to its owner email
// so the emitter can address the right per-owner subject. Satisfied by
// sessionregistry.Store via a cmd-layer adapter.
type SessionToOwnerResolver interface {
	OwnerForSession(ctx context.Context, scope, sessionID string) (string, error)
}

// LifecycleEmitterMetrics keeps the activity-delta emitter observable
// without coupling to prometheus directly. Wired through observability.go.
type LifecycleEmitterMetrics interface {
	RecordActivityDelta(emitted bool)
	RecordActivityFailure()
}

type noopLifecycleEmitterMetrics struct{}

func (noopLifecycleEmitterMetrics) RecordActivityDelta(_ bool) {}
func (noopLifecycleEmitterMetrics) RecordActivityFailure()     {}

// EmitChatActivityDelta is the sessionbus.LifecycleEmitter contract.
// Returns nil on no-op (delta unchanged); returns an error only on
// store/publish failures the caller should log. All durable writes
// (ledger row + sessions row column update + NATS publish) go through
// e.Writer.RecordTransition so the Phase 1 dual-write invariant holds:
// every emit produces both a session_lifecycle_events row AND a
// sessions.activity_summary column update at the same row_version.
func (e *ChatActivityEmitter) EmitChatActivityDelta(ctx context.Context, event map[string]any) error {
	if e == nil || event == nil {
		return nil
	}
	metrics := e.Metrics
	if metrics == nil {
		metrics = noopLifecycleEmitterMetrics{}
	}
	eventType, _ := event["type"].(string)
	if !lifecycleevents.IsLifecycleChatEventType(eventType) {
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

	// Resolve owner — the chat event doesn't carry the email field
	// (intentionally; tank_session_id is the durable routing key). The
	// registry has the mapping.
	owner, err := e.Registry.OwnerForSession(ctx, e.Scope, publicID)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: resolve owner for session %q: %w", publicID, err)
	}
	if owner == "" {
		// Session row was deleted (or never registered) — nothing to
		// emit; the sidebar will drop the row on session.deleted.
		return nil
	}

	prior, err := e.Writer.Store.LatestActivity(ctx, e.Scope, publicID)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: read prior activity for %q: %w", publicID, err)
	}

	// Re-fold the last 50 chat lifecycle events (same bound the deleted
	// activity polling endpoint used). Cheap: indexed scan against
	// (tank_session_id, order_key) bounded by event_type ANY.
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
	failedFromPod := false
	if status, err := e.Writer.Store.LatestPodStatus(ctx, e.Scope, publicID); err == nil && status != nil && status.Status == "Failed" {
		failedFromPod = true
	}

	next := lifecycleevents.DeriveActivitySummary(prior, folded, unread, failedFromPod)
	if prior != nil && lifecycleevents.ActivitySummariesEqual(*prior, next) {
		metrics.RecordActivityDelta(false)
		return nil
	}

	triggerOrderKey, _ := event["order_key"].(string)
	if strings.TrimSpace(triggerOrderKey) == "" {
		// Defensive — chat events always carry order_key under the
		// post-#461 schema; if a row arrived without one, derive a
		// stable event_id from now() so the unique constraint still
		// holds.
		triggerOrderKey = fmt.Sprintf("ts:%d", time.Now().UnixNano())
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

	row := lifecycleevents.Event{
		Email:        owner,
		SessionScope: e.Scope,
		SessionID:    publicID,
		Type:         lifecycleevents.EventTypeActivityChanged,
		EventID:      fmt.Sprintf("activity_changed:%s", triggerOrderKey),
		Payload:      summaryMap,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	outcome, err := e.Writer.RecordTransition(ctx, row)
	if err != nil {
		metrics.RecordActivityFailure()
		return fmt.Errorf("chat-activity emitter: record transition for %q: %w", publicID, err)
	}
	if outcome == TransitionDeduped {
		// Trigger event_id already produced a row — another orchestrator
		// replica won the race. RowWriter already skipped the publish.
		metrics.RecordActivityDelta(false)
		return nil
	}
	metrics.RecordActivityDelta(true)
	if outcome == TransitionPublishFailed {
		slog.Warn("chat-activity emitter: publish failed but durable row committed",
			"session_id", publicID,
			"owner", owner,
			"scope", e.Scope,
		)
	}
	return nil
}
