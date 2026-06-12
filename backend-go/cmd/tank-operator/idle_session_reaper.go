package main

// The durable idle-session reaper. Replaces the per-replica in-memory
// reaper that lived in sessions.Manager: its WebSocket guard (TrackWS) was
// dead code, its only activity feed was the SPA's visible-tab touch loop,
// and its clocks reset on every deploy — so it would have deleted any
// unattended-but-live session (an autonomous agent mid-task, a session
// parked on a durable wake, an MCP-spawned run) after idleTimeout of
// replica uptime while never reaping genuinely abandoned sessions across
// frequent deploys. Pod deletion is terminal by design, which made the old
// shape a latent destroyer of live work (2026-06-12 audit, issue #1079).
//
// Idleness here is durable: the registry's ClaimIdleForReap evaluates the
// whole predicate (updated_at past the cutoff, settled activity status, no
// pending wakes or undispatched launches) and claims the row in one
// conditional UPDATE, so any concurrent activity write defeats the reaper
// atomically. Both replicas may run this loop — the row claim collapses
// them, and pod deletion is idempotent. Manager.Delete is reused as the
// executor so pod removal, the (idempotent) registry mark, and the sidebar
// tombstone publish ride the exact same path a user-initiated delete does.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
)

// idleReapClaimer is the slice of the session registry the reaper needs;
// an interface so the loop is unit-testable around the DSN-gated store.
type idleReapClaimer interface {
	ClaimIdleForReap(ctx context.Context, cutoff time.Time, limit int) ([]sessionregistry.ReapedSession, error)
}

const idleReapBatchLimit = 20

func runIdleSessionReaper(ctx context.Context, app *appServer, claimer idleReapClaimer, interval, idleTimeout time.Duration) error {
	if app == nil || app.mgr == nil || claimer == nil || idleTimeout <= 0 {
		return nil
	}
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.reapIdleSessions(ctx, claimer, time.Now().UTC().Add(-idleTimeout)); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("idle session reap failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) reapIdleSessions(ctx context.Context, claimer idleReapClaimer, cutoff time.Time) error {
	claimed, err := claimer.ClaimIdleForReap(ctx, cutoff, idleReapBatchLimit)
	if err != nil {
		return err
	}
	for _, row := range claimed {
		// The row is already durably invisible; Delete removes the pod,
		// re-marks idempotently, and publishes the sidebar tombstone.
		if err := s.mgr.Delete(ctx, row.Email, row.SessionID); err != nil {
			recordIdleSessionReaped("delete_failed")
			slog.Warn("idle session reap: pod delete failed (row already claimed; pod retried next tick by k8s GC or manual delete)",
				"session_id", row.SessionID,
				"owner", row.Email,
				"pod", row.PodName,
				"error", err,
			)
			continue
		}
		recordIdleSessionReaped("deleted")
		slog.Info("idle session reaped",
			"session_id", row.SessionID,
			"owner", row.Email,
			"pod", row.PodName,
			"cutoff", cutoff,
		)
	}
	return nil
}
