package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type sessionActivityRefresher interface {
	RefreshSessionActivity(ctx context.Context, owner, scope, sessionID string) error
}

type scopedSessionActivityRefresher struct {
	pool       *pgxpool.Pool
	publisher  sessioncontroller.RowUpdatePublisher
	localScope string
	local      *sessioncontroller.ChatActivityEmitter
}

func (r *scopedSessionActivityRefresher) RefreshSessionActivity(ctx context.Context, owner, scope, sessionID string) error {
	if r == nil {
		return nil
	}
	scope = normalizeSessionScope(scope)
	if r.local != nil && scope == normalizeSessionScope(r.localScope) {
		return r.local.RefreshSessionActivity(ctx, owner, sessionID)
	}
	if r.pool == nil || r.publisher == nil {
		return nil
	}

	registry := sessionregistry.NewPostgresStore(r.pool, scope)
	rowPublisher := &sessioncontroller.RowPublisher{
		Fetcher:   registry,
		Publisher: r.publisher,
		Scope:     scope,
	}
	rowWriter, err := sessioncontroller.NewRowWriter(
		rowPublisher,
		r.pool,
		promRowWriterMetrics{},
	)
	if err != nil {
		return err
	}
	emitter := &sessioncontroller.ChatActivityEmitter{
		Writer:     rowWriter,
		ChatEvents: store.NewPostgresSessionEventStore(r.pool, scope),
		ReadStates: store.NewPostgresConversationReadStateStore(r.pool, scope),
		Registry:   registry,
		Rows:       registry,
		Wakes: combinedWakeChecker{
			scheduled:  pgstore.NewScheduledWakeupStore(r.pool, scope),
			background: pgstore.NewBackgroundTaskWakeStore(r.pool, scope),
		},
		Metrics: promLifecycleEmitterMetrics{},
		Scope:   scope,
	}
	return emitter.RefreshSessionActivity(ctx, strings.ToLower(strings.TrimSpace(owner)), strings.TrimSpace(sessionID))
}

// combinedWakeChecker ORs the two durable self-scheduled-work tables behind the
// sessioncontroller.PendingWakeChecker the activity emitter uses to fold a
// parked session into the non-summoning "scheduled" status. A nil inner store
// (degraded boot before Postgres is wired) is skipped, never surfaced as an
// error, so the emitter falls back to ordinary "ready". See
// docs/scheduled-turn-continuity.md.
type combinedWakeChecker struct {
	scheduled  *pgstore.ScheduledWakeupStore
	background *pgstore.BackgroundTaskWakeStore
}

func (c combinedWakeChecker) HasPendingWake(ctx context.Context, scope, sessionID string) (bool, error) {
	if c.scheduled != nil {
		pending, err := c.scheduled.HasPending(ctx, scope, sessionID)
		if err != nil {
			return false, err
		}
		if pending {
			return true, nil
		}
	}
	if c.background != nil {
		return c.background.HasPending(ctx, scope, sessionID)
	}
	return false, nil
}
