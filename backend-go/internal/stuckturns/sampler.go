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
// orchestrator's local scope every 60s and flags two distinct stall
// classes, reported as two series of one gauge
// (tank_sessions_stuck_in_progress{phase}):
//
//   - phase="accepted": activity_summary.status submitted/claimed with
//     activity_summary.updated_at older than the accepted threshold
//     (default 10m, deliberately ABOVE the runner's 240s terminal so it
//     only fires when the runner-side terminal did NOT resolve it — the
//     genuine pre-progress wedge).
//   - phase="streaming": activity_summary.status streaming whose LAST
//     LEDGER EVENT (session_events, latest order_key row's created_at)
//     is older than the streaming threshold (default 20m). The staleness
//     basis differs on purpose: activity_summary.updated_at only moves
//     when the fold OUTPUT changes, so for a healthy long streaming turn
//     it is pinned at the turn.started moment — comparing it would flag
//     every long turn. Only ledger silence distinguishes a wedged
//     boundary from a live turn. This class exists because of sessions
//     828/829 (2026-06-12): the antigravity runner's turn-settle window
//     was cancelled by a transcript-rewrite replay and never re-armed,
//     leaving turns open and ledger-silent for 30+ minutes while this
//     gauge read 0 (the accepted-only blind spot, tank-operator#1085).
//     A streaming row is suspicion, not a verdict — a single quiet tool
//     call can legitimately exceed the threshold; the row localizes,
//     the stranded-turn sweep (2h floor) remains the terminal-writing
//     backstop.
//
// The gauge carries only the bounded phase label (no per-session/turn/
// email labels, per the cardinality rules); per-session detail rides
// one slog.Warn per stuck session (session_id is allowed in slog, never
// in metrics) and the admin debug endpoint GET /api/debug/stuck-turns.
//
// IMPORTANT on the accepted-class timestamp filter: the predicate
// compares activity_summary->>'updated_at' as TEXT against an RFC3339-Z
// formatted threshold string. The fold (internal/sessionactivity)
// writes ISO-8601 UTC `Z` strings (e.g. 2026-06-06T19:01:41.673Z), and
// a lexicographic compare is correct for same-format UTC-Z timestamps.
// We deliberately do NOT cast the JSON text to timestamptz in SQL: a
// single malformed row would error the whole query, which would be
// ironic for a failure-detector. The string is parsed best-effort in
// Go for display/stuck-seconds only. The streaming class has no such
// hazard — session_events.created_at is a real timestamptz column — so
// it binds a time.Time directly.
package stuckturns

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Phase labels for the two stall classes. Bounded by construction —
// these are the only values that ever reach the gauge's phase label.
const (
	// PhaseAccepted: durably accepted (submitted/claimed), no provider
	// progress. Staleness basis: activity_summary.updated_at.
	PhaseAccepted = "accepted"
	// PhaseStreaming: provider progressed (streaming) but the ledger
	// went silent with no terminal — the wedged-boundary class.
	// Staleness basis: the session's last session_events row.
	PhaseStreaming = "streaming"
)

// StuckTurn is the projection of one stalled session the sampler flags.
// session_id rides here (and in the slog line and the admin endpoint)
// but never in a metric label. Phase says which stall class matched;
// the staleness basis is ActivityUpdatedAt for PhaseAccepted and
// LastEventAt for PhaseStreaming.
type StuckTurn struct {
	SessionID                   string
	Scope                       string
	Mode                        string
	Phase                       string
	ActivityStatus              string
	ActiveTurnID                string
	ActivityUpdatedAt           time.Time
	LastEventAt                 time.Time
	StuckSeconds                int64
	ProviderRateLimitStatus     string
	ProviderRateLimitObservedAt *time.Time
}

// stuckBasis returns the staleness anchor for the row's phase (zero
// time when the row carried no parseable anchor).
func (t StuckTurn) stuckBasis() time.Time {
	if t.Phase == PhaseStreaming {
		return t.LastEventAt
	}
	return t.ActivityUpdatedAt
}

// Lister resolves the stalled sessions for a scope. Production wires
// ListerFromQuery against the pgxpool.Pool; tests inject a fake.
//
// ListStuckTurns (accepted class): olderThanRFC3339 is compared as TEXT
// against activity_summary->>'updated_at' (see the package doc on why
// the compare is lexicographic, not a timestamptz cast).
//
// ListStreamingStuckTurns (streaming class): lastEventBefore is a real
// time.Time bound against session_events.created_at (timestamptz — no
// malformed-JSON hazard, so no TEXT dance).
type Lister interface {
	ListStuckTurns(ctx context.Context, scope string, olderThanRFC3339 string, limit int) ([]StuckTurn, error)
	ListStreamingStuckTurns(ctx context.Context, scope string, lastEventBefore time.Time, limit int) ([]StuckTurn, error)
}

// Metrics is the metric sink. Production wires this to the
// promauto-registered gauge + counter in the orchestrator's
// observability.go; tests pass a counting stub. SetStuckCount writes
// one phase's last-pass snapshot (phase is one of the Phase* constants).
type Metrics interface {
	SetStuckCount(phase string, n int)
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
	lister             Lister
	metrics            Metrics
	threshold          time.Duration
	streamingThreshold time.Duration
	interval           time.Duration
	queryTimeout       time.Duration
	limit              int
	scope              string
	logger             *slog.Logger
}

// SamplerConfig wires the sampler. Threshold (accepted class) defaults
// to 10m (above the runner's 240s terminal); StreamingThreshold
// defaults to 20m (above any legitimate single quiet tool call's usual
// span, below the stranded-turn sweep's 2h terminal floor — detection
// beats the user noticing, the sweep still owns the terminal). Interval
// defaults to 60s, QueryTimeout to 5s, Limit to 200 (per class), Scope
// to "default", Logger to slog.Default().
type SamplerConfig struct {
	Lister             Lister
	Metrics            Metrics
	Threshold          time.Duration
	StreamingThreshold time.Duration
	Interval           time.Duration
	QueryTimeout       time.Duration
	Limit              int
	Scope              string
	Logger             *slog.Logger
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
	streamingThreshold := cfg.StreamingThreshold
	if streamingThreshold <= 0 {
		streamingThreshold = 20 * time.Minute
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
		lister:             cfg.Lister,
		metrics:            cfg.Metrics,
		threshold:          threshold,
		streamingThreshold: streamingThreshold,
		interval:           interval,
		queryTimeout:       queryTimeout,
		limit:              limit,
		scope:              scope,
		logger:             logger,
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

// RunOnce executes a single detector pass — both stall classes — and
// returns the total number of stuck turns found. Safe to call from
// tests with a known clock; the production goroutine calls this on
// every ticker tick.
//
// The two classes list and gauge independently: an error in one class
// counts a bounded sample-error reason and leaves THAT phase's gauge
// series untouched (a stale value plus a nonzero error rate is the
// "detector is blind" signal documented in observability.md), while the
// other class still refreshes.
//
// The accepted threshold is materialized as now-threshold formatted
// time.RFC3339 in UTC; that string is the TEXT comparand the lister's
// SQL uses against activity_summary->>'updated_at'. The streaming
// threshold stays a time.Time bound against session_events.created_at.
func (s *Sampler) RunOnce(ctx context.Context, now time.Time) int {
	if s == nil {
		return 0
	}
	total := 0

	olderThan := now.Add(-s.threshold).UTC().Format(time.RFC3339)
	passCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	accepted, err := s.lister.ListStuckTurns(passCtx, s.scope, olderThan, s.limit)
	cancel()
	if err != nil {
		s.metrics.RecordSampleError("list")
		s.logger.Warn("stuck turn sampler: accepted-class list failed",
			"scope", s.scope,
			"older_than", olderThan,
			"error", err,
		)
	} else {
		s.metrics.SetStuckCount(PhaseAccepted, len(accepted))
		s.logStuck(accepted, now)
		total += len(accepted)
	}

	lastEventBefore := now.Add(-s.streamingThreshold).UTC()
	passCtx, cancel = context.WithTimeout(ctx, s.queryTimeout)
	streaming, err := s.lister.ListStreamingStuckTurns(passCtx, s.scope, lastEventBefore, s.limit)
	cancel()
	if err != nil {
		s.metrics.RecordSampleError("list_streaming")
		s.logger.Warn("stuck turn sampler: streaming-class list failed",
			"scope", s.scope,
			"last_event_before", lastEventBefore.Format(time.RFC3339),
			"error", err,
		)
	} else {
		s.metrics.SetStuckCount(PhaseStreaming, len(streaming))
		s.logStuck(streaming, now)
		total += len(streaming)
	}

	s.logger.Info("stuck turn sampler: pass complete",
		"scope", s.scope,
		"stuck", total,
	)
	return total
}

// logStuck emits the per-session localizer line for one class's rows.
// session_id is allowed in slog, never in metrics.
func (s *Sampler) logStuck(stuck []StuckTurn, now time.Time) {
	for _, t := range stuck {
		stuckSeconds := t.StuckSeconds
		if basis := t.stuckBasis(); stuckSeconds == 0 && !basis.IsZero() {
			stuckSeconds = int64(now.Sub(basis).Seconds())
		}
		var observedAt string
		if t.ProviderRateLimitObservedAt != nil {
			observedAt = t.ProviderRateLimitObservedAt.UTC().Format(time.RFC3339)
		}
		var lastEventAt string
		if !t.LastEventAt.IsZero() {
			lastEventAt = t.LastEventAt.UTC().Format(time.RFC3339)
		}
		s.logger.Warn("stuck turn sampler: session stalled past threshold",
			"scope", s.scope,
			"session_id", t.SessionID,
			"mode", t.Mode,
			"phase", t.Phase,
			"activity_status", t.ActivityStatus,
			"active_turn_id", t.ActiveTurnID,
			"stuck_seconds", stuckSeconds,
			"last_event_at", lastEventAt,
			"provider_rate_limit_status", t.ProviderRateLimitStatus,
			"provider_rate_limit_observed_at", observedAt,
		)
	}
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
			Phase:                       PhaseAccepted,
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

// listStreamingStuckTurnsSQL selects streaming sessions whose ledger
// went silent: the latest session_events row (PK (tank_session_id,
// order_key) — the LATERAL is an O(1) backward index scan per
// candidate) is older than the threshold. The sessions filter
// (scope + Active + streaming) bounds the LATERAL to a handful of rows
// per pass, so this stays cheap at the 60s cadence even on the B1ms
// instance (the stranded-turn sweep's cost lesson was its 30-day
// turn.submitted scan, not this shape).
//
// activity_summary.updated_at is deliberately NOT the comparand here:
// the fold only rewrites it when the fold OUTPUT changes, so a healthy
// long streaming turn keeps the turn.started timestamp for its whole
// life and would always read stale. Ledger silence — no
// session_events row of ANY type — is what distinguishes a wedged
// boundary (sessions 828/829) from a live turn.
const listStreamingStuckTurnsSQL = `
SELECT s.session_id, s.session_scope, s.mode,
       s.activity_summary->>'status' AS activity_status,
       COALESCE(s.activity_summary->>'active_turn_id','') AS active_turn_id,
       ev.last_event_at,
       COALESCE(s.provider_rate_limit_info->>'status','') AS rl_status,
       s.provider_rate_limit_observed_at
FROM sessions s
CROSS JOIN LATERAL (
  SELECT e.created_at AS last_event_at
  FROM session_events e
  WHERE e.tank_session_id = s.session_id
  ORDER BY e.order_key DESC
  LIMIT 1
) ev
WHERE s.session_scope = $1
  AND s.status = 'Active'
  AND s.terminating_at IS NULL
  AND s.activity_summary->>'status' = 'streaming'
  AND ev.last_event_at < $2
ORDER BY ev.last_event_at ASC
LIMIT $3`

// ListStreamingStuckTurns is the Postgres-backed streaming-class
// implementation. lastEventBefore binds directly as timestamptz —
// created_at is a real column, so none of the accepted class's
// TEXT-compare defensiveness applies.
func (l ListerFromQuery) ListStreamingStuckTurns(ctx context.Context, scope string, lastEventBefore time.Time, limit int) ([]StuckTurn, error) {
	if l.Pool == nil {
		return nil, nil
	}
	rows, err := l.Pool.Query(ctx, listStreamingStuckTurnsSQL, scope, lastEventBefore.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StuckTurn
	for rows.Next() {
		var (
			sessionID, rowScope, mode    string
			activityStatus, activeTurnID string
			lastEventAt                  time.Time
			rlStatus                     string
			observedAt                   *time.Time
		)
		if err := rows.Scan(
			&sessionID, &rowScope, &mode,
			&activityStatus, &activeTurnID,
			&lastEventAt, &rlStatus,
			&observedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, StuckTurn{
			SessionID:                   sessionID,
			Scope:                       rowScope,
			Mode:                        mode,
			Phase:                       PhaseStreaming,
			ActivityStatus:              activityStatus,
			ActiveTurnID:                activeTurnID,
			LastEventAt:                 lastEventAt,
			ProviderRateLimitStatus:     rlStatus,
			ProviderRateLimitObservedAt: observedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
