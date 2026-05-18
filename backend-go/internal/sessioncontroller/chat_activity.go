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

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionactivity"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// ChatActivityEmitter holds the dependencies the chat → sidebar bridge
// needs at steady state.
type ChatActivityEmitter struct {
	Writer     *RowWriter
	ChatEvents store.SessionEventStore
	ReadStates store.ConversationReadStateStore
	Registry   SessionToOwnerResolver
	Rows       RowFetcher
	Metrics    LifecycleEmitterMetrics
	Scope      string
}

// SessionToOwnerResolver maps a public session id to its owner email
// so the emitter can address the right per-owner subject. Satisfied by
// sessionregistry.Store.
type SessionToOwnerResolver interface {
	OwnerForSession(ctx context.Context, scope, sessionID string) (string, error)
}

// LifecycleEmitterMetrics keeps the activity-delta emitter observable
// without coupling to prometheus directly.
type LifecycleEmitterMetrics interface {
	RecordActivityDelta(emitted bool)
	RecordActivityFailure()
}

type noopLifecycleEmitterMetrics struct{}

func (noopLifecycleEmitterMetrics) RecordActivityDelta(_ bool) {}
func (noopLifecycleEmitterMetrics) RecordActivityFailure()     {}

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
	if !sessionactivity.IsLifecycleChatEventType(eventType) {
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

	next := sessionactivity.DeriveActivitySummary(prior, folded, unread, failedFromPod)
	if prior != nil && sessionactivity.ActivitySummariesEqual(*prior, next) {
		metrics.RecordActivityDelta(false)
		return nil
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
