package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionregistry"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
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
		Metrics:    promLifecycleEmitterMetrics{},
		Scope:      scope,
	}
	return emitter.RefreshSessionActivity(ctx, strings.ToLower(strings.TrimSpace(owner)), strings.TrimSpace(sessionID))
}
