package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

const (
	// strandedLaunchSweepInterval is how often the backstop scans. Stranding
	// is a rare durability gap, not a latency-sensitive path, so a slow tick
	// is fine — the cost is purely "how long a stranded turn shows as pending
	// before it flips to failed."
	strandedLaunchSweepInterval = 60 * time.Second
	// strandedLaunchMinAge is the floor on a launch's age before the sweep
	// will touch it. It must comfortably exceed the worst-case duration of a
	// healthy attachment launch's browser-driven phase two
	// (waitForSessionReady → upload files → POST /turns). A normal launch
	// writes turn.submitted within milliseconds-to-seconds of pod-ready, so
	// 15 minutes is many multiples of headroom and makes a false positive on
	// a still-in-flight launch effectively impossible.
	strandedLaunchMinAge = 15 * time.Minute
	// strandedLaunchMaxAge bounds the historical scan so the sweep never
	// folds the deep ledger. A launch older than this is abandoned (its pod
	// is long gone) and not worth a terminal; the create-time user bubble
	// stays, which matches "session-pod death is terminal, not a durability
	// target" from the conversation protocol.
	strandedLaunchMaxAge = 30 * 24 * time.Hour
	// strandedLaunchBatchLimit caps rows processed per tick. Strands are
	// rare; a backlog (e.g. first run after deploying this) drains over a few
	// ticks rather than in one large transaction burst.
	strandedLaunchBatchLimit = 100
	// strandedLaunchFailureReason is the durable turn.command_failed reason.
	// Mirrors the wording the inline publish-failure path uses so the two
	// strand classes read consistently in the ledger.
	strandedLaunchFailureReason = "launch_never_dispatched: deferred launch turn recorded user_message.created but no turn.submitted; the create flow did not complete the upload + dispatch step"
)

// runStrandedLaunchSweepLoop is the durable backstop for stranded launch
// turns — the gap that left session 523 wedged: an attachment-backed launch
// (initial_turn.deferred=true) writes user_message.created at create time,
// then relies entirely on the browser tab to run phase two (wait for ready,
// upload the files into the workspace, POST /turns with
// existing_user_message=true) which is what writes turn.submitted and
// publishes the runnable submit_turn command. If that in-tab sequence never
// completes — tab closed/reloaded, network drop, an upload/dispatch error
// that only surfaced as a toast — the turn is left durably recorded but never
// dispatched: the runner parks on its first-turn await forever, and nothing
// server-side ever completed or failed it. There is no other publisher of a
// terminal for that turn, so without this loop the strand is permanent and
// invisible.
//
// The loop converts the silent strand into a durable turn.command_failed, so
// the SPA renders the launch as failed (and the user can retry) instead of an
// idle session with a lone user bubble. It does NOT re-drive the dispatch:
// the file bytes only ever lived in the originating browser, so a server-side
// re-dispatch would reference workspace paths that were never written.
// Surfacing the failure is the honest outcome.
//
// Safety:
//   - strandedLaunchMinAge keeps a healthy, still-in-flight launch out of the
//     candidate set, and any dispatched turn has its turn.submitted written
//     in the same backend call as user_message.created, so "lone
//     user_message.created older than the floor" is an unambiguous strand.
//   - turn.command_failed is a terminal event (conversation.IsTurnTerminalEvent),
//     so on the rare chance the browser dispatches late, the runner's
//     already-terminal guard (runner.ts finalizeCommandIfAlreadyTerminal)
//     drops the stray submit_turn — no double-run.
//   - the event_id is deterministic in turn_id, so both orchestrator replicas
//     running this loop collapse to one row at the
//     session_events_event_identity unique index (real since migration
//     0151; before that this comment described a constraint that did not
//     exist, and replica races double-wrote terminals) — no leader
//     election needed.
func runStrandedLaunchSweepLoop(ctx context.Context, app *appServer, interval time.Duration) error {
	if app == nil || app.sessionEvents == nil {
		return nil
	}
	if interval <= 0 {
		interval = strandedLaunchSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.processStrandedLaunches(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("stranded launch sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) processStrandedLaunches(ctx context.Context, now time.Time) error {
	if s == nil || s.sessionEvents == nil {
		return nil
	}
	olderThan := now.Add(-strandedLaunchMinAge)
	notBefore := now.Add(-strandedLaunchMaxAge)
	rows, err := s.sessionEvents.FindStrandedLaunchTurns(ctx, olderThan, notBefore, strandedLaunchBatchLimit)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.failStrandedLaunch(ctx, row, now); err != nil {
			slog.Warn("stranded launch fail-mark failed",
				"session_id", row.SessionID,
				"turn_id", row.TurnID,
				"error", err)
			// Best-effort per row; a transient write error retries on the
			// next tick because the row still has no terminal.
			continue
		}
	}
	return nil
}

// failStrandedLaunch emits the durable turn.command_failed for one stranded
// launch via the same backend-event path the inline publish-failure branch
// uses, so the transcript-row materialization, activity refresh, and SSE wake
// all fire and the SPA flips the turn to failed live.
func (s *appServer) failStrandedLaunch(ctx context.Context, row store.StrandedLaunchTurn, now time.Time) error {
	sessionID := strings.TrimSpace(row.SessionID)
	turnID := strings.TrimSpace(row.TurnID)
	if sessionID == "" || turnID == "" {
		// A launch row without an addressable turn/session can't carry a
		// well-formed terminal; skip rather than emit an unroutable event.
		recordStrandedLaunchSwept("skipped_incomplete")
		return nil
	}
	storageKey := strings.TrimSpace(row.TankSessionID)
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	}
	runtime := strings.TrimSpace(row.Runtime)
	if runtime == "" {
		runtime = "claude"
	}
	failed := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             strings.TrimSpace(row.Email),
		TurnID:            turnID,
		ClientNonce:       strings.TrimSpace(row.ClientNonce),
		Runtime:           runtime,
		Reason:            strandedLaunchFailureReason,
		Now:               now,
	})
	if err := s.persistBackendEvent(ctx, storageKey, failed); err != nil {
		recordStrandedLaunchSwept("persist_error")
		return err
	}
	recordStrandedLaunchSwept("failed")
	return nil
}
