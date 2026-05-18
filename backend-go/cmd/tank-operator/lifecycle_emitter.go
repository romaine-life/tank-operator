package main

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

// lifecycleListEventPublisher is the narrow publish surface
// lifecycleEmitter needs. *sessionbus.Bus satisfies it via
// PublishSessionListEvent. Kept as an interface here so unit tests can
// substitute an in-memory recorder.
type lifecycleListEventPublisher interface {
	PublishSessionListEvent(ctx context.Context, email, scope string, payload []byte) error
}

// lifecycleEmitter bridges chat-event upserts into sidebar
// session.activity_changed deltas. The session-bus persister calls
// EmitChatActivityDelta after a successful upsert; the emitter folds the
// new event into the running activity summary, compares against the
// previously-emitted summary for that session, and only writes + publishes
// when something the sidebar would render has changed.
//
// Why this lives in cmd/tank-operator rather than in lifecycleevents:
// it needs to read the chat ledger (`store.SessionEventStore`) to refold
// the last-N lifecycle markers, and to walk the registry to resolve
// session_id → owner_email. Keeping the cross-package wiring at the cmd
// layer matches every other bridge (the sessionbus.PersisterMetrics
// adapter lives here too).
type lifecycleEmitter struct {
	store          lifecycleevents.Store
	chatEvents     store.SessionEventStore
	readStates     store.ConversationReadStateStore
	registry       sessionToOwnerResolver
	publisher      lifecycleListEventPublisher
	metrics        lifecycleEmitterMetrics
	scope          string
}

// sessionToOwnerResolver maps a public session id to its owner email so
// the emitter can address the right per-owner subject. Satisfied by
// sessionregistry.Store via the cmd-layer adapter.
type sessionToOwnerResolver interface {
	OwnerForSession(ctx context.Context, scope, sessionID string) (string, error)
}

// lifecycleEmitterMetrics keeps the activity-delta emitter observable
// without coupling to prometheus directly. Wired through observability.go.
type lifecycleEmitterMetrics interface {
	RecordActivityDelta(emitted bool)
	RecordActivityFailure()
}

type noopLifecycleEmitterMetrics struct{}

func (noopLifecycleEmitterMetrics) RecordActivityDelta(_ bool) {}
func (noopLifecycleEmitterMetrics) RecordActivityFailure()     {}

// EmitChatActivityDelta is the sessionbus.LifecycleEmitter contract.
// Returns nil on no-op (delta unchanged); returns an error only on
// store/publish failures the caller should log.
func (e *lifecycleEmitter) EmitChatActivityDelta(ctx context.Context, event map[string]any) error {
	if e == nil || event == nil {
		return nil
	}
	eventType, _ := event["type"].(string)
	if !lifecycleevents.IsLifecycleChatEventType(eventType) {
		return nil
	}
	storageKey, _ := event["tank_session_id"].(string)
	publicID, _ := event["session_id"].(string)
	if publicID == "" {
		publicID = strings.TrimSpace(strings.TrimPrefix(storageKey, e.scope+":"))
	}
	if publicID == "" {
		return nil
	}
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(e.scope, publicID)
	}

	// Resolve owner — the chat event doesn't carry the email field
	// (intentionally; tank_session_id is the durable routing key). The
	// registry has the mapping.
	owner, err := e.registry.OwnerForSession(ctx, e.scope, publicID)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: resolve owner for session %q: %w", publicID, err)
	}
	if owner == "" {
		// Session row was deleted (or never registered) — nothing to
		// emit; the sidebar will drop the row on session.deleted.
		return nil
	}

	prior, err := e.store.LatestActivity(ctx, e.scope, publicID)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: read prior activity for %q: %w", publicID, err)
	}

	// Re-fold the last 50 chat lifecycle events (same bound the deleted
	// activity polling endpoint used). Cheap: indexed scan against
	// (tank_session_id, order_key) bounded by event_type ANY.
	folded, err := e.chatEvents.LatestLifecycleEvents(ctx, publicID, 50)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: read recent chat events for %q: %w", publicID, err)
	}
	readOrderKey := ""
	if e.readStates != nil {
		rs, rErr := e.readStates.Get(ctx, owner, publicID)
		if rErr != nil {
			e.metrics.RecordActivityFailure()
			return fmt.Errorf("lifecycle emitter: read state for %q: %w", publicID, rErr)
		}
		if rs != nil {
			readOrderKey = rs.LastReadOrderKey
		}
	}
	unread, err := e.chatEvents.UnreadOutputCount(ctx, publicID, readOrderKey)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: unread count for %q: %w", publicID, err)
	}
	failedFromPod := false
	if status, err := e.store.LatestPodStatus(ctx, e.scope, publicID); err == nil && status != nil && status.Status == "Failed" {
		failedFromPod = true
	}

	next := lifecycleevents.DeriveActivitySummary(prior, folded, unread, failedFromPod)
	if prior != nil && lifecycleevents.ActivitySummariesEqual(*prior, next) {
		e.metrics.RecordActivityDelta(false)
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
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: marshal summary for %q: %w", publicID, err)
	}
	var summaryMap map[string]any
	if err := json.Unmarshal(summaryPayload, &summaryMap); err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: roundtrip summary for %q: %w", publicID, err)
	}

	row := lifecycleevents.Event{
		Email:        owner,
		SessionScope: e.scope,
		SessionID:    publicID,
		Type:         lifecycleevents.EventTypeActivityChanged,
		EventID:      fmt.Sprintf("activity_changed:%s", triggerOrderKey),
		Payload:      summaryMap,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	assigned, alreadyExists, err := e.store.Append(ctx, row)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: append activity row for %q: %w", publicID, err)
	}
	if alreadyExists {
		// Trigger event_id already produced a row — another orchestrator
		// replica won the race. Skip publish to avoid double-rendering.
		e.metrics.RecordActivityDelta(false)
		return nil
	}
	e.metrics.RecordActivityDelta(true)

	wirePayload, err := json.Marshal(assigned)
	if err != nil {
		e.metrics.RecordActivityFailure()
		return fmt.Errorf("lifecycle emitter: marshal wire payload: %w", err)
	}
	if err := e.publisher.PublishSessionListEvent(ctx, owner, e.scope, wirePayload); err != nil {
		// Non-fatal: durable row already written; sidebar will catch up
		// on cursor-resume.
		slog.Warn("lifecycle emitter: publish failed",
			"session_id", publicID,
			"owner", owner,
			"scope", e.scope,
			"order_key", assigned.OrderKey,
			"error", err,
		)
	}
	return nil
}
