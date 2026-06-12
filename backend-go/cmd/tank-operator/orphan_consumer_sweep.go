package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
)

// Orphan-consumer sweep cadence. The 5-minute initial delay lets
// pre-deploy session pods reconnect and re-register their consumers
// after an orchestrator restart, so a freshly-restarted orchestrator
// doesn't briefly see existing consumers as orphans during the
// reconnect window. The 15-minute MinAge inside SweepOrphanConsumers
// is the second safety net for the same race; the initial delay
// shaves the first sweep down to safer ground regardless.
//
// The third guard lives on the liveness-set side: the sweep's live set
// is sessionregistry.ListLiveIDsForScope — visible sessions plus any
// session updated within the last 24 h
// (sessionregistry.DefaultLiveSessionRecencyWindow). Sessions rows are
// soft-deleted (visible=false, updated_at bumped), so the recency
// union keeps a just-deleted session's consumers protected while its
// runner drains; MinAge cannot cover that race because it runs on the
// consumer's creation clock, not the row's deletion clock.
const (
	orphanSweepInitialDelay = 5 * time.Minute
	orphanSweepInterval     = 1 * time.Hour
	orphanSweepPassTimeout  = 90 * time.Second
)

// startOrphanConsumerSweeps runs the durable cleanup loop that
// removes stranded JetStream consumers from the TANK_SESSION_BUS
// stream. Background: every session owns two durable consumers (data
// plane + control plane per provider); the runner-side
// ensureConsumer / ensureControlConsumer only creates them, so
// deleted sessions leak consumers indefinitely. The 2026-05-25
// observation was 725 consumers for 6 live sessions, eating ~50 % of
// the JetStream RAM budget. CLAUDE.md's migration audit checklist
// names this exact failure mode; this loop is the durable
// remediation.
//
// Skipped when either dependency is nil — stub-mode local dev has
// no Postgres pool to list live sessions from and no real JetStream
// stream to sweep.
func startOrphanConsumerSweeps(
	ctx context.Context,
	bus *sessionbus.Bus,
	registry *sessionregistry.Store,
	scope string,
) {
	if bus == nil || registry == nil {
		slog.Info("orphan consumer sweep: skipped (no bus or registry)",
			"scope", scope,
			"bus_wired", bus != nil,
			"registry_wired", registry != nil,
		)
		return
	}
	go runOrphanConsumerSweepLoop(ctx, bus, registry, scope)
}

func runOrphanConsumerSweepLoop(
	ctx context.Context,
	bus *sessionbus.Bus,
	registry *sessionregistry.Store,
	scope string,
) {
	// Initial delay so existing runners can re-register their
	// consumers post-orchestrator-restart before we treat anything
	// as orphan.
	select {
	case <-ctx.Done():
		return
	case <-time.After(orphanSweepInitialDelay):
	}

	runOnce := func() {
		passCtx, cancel := context.WithTimeout(ctx, orphanSweepPassTimeout)
		defer cancel()

		live, err := registry.ListLiveIDsForScope(passCtx, sessionregistry.DefaultLiveSessionRecencyWindow)
		if err != nil {
			slog.Warn("orphan consumer sweep: list live sessions failed",
				"scope", scope, "error", err)
			recordSessionBusOrphanSweepResult("error")
			return
		}

		result, err := bus.SweepOrphanConsumers(passCtx, sessionbus.SweepConfig{
			LiveSessionIDs: live,
		})
		if err != nil {
			slog.Warn("orphan consumer sweep failed",
				"scope", scope, "error", err)
			recordSessionBusOrphanSweepResult("error")
			return
		}
		promSweepMetrics{}.RecordSweepPass(result)
		recordSessionBusOrphanSweepResult("ok")
		slog.Info("orphan consumer sweep complete",
			"scope", scope,
			"live_session_ids", len(live),
			"scanned", result.Scanned,
			"skipped_out_of_scope", result.SkippedOutOfScope,
			"skipped_live", result.SkippedLive,
			"skipped_too_young", result.SkippedTooYoung,
			"orphans", result.Orphans,
			"deleted", result.Deleted,
			"errors", result.Errors,
		)
	}

	runOnce()
	ticker := time.NewTicker(orphanSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
