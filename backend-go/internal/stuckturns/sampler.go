// Package stuckturns implements the orchestrator-side detector for
// turns that were durably accepted but never made provider progress
// and never reached a terminal — the "wedged/crashed runner" failure
// mode the runner itself cannot report.
//
// Stage 1 of the rate-limit-stall hardening effort taught the runner
// to force-fail a turn after a bounded Claude SDK api_retry{rate_limit}
// storm (SESSION_PROVIDER_RETRY_STALL_MS, 240s) by publishing a
// durable turn terminal. But a fully-wedged or crashed runner emits
// nothing: it cannot fail its own turn, so the turn sits in
// sessions.activity_summary.status="claimed" (or "submitted") forever
// with no terminal. The aggregate TankTurnSilentStranding alert
// (submitted-vs-terminal rate over a window) is too coarse to localize
// such a turn, and an operator must be able to diagnose from /metrics +
// /api/debug + slog + Grafana alone — no kubectl. See
// docs/observability.md → "Stuck turn debug surface".
//
// This sampler queries the durable sessions table for the
// orchestrator's local scope every 60s and flags rows whose
// activity_summary is submitted/claimed and whose activity_summary
// updated_at is older than a threshold (default 10m, deliberately ABOVE
// the runner's 240s terminal so it only fires when the runner-side
// terminal did NOT resolve it — the genuine wedge). It sets a single
// unlabeled gauge to the count (no per-session/turn/email labels, per
// the cardinality rules), emits one slog.Warn per stuck session
// (session_id is allowed in slog, never in metrics), and a per-pass
// slog.Info summary. The admin debug endpoint
// GET /api/debug/stuck-turns lists the detail.
//
// IMPORTANT on the timestamp filter: the predicate compares
// activity_summary->>'updated_at' as TEXT against an RFC3339-Z
// formatted threshold string. The fold (internal/sessionactivity)
// writes ISO-8601 UTC `Z` strings (e.g. 2026-06-06T19:01:41.673Z), and
// a lexicographic compare is correct for same-format UTC-Z timestamps.
// We deliberately do NOT cast the JSON text to timestamptz in SQL: a
// single malformed row would error the whole query, which would be
// ironic for a failure-detector. The string is parsed best-effort in
// Go for display/stuck-seconds only.
package stuckturns

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StuckTurn is the projection of one durably accepted-but-unprogressed
// session the sampler flags. session_id rides here (and in the slog
// line and the admin endpoint) but never in a metric label.
type StuckTurn struct {
	SessionID                   string
	Scope                       string
	Mode                        string
	ActivityStatus              string
	ActiveTurnID                string
	ActivityUpdatedAt           time.Time
	StuckSeconds                int64
	ProviderRateLimitStatus     string
	ProviderRateLimitObservedAt *time.Time
}

// Lister resolves the set of stuck turns for a scope older than the
// supplied RFC3339-Z threshold string. Production wires
// ListerFromQuery against the pgxpool.Pool; tests inject a fake.
//
// olderThanRFC3339 is compared as TEXT against
// activity_summary->>'updated_at' (see the package doc on why the
// compare is lexicographic, not a timestamptz cast).
type Lister interface {
	ListStuckTurns(ctx context.Context, scope string, olderThanRFC3339 string, limit int) ([]StuckTurn, error)
}

// Metrics is the metric sink. Production wires this to the
// promauto-registered gauge + counter in the orchestrator's
// observability.go; tests pass a counting stub.
type Metrics interface {
	SetStuckCount(n int)
	RecordSampleError(reason string)
}

// PGQuerier is the minimal pgx interface the lister needs. Lets tests
// inject a stub without standing up a real pgxpool.
type PGQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Sampler holds the dependencies and configuration for a single
// orchestrator replica's stuck-turn detector. Construct one via
// NewSampler, then call Run to start the goroutine. RunOnce is exposed
// for tests.
type Sampler struct {
	lister       Lister
	metrics      Metrics
	threshold    time.Duration
	interval     time.Duration
	queryTimeout time.Duration
	limit        int
	scope        string
	logger       *slog.Logger
}

// SamplerConfig wires the sampler. Threshold defaults to 10m (above the
// runner's 240s terminal), Interval to 60s, QueryTimeout to 5s, Limit
// to 200, Scope to "default", Logger to slog.Default().
type SamplerConfig struct {
	Lister       Lister
	Metrics      Metrics
	Threshold    time.Duration
	Interval     time.Duration
	QueryTimeout time.Duration
	Limit        int
	Scope        string
	Logger       *slog.Logger
}

// NewSampler validates the config and returns a Sampler ready to Run.
// Returns nil when Lister or Metrics is missing — the caller (main.go)
// treats nil as "detector disabled in this build / stub mode" so a
// Postgres-less boot path doesn't panic.
func NewSampler(cfg SamplerConfig) *Sampler {
	if cfg.Lister == nil || cfg.Metrics == nil {
		return nil
	}
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = 10 * time.Minute
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	queryTimeout := cfg.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 5 * time.Second
	}
	limit := cfg.Limit
	if limit <= 0 {
		limit = 200
	}
	scope := strings.TrimSpace(cfg.Scope)
	if scope == "" {
		scope = "default"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Sampler{
		lister:       cfg.Lister,
		metrics:      cfg.Metrics,
		threshold:    threshold,
		interval:     interval,
		queryTimeout: queryTimeout,
		limit:        limit,
		scope:        scope,
		logger:       logger,
	}
}

// Run executes one pass immediately, then one per ticker tick, until
// ctx is cancelled. Errors from individual passes are logged and
// counted via Metrics.RecordSampleError; they do not stop the loop.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.RunOnce(ctx, time.Now())
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RunOnce(ctx, time.Now())
		}
	}
}

// RunOnce executes a single detector pass and returns the number of
// stuck turns found. Safe to call from tests with a known clock; the
// production goroutine calls this on every ticker tick.
//
// The threshold is materialized as now-threshold formatted
// time.RFC3339 in UTC; that string is the TEXT comparand the lister's
// SQL uses against activity_summary->>'updated_at'.
func (s *Sampler) RunOnce(ctx context.Context, now time.Time) int {
	if s == nil {
		return 0
	}
	olderThan := now.Add(-s.threshold).UTC().Format(time.RFC3339)
	passCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	stuck, err := s.lister.ListStuckTurns(passCtx, s.scope, olderThan, s.limit)
	cancel()
	if err != nil {
		s.metrics.RecordSampleError("list")
		s.logger.Warn("stuck turn sampler: list failed",
			"scope", s.scope,
			"older_than", olderThan,
			"error", err,
		)
		return 0
	}
	s.metrics.SetStuckCount(len(stuck))
	for _, t := range stuck {
		stuckSeconds := t.StuckSeconds
		if stuckSeconds == 0 && !t.ActivityUpdatedAt.IsZero() {
			stuckSeconds = int64(now.Sub(t.ActivityUpdatedAt).Seconds())
		}
		var observedAt string
		if t.ProviderRateLimitObservedAt != nil {
			observedAt = t.ProviderRateLimitObservedAt.UTC().Format(time.RFC3339)
		}
		s.logger.Warn("stuck turn sampler: session accepted but unprogressed past threshold",
			"scope", s.scope,
			"session_id", t.SessionID,
			"mode", t.Mode,
			"activity_status", t.ActivityStatus,
			"active_turn_id", t.ActiveTurnID,
			"stuck_seconds", stuckSeconds,
			"provider_rate_limit_status", t.ProviderRateLimitStatus,
			"provider_rate_limit_observed_at", observedAt,
		)
	}
	s.logger.Info("stuck turn sampler: pass complete",
		"scope", s.scope,
		"stuck", len(stuck),
	)
	return len(stuck)
}

// ListerFromQuery binds a PGQuerier to the Lister interface. Production
// wires this against the pgxpool.Pool; tests pass a stub PGQuerier.
type ListerFromQuery struct {
	Pool PGQuerier
}

// listStuckTurnsSQL selects durably accepted-but-unprogressed rows for
// one scope. The activity_summary->>'updated_at' comparison is a TEXT
// lexicographic compare against an RFC3339-Z threshold (NOT a
// timestamptz cast — a single malformed row must not error the whole
// detector). status='Active' + terminating_at IS NULL excludes
// shutting-down/dead pods, whose missing terminal is expected.
const listStuckTurnsSQL = `
SELECT session_id, session_scope, mode,
       activity_summary->>'status' AS activity_status,
       COALESCE(activity_summary->>'active_turn_id','') AS active_turn_id,
       COALESCE(activity_summary->>'updated_at','') AS activity_updated_at,
       COALESCE(provider_rate_limit_info->>'status','') AS rl_status,
       provider_rate_limit_observed_at
FROM sessions
WHERE session_scope = $1
  AND status = 'Active'
  AND terminating_at IS NULL
  AND activity_summary->>'status' IN ('submitted','claimed')
  AND activity_summary->>'updated_at' IS NOT NULL
  AND activity_summary->>'updated_at' < $2
ORDER BY activity_summary->>'updated_at' ASC
LIMIT $3`

// ListStuckTurns is the Postgres-backed implementation. ActivityUpdatedAt
// is parsed best-effort from the TEXT updated_at (a malformed value
// yields a zero time, which the caller surfaces as stuck_seconds=0
// rather than erroring the pass).
func (l ListerFromQuery) ListStuckTurns(ctx context.Context, scope string, olderThanRFC3339 string, limit int) ([]StuckTurn, error) {
	if l.Pool == nil {
		return nil, nil
	}
	rows, err := l.Pool.Query(ctx, listStuckTurnsSQL, scope, olderThanRFC3339, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StuckTurn
	for rows.Next() {
		var (
			sessionID, rowScope, mode    string
			activityStatus, activeTurnID string
			activityUpdatedAt, rlStatus  string
			observedAt                   *time.Time
		)
		if err := rows.Scan(
			&sessionID, &rowScope, &mode,
			&activityStatus, &activeTurnID,
			&activityUpdatedAt, &rlStatus,
			&observedAt,
		); err != nil {
			return nil, err
		}
		st := StuckTurn{
			SessionID:                   sessionID,
			Scope:                       rowScope,
			Mode:                        mode,
			ActivityStatus:              activityStatus,
			ActiveTurnID:                activeTurnID,
			ProviderRateLimitStatus:     rlStatus,
			ProviderRateLimitObservedAt: observedAt,
		}
		if parsed, perr := time.Parse(time.RFC3339, activityUpdatedAt); perr == nil {
			st.ActivityUpdatedAt = parsed
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
