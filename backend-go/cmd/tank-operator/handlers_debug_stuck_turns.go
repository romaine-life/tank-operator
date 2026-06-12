// Admin-only debug surface for orchestrator-detected stuck turns, in
// two stall classes mirroring the sampler:
//
//   - phase=accepted: durable activity_summary submitted/claimed
//     (accepted, no provider progress) with updated_at older than
//     threshold_seconds.
//   - phase=streaming: durable activity_summary streaming whose last
//     session_events row is older than streaming_threshold_seconds —
//     the wedged-boundary class (turn open, ledger silent, no
//     terminal; sessions 828/829, tank-operator#1085).
//
// This is the per-entity localizer for the stuck-turn observability
// story. The aggregate signal is the tank_sessions_stuck_in_progress
// gauge (phase label) plus the TankSessionStuckInProgress alert; this
// endpoint resolves "which session_ids, for how long, with what
// provider rate-limit state" once the alert fires — without kubectl,
// per the observability contract (operators diagnose from /metrics +
// /api/debug + slog + Grafana alone).
//
// A row here means the runner did NOT fail the turn itself: it is the
// orchestrator-side complement to the runner's api_retry rate-limit
// terminal (PROVIDER_RETRY_STALL_MS, 240s). The accepted threshold
// default (10m) sits deliberately above 240s so a turn the runner-side
// terminal would have resolved never appears here. A streaming row is
// suspicion, not a verdict — a single long quiet tool call can
// legitimately exceed the threshold; inspect the listed session_id's
// runner logs, its session_events tail, and (for antigravity) the
// runner's turn-settle metrics to localize the cause.
//
// Auth: Tank admin power required. Emits a structured slog audit line
// per call.
package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/stuckturns"
)

const (
	debugStuckTurnsDefaultThresholdSeconds          = 600
	debugStuckTurnsDefaultStreamingThresholdSeconds = 1200
	debugStuckTurnsMinThresholdSeconds              = 60
	debugStuckTurnsMaxThresholdSeconds              = 86400
	debugStuckTurnsDefaultLimit                     = 100
	debugStuckTurnsMaxLimit                         = 500
)

func (s *appServer) handleDebugStuckTurns(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordDebugStuckTurnsRead("forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		recordDebugStuckTurnsRead("forbidden")
		writeError(w, status, scopeErr.Error())
		return
	}

	if s.pgPool == nil {
		recordDebugStuckTurnsRead("not_configured")
		writeError(w, http.StatusServiceUnavailable, "Postgres pool not wired")
		return
	}

	thresholdSeconds := clampedQueryInt(
		r.URL.Query().Get("threshold_seconds"),
		debugStuckTurnsDefaultThresholdSeconds,
		debugStuckTurnsMinThresholdSeconds,
		debugStuckTurnsMaxThresholdSeconds,
	)
	streamingThresholdSeconds := clampedQueryInt(
		r.URL.Query().Get("streaming_threshold_seconds"),
		debugStuckTurnsDefaultStreamingThresholdSeconds,
		debugStuckTurnsMinThresholdSeconds,
		debugStuckTurnsMaxThresholdSeconds,
	)
	limit := clampedQueryInt(
		r.URL.Query().Get("limit"),
		debugStuckTurnsDefaultLimit,
		1,
		debugStuckTurnsMaxLimit,
	)

	now := time.Now()
	lister := stuckturns.ListerFromQuery{Pool: s.pgPool}

	olderThan := now.Add(-time.Duration(thresholdSeconds) * time.Second).UTC().Format(time.RFC3339)
	accepted, err := lister.ListStuckTurns(r.Context(), scope, olderThan, limit)
	if err != nil {
		recordDebugStuckTurnsRead("store_error")
		slog.Warn("debug stuck-turns: accepted-class list failed",
			"caller_email", user.Email,
			"session_scope", scope,
			"threshold_seconds", thresholdSeconds,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "stuck-turns list failed: "+err.Error())
		return
	}

	lastEventBefore := now.Add(-time.Duration(streamingThresholdSeconds) * time.Second).UTC()
	streaming, err := lister.ListStreamingStuckTurns(r.Context(), scope, lastEventBefore, limit)
	if err != nil {
		recordDebugStuckTurnsRead("store_error")
		slog.Warn("debug stuck-turns: streaming-class list failed",
			"caller_email", user.Email,
			"session_scope", scope,
			"streaming_threshold_seconds", streamingThresholdSeconds,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "stuck-turns list failed: "+err.Error())
		return
	}

	rows := append(append([]stuckturns.StuckTurn{}, accepted...), streaming...)
	stuckTurns := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		basis := row.ActivityUpdatedAt
		if row.Phase == stuckturns.PhaseStreaming {
			basis = row.LastEventAt
		}
		stuckSeconds := row.StuckSeconds
		if stuckSeconds == 0 && !basis.IsZero() {
			stuckSeconds = int64(now.Sub(basis).Seconds())
		}
		observedAt := ""
		if row.ProviderRateLimitObservedAt != nil {
			observedAt = row.ProviderRateLimitObservedAt.UTC().Format(time.RFC3339)
		}
		lastEventAt := ""
		if !row.LastEventAt.IsZero() {
			lastEventAt = row.LastEventAt.UTC().Format(time.RFC3339)
		}
		stuckTurns = append(stuckTurns, map[string]any{
			"session_id":                      row.SessionID,
			"mode":                            row.Mode,
			"phase":                           row.Phase,
			"activity_status":                 row.ActivityStatus,
			"active_turn_id":                  row.ActiveTurnID,
			"stuck_seconds":                   stuckSeconds,
			"last_event_at":                   lastEventAt,
			"provider_rate_limit_status":      row.ProviderRateLimitStatus,
			"provider_rate_limit_observed_at": observedAt,
		})
	}

	result := "ok"
	if len(stuckTurns) == 0 {
		result = "empty"
	}
	recordDebugStuckTurnsRead(result)
	slog.Info("debug stuck-turns: "+result,
		"caller_email", user.Email,
		"session_scope", scope,
		"threshold_seconds", thresholdSeconds,
		"streaming_threshold_seconds", streamingThresholdSeconds,
		"count", len(stuckTurns),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"description":                 debugStuckTurnsDescription,
		"scope":                       scope,
		"threshold_seconds":           thresholdSeconds,
		"streaming_threshold_seconds": streamingThresholdSeconds,
		"count":                       len(stuckTurns),
		"stuck_turns":                 stuckTurns,
	})
}

// clampedQueryInt parses a query-string integer, falling back to def on
// empty/garbage input, then clamps the result into [min, max].
func clampedQueryInt(raw string, def, min, max int) int {
	v := def
	if parsed, err := strconv.Atoi(raw); err == nil {
		v = parsed
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v
}

// debugStuckTurnsDescription rides in the JSON payload so an operator
// running `curl | jq` understands the surface without leaving the
// terminal.
const debugStuckTurnsDescription = `Orchestrator-detected stuck turns, two stall classes by phase:

phase=accepted — sessions durably accepted (activity_summary.status
submitted/claimed) with no provider progress past threshold_seconds
(default 600s, above the runner's 240s PROVIDER_RETRY_STALL_MS
terminal so a turn the runner would have resolved never appears here).

phase=streaming — sessions whose provider progressed (streaming) but
whose ledger went silent: the last session_events row is older than
streaming_threshold_seconds (default 1200s). This is the
wedged-boundary class (turn open, no terminal — sessions 828/829,
tank-operator#1085). A streaming row is suspicion, not a verdict: a
single long quiet tool call can legitimately exceed the threshold.

This pairs with the TankSessionStuckInProgress alert. A row here means
the runner did NOT fail the turn itself — a fully-wedged or crashed
runner that can emit nothing, a stall class the runner cannot see, or
a wedged turn boundary. To localize the cause for a listed session_id,
read that session's runner logs, its session_events tail
(last_event_at is the staleness anchor for streaming rows), and for
antigravity the runner's turn-settle metrics. Each row carries
stuck_seconds and the last provider_rate_limit_status the runner
reported, if any.

The endpoint never mutates state.`
