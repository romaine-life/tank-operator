// Admin-only debug surface for orchestrator-detected stuck turns:
// sessions whose durable activity_summary is submitted/claimed
// (accepted, no provider progress) and whose updated_at is older than
// the stall threshold, with no terminal event resolving the turn.
//
// This is the per-entity localizer for the stuck-turn observability
// story. The aggregate signal is the tank_sessions_stuck_in_progress
// gauge plus the TankSessionStuckInProgress alert; this endpoint
// resolves "which session_ids, for how long, with what provider
// rate-limit state" once the alert fires — without kubectl, per the
// observability contract (operators diagnose from /metrics +
// /api/debug + slog + Grafana alone).
//
// A row here means the runner did NOT fail the turn itself: it is the
// orchestrator-side complement to the runner's api_retry rate-limit
// terminal (PROVIDER_RETRY_STALL_MS, 240s). The threshold default
// (10m) sits deliberately above 240s so a turn the runner-side
// terminal would have resolved never appears here — only the genuine
// wedge (fully-wedged/crashed runner, or a stall class the runner
// cannot see) does. Inspect the listed session_id's claude-runner logs
// and session_events to localize the cause.
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
	debugStuckTurnsDefaultThresholdSeconds = 600
	debugStuckTurnsMinThresholdSeconds     = 60
	debugStuckTurnsMaxThresholdSeconds     = 86400
	debugStuckTurnsDefaultLimit            = 100
	debugStuckTurnsMaxLimit                = 500
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
	limit := clampedQueryInt(
		r.URL.Query().Get("limit"),
		debugStuckTurnsDefaultLimit,
		1,
		debugStuckTurnsMaxLimit,
	)

	threshold := time.Duration(thresholdSeconds) * time.Second
	now := time.Now()
	olderThan := now.Add(-threshold).UTC().Format(time.RFC3339)

	rows, err := stuckturns.ListerFromQuery{Pool: s.pgPool}.ListStuckTurns(r.Context(), scope, olderThan, limit)
	if err != nil {
		recordDebugStuckTurnsRead("store_error")
		slog.Warn("debug stuck-turns: list failed",
			"caller_email", user.Email,
			"session_scope", scope,
			"threshold_seconds", thresholdSeconds,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "stuck-turns list failed: "+err.Error())
		return
	}

	stuckTurns := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		stuckSeconds := row.StuckSeconds
		if stuckSeconds == 0 && !row.ActivityUpdatedAt.IsZero() {
			stuckSeconds = int64(now.Sub(row.ActivityUpdatedAt).Seconds())
		}
		observedAt := ""
		if row.ProviderRateLimitObservedAt != nil {
			observedAt = row.ProviderRateLimitObservedAt.UTC().Format(time.RFC3339)
		}
		stuckTurns = append(stuckTurns, map[string]any{
			"session_id":                      row.SessionID,
			"mode":                            row.Mode,
			"activity_status":                 row.ActivityStatus,
			"active_turn_id":                  row.ActiveTurnID,
			"stuck_seconds":                   stuckSeconds,
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
		"count", len(stuckTurns),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"description":       debugStuckTurnsDescription,
		"scope":             scope,
		"threshold_seconds": thresholdSeconds,
		"count":             len(stuckTurns),
		"stuck_turns":       stuckTurns,
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
const debugStuckTurnsDescription = `Orchestrator-detected stuck turns: sessions durably accepted (activity_summary.status submitted/claimed) with no provider progress past the threshold.

This pairs with the TankSessionStuckInProgress alert and is the
orchestrator-side complement to the runner's api_retry rate-limit
terminal (PROVIDER_RETRY_STALL_MS, 240s). A row here means the runner
did NOT fail the turn itself — either a fully-wedged or crashed runner
that can emit nothing, or a stall class the runner cannot see. The
default threshold (600s) sits above the runner's 240s terminal so a
turn the runner-side terminal would have resolved never appears here.

To localize the cause for a listed session_id, read that session's
claude-runner logs and its session_events ledger. Each row carries
stuck_seconds (how long it has been accepted-but-unprogressed) and the
last provider_rate_limit_status the runner reported, if any.

The endpoint never mutates state.`
