package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	ciWatchReconcileInterval = 5 * time.Minute
	ciWatchStaleAfter        = 12 * time.Minute
	ciWatchReconcileBatch    = 200
)

// runCIWatchReconcileLoop is the durable stranded-watch backstop. Webhooks are
// at-least-once and lossy, and the in-memory mergeability retry does not survive
// an orchestrator restart, so a 'watching' watch whose deciding delivery was
// dropped would otherwise hang forever (the 2026-06-17 stall class) -- keeping
// its session reaper-protected and asleep. Each pass re-drives only watches that
// have seen no event past the staleness window; a fresh live read resolves them
// or proves a genuine stall. Re-drive is the same idempotent conditional-write
// reconcile the webhook path uses, so a double pass (two replicas, or racing a
// webhook) is harmless. Healthy watches with events flowing are never touched,
// so this is a backstop, not a poll of CI. It also publishes the
// oldest-stale-age gauge the TankCIWatchStalled alert fires on.
func runCIWatchReconcileLoop(ctx context.Context, app *appServer, interval, staleAfter time.Duration) error {
	if app == nil || app.ciWatches == nil || app.mcpGitHub == nil {
		return nil
	}
	if interval <= 0 {
		interval = ciWatchReconcileInterval
	}
	if staleAfter <= 0 {
		staleAfter = ciWatchStaleAfter
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.reconcileStaleCIWatches(ctx, staleAfter); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("ci watch reconcile loop pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) reconcileStaleCIWatches(ctx context.Context, staleAfter time.Duration) error {
	if s == nil || s.ciWatches == nil {
		return nil
	}
	stale, err := s.ciWatches.ListStaleWatching(ctx, staleAfter, ciWatchReconcileBatch)
	if err != nil {
		return err
	}
	var oldest float64
	for _, watch := range stale {
		if !watch.RegisteredAt.IsZero() {
			if age := time.Since(watch.RegisteredAt).Seconds(); age > oldest {
				oldest = age
			}
		}
		recordCIWebhook("backstop", "stale_redrive")
		if _, err := s.reconcileAndApplyCIWatch(ctx, watch, ciWatchReconcileBackstop); err != nil {
			slog.Warn("ci watch backstop reconcile failed", "watch_id", watch.WatchID, "error", err)
		}
	}
	setCIWatchOldestStaleAge(oldest)
	return nil
}
