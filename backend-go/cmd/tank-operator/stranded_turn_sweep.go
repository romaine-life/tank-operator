package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

const (
	// strandedTurnSweepInterval. Unlike the launch sweep's cheap lone-event
	// probe, FindStrandedTurns scans the full 30-day turn.submitted window
	// with three correlated subqueries — at the launch sweep's 60s cadence on
	// both replicas it kept the B1ms instance pinned after the historical
	// drain (sustained TankPgConnectionPollFailing / select_session_events
	// P99, observed 2026-06-11 post-#1055). Stranding detection tolerates
	// large latency by construction: candidates already sit ≥30 minutes
	// behind the quiet-window floor, so a 15-minute cadence adds at most
	// ~50% to time-to-terminal while cutting steady-state query load 15×
	// per replica.
	strandedTurnSweepInterval = 15 * time.Minute
	// strandedTurnMinAgeSubmitted is the age floor for a never-claimed strand.
	// A healthy runner claims a deliverable submit_turn within seconds (it is
	// a queue pop), so thirty quiet minutes is many multiples of headroom. The
	// quiet-session predicate in FindStrandedTurns carries the real safety: a
	// turn legitimately queued behind a long-running turn lives in a session
	// that keeps producing events and is never a candidate.
	strandedTurnMinAgeSubmitted = 30 * time.Minute
	// strandedTurnMinAgeProgressed is the (much longer) floor for a
	// claimed/started turn with no terminal — a runner that died mid-turn.
	// Live long turns emit ledger events (items, turn.usage) continuously;
	// requiring BOTH two hours since submit AND a fully silent session for
	// the quiet window makes a false positive require a runner that is alive
	// yet has produced nothing durable for half an hour, which the runner's
	// own 240s provider-stall terminal already rules out.
	strandedTurnMinAgeProgressed = 2 * time.Hour
	// strandedTurnQuietWindow is how long the whole session must have been
	// silent (zero session_events of any kind) before any of its turns are
	// candidates.
	strandedTurnQuietWindow = 30 * time.Minute
	// strandedTurnMaxAge bounds the historical scan off the deep ledger,
	// matching the launch sweep's rationale: a strand older than this
	// predates the sweep and its pod is long gone.
	strandedTurnMaxAge = 30 * 24 * time.Hour
	// strandedTurnBatchLimit caps rows per tick so a first-deploy backlog
	// drains over a few ticks.
	strandedTurnBatchLimit = 100

	// strandedTurnReasonCommandLost is the durable terminal reason for a
	// never-claimed user turn: the submit_turn command was recorded and
	// published but no runner ever claimed it (runner-side MaxDeliver
	// exhaustion, a dead runner process, or a publish that never landed).
	strandedTurnReasonCommandLost = "submit_command_lost: the turn was durably submitted but no runner ever claimed it; the submit_turn command was lost (dead or wedged runner, or command-consumer exhaustion). Resubmit the message."
	// strandedTurnReasonProgressLost is the durable terminal reason for a
	// claimed/started turn whose runner died mid-turn without a terminal.
	strandedTurnReasonProgressLost = "turn_progress_lost: a runner claimed this turn but stopped producing durable events and never wrote a terminal (runner process died mid-turn). Resubmit the message."
)

// runStrandedTurnSweepLoop is the command-plane four-outcome backstop: every
// durable turn.submitted must be followed by exactly one durable terminal,
// and when the runner side dies — a crashed runner process, a submit_turn
// command that exhausted its runner-consumer deliveries, a pod that wedged —
// nothing else ever writes that terminal. The 2026-06-11 incident
// (tank-operator#1051) stranded five sessions exactly this way: turns durably
// submitted/streaming for hours with idle runners, visible only as stuck
// chips and only diagnosable with kubectl.
//
// The sweep converts each silent strand into a durable turn.command_failed so
// the SPA renders it failed and the composer unblocks. It does NOT re-drive
// the command: the runner's state for the turn is unknown or gone, and a
// re-publish could double-run a turn whose work partially happened. Surfacing
// the failure is the honest outcome; the user (or the wake machinery) decides
// whether to resubmit.
//
// Safety mirrors the stranded-launch sweep, plus the quiet-session predicate:
//   - candidates require the WHOLE session silent for strandedTurnQuietWindow,
//     so a turn queued behind a long-running turn (live session, events
//     flowing) or itself mid-work can never be swept;
//   - never-claimed strands need strandedTurnMinAgeSubmitted; claimed strands
//     need strandedTurnMinAgeProgressed on top of the silence;
//   - turn.command_failed is terminal, so a late runner racing the sweep hits
//     the already-terminal guard and drops the stray command;
//   - the event_id is deterministic in turn_id, so both replicas collapse to
//     one row at the session_events_event_identity unique index (real
//     since migration 0151).
func runStrandedTurnSweepLoop(ctx context.Context, app *appServer, interval time.Duration) error {
	if app == nil || app.sessionEvents == nil {
		return nil
	}
	if interval <= 0 {
		interval = strandedTurnSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.processStrandedTurns(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("stranded turn sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) processStrandedTurns(ctx context.Context, now time.Time) error {
	if s == nil || s.sessionEvents == nil {
		return nil
	}
	olderThan := now.Add(-strandedTurnMinAgeSubmitted)
	quietSince := now.Add(-strandedTurnQuietWindow)
	notBefore := now.Add(-strandedTurnMaxAge)
	rows, err := s.sessionEvents.FindStrandedTurns(ctx, olderThan, quietSince, notBefore, strandedTurnBatchLimit)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		// Pipeline-liveness gate. turn.submitted rows land over HTTP
		// directly, so a persister / session-bus outage makes every active
		// session look quiet while submits keep accumulating — without this
		// gate the sweep would mass-fail healthy in-flight turns exactly
		// when the pipeline is recovering. Runner progress anywhere in the
		// fleet within the quiet window proves events can flow; until it
		// does, candidates stay candidates (they re-qualify on a later tick)
		// and nothing is written. The gate prefers a delayed terminal over a
		// false one: a genuine strand on an otherwise idle fleet waits for
		// the next runner event anywhere before it is failed, which is the
		// first moment a user is looking again.
		alive, liveErr := s.sessionEvents.HasRecentRunnerEvent(ctx, quietSince)
		if liveErr != nil {
			return liveErr
		}
		if !alive {
			for range rows {
				recordStrandedTurnSwept("deferred_pipeline_quiet")
			}
			slog.Warn("stranded turn sweep deferred: no runner events fleet-wide in the quiet window",
				"candidates", len(rows),
				"quiet_since", quietSince,
			)
			return nil
		}
	}
	for _, row := range rows {
		if row.Progressed && now.Sub(row.CreatedAt) < strandedTurnMinAgeProgressed {
			// A claimed turn gets the longer floor; it stays a candidate on
			// later ticks if the session stays silent.
			recordStrandedTurnSwept("deferred_progressed")
			continue
		}
		if err := s.failStrandedTurn(ctx, row, now); err != nil {
			slog.Warn("stranded turn fail-mark failed",
				"session_id", row.SessionID,
				"turn_id", row.TurnID,
				"error", err)
			// Best-effort per row; a transient write error retries next tick
			// because the turn still has no terminal.
			continue
		}
	}
	return nil
}

// strandedTurnReason picks the terminal reason: continuation turns
// (background-task wakes, scheduled wakeups) get the away-error reason so the
// sidebar rings the summon bell — the agent promised to resume while the user
// was away and the resume silently died — while ordinary user turns fail
// plainly.
func strandedTurnReason(row store.StrandedTurn) string {
	source := strings.TrimSpace(row.Source)
	if isBackgroundWakeTurnID(strings.TrimSpace(row.TurnID)) ||
		source == string(conversation.TurnSubmittedSourceBackgroundTask) ||
		source == string(conversation.TurnSubmittedSourceScheduleWakeup) {
		return sessionactivity.AwayErrorReasonStrandedContinuation
	}
	if row.Progressed {
		return strandedTurnReasonProgressLost
	}
	return strandedTurnReasonCommandLost
}

// failStrandedTurn emits the durable turn.command_failed for one stranded
// turn via the same backend-event path the launch sweep uses, so transcript
// materialization, activity refresh, and the SSE wake all fire and the SPA
// flips the turn to failed live.
func (s *appServer) failStrandedTurn(ctx context.Context, row store.StrandedTurn, now time.Time) error {
	sessionID := strings.TrimSpace(row.SessionID)
	turnID := strings.TrimSpace(row.TurnID)
	if sessionID == "" || turnID == "" {
		recordStrandedTurnSwept("skipped_incomplete")
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
		Reason:            strandedTurnReason(row),
		Now:               now,
	})
	if err := s.persistBackendEvent(ctx, storageKey, failed); err != nil {
		recordStrandedTurnSwept("persist_error")
		return err
	}
	recordStrandedTurnSwept("failed")
	slog.Warn("stranded turn swept to durable terminal",
		"session_id", sessionID,
		"turn_id", turnID,
		"progressed", row.Progressed,
		"source", strings.TrimSpace(row.Source),
		"age", now.Sub(row.CreatedAt).Truncate(time.Second).String(),
	)
	return nil
}
