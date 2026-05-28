// Package conversationreadstate implements the server-side
// cross-check for the transcript navigation latch failure mode. It
// samples the live per-replica SSE stream registry, joins each open
// stream against the durable sessions row + conversation_read_state
// cursor, and emits a counter when a session is durably idle but the
// user's read cursor lags behind the live tail.
//
// The counter is the cross-check for the client-side
// `navigation-mode-entered-historical-anchor` event. The client
// signal answers "the SPA reports entering historical-anchor mode";
// this server signal answers "the durable consequence (read cursor
// behind the live tail) is visible from the database." A spike in
// both at once is the load-bearing alert evidence; either alone is
// ambiguous (the client could be reporting a real user gesture; the
// server could be counting users who legitimately scrolled to
// history).
//
// The sampler is intentionally cheap: it reads the registry under a
// short read lock and runs two indexed Postgres lookups per open
// stream per pass. At our scale (≤100 concurrent streams,
// 60-second interval) that is well under the orchestrator's
// existing per-route load. See docs/observability.md → Cost / scaling
// for the cardinality bound on the new counter.
package conversationreadstate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionstream"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// SessionInfo is the projection of a sessions row the sampler needs
// to evaluate the stagnation predicate. It excludes the columns the
// admin debug endpoint returns; only the fields that participate in
// the predicate or the metric labels are populated.
type SessionInfo struct {
	Mode                string
	Status              string
	ActiveTurnID        string
	LastDurableOrderKey string
}

// SessionLookup resolves a (email, scope, session_id) tuple to a
// SessionInfo. Returns (nil, nil) when no row exists — the caller
// treats missing rows as "skip this stream" rather than as an error.
//
// In production this is backed by Postgres via SessionLookupFromPool;
// tests inject a fake to drive the predicate matrix.
type SessionLookup interface {
	LookupSession(ctx context.Context, email, scope, sessionID string) (*SessionInfo, error)
}

// ReadStateLookup resolves a (email, scope, session_id) tuple to a
// conversation_read_state row. Mirrors store.ConversationReadStateStore.Get
// without binding the sampler to the wider store interface.
type ReadStateLookup interface {
	Get(ctx context.Context, email, sessionID string) (*store.ConversationReadStateRecord, error)
}

// StagnationCounter is the metric sink. Production wires this to
// promauto-registered counters in the orchestrator's observability.go;
// tests pass a counting stub.
type StagnationCounter interface {
	RecordStagnant(sessionMode, scope string)
	RecordSkippedActiveTurn(sessionMode, scope string)
	RecordSkippedIdleCaughtUp(sessionMode, scope string)
	RecordSkippedMissingSession(scope string)
	RecordSampleError(reason string)
}

// Sampler holds the dependencies and configuration for a single
// orchestrator replica's stagnation sampler. Construct one via
// NewSampler, then call Run to start the goroutine. RunOnce is
// exposed for tests.
type Sampler struct {
	registry     *sessionstream.Registry
	lookup       SessionLookup
	readStates   func(scope string) ReadStateLookup
	counter      StagnationCounter
	localScope   string
	interval     time.Duration
	queryTimeout time.Duration
	logger       *slog.Logger
}

// SamplerConfig wires the sampler. localScope is the orchestrator
// replica's session scope — the value used for streams that lack a
// scope-prefixed storage key. interval defaults to 60s when zero.
// queryTimeout defaults to 5s.
type SamplerConfig struct {
	Registry     *sessionstream.Registry
	Lookup       SessionLookup
	ReadStates   func(scope string) ReadStateLookup
	Counter      StagnationCounter
	LocalScope   string
	Interval     time.Duration
	QueryTimeout time.Duration
	Logger       *slog.Logger
}

// NewSampler validates the config and returns a Sampler ready to Run.
// Returns nil when any required dependency is missing — the caller
// (main.go) treats nil as "sampler disabled in this build / stub
// mode" so a Postgres-less boot path doesn't panic.
func NewSampler(cfg SamplerConfig) *Sampler {
	if cfg.Registry == nil || cfg.Lookup == nil || cfg.ReadStates == nil || cfg.Counter == nil {
		return nil
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	queryTimeout := cfg.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 5 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	localScope := strings.TrimSpace(cfg.LocalScope)
	if localScope == "" {
		localScope = "default"
	}
	return &Sampler{
		registry:     cfg.Registry,
		lookup:       cfg.Lookup,
		readStates:   cfg.ReadStates,
		counter:      cfg.Counter,
		localScope:   localScope,
		interval:     interval,
		queryTimeout: queryTimeout,
		logger:       logger,
	}
}

// Run blocks until ctx is cancelled, running one sample per tick.
// Errors from individual passes are logged and counted via the
// counter's RecordSampleError; they do not stop the loop.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil {
		return
	}
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

// SampleSummary is the per-pass tally. Tests assert against the
// counts; the production loop logs a single slog line per pass with
// these fields so an operator can read "did the sampler run, how
// many streams, how many stagnant" from kubectl logs.
type SampleSummary struct {
	Streams            int
	Stagnant           int
	SkippedActiveTurn  int
	SkippedCaughtUp    int
	SkippedMissingRow  int
	SkippedReadStateErr int
	SkippedSessionErr  int
}

// RunOnce executes a single sample pass. Returns the per-pass tally.
// Safe to call from tests with a known clock; the production
// goroutine calls this on every ticker tick.
func (s *Sampler) RunOnce(ctx context.Context, now time.Time) SampleSummary {
	if s == nil {
		return SampleSummary{}
	}
	snapshots := s.registry.Snapshot(now)
	summary := SampleSummary{Streams: len(snapshots)}
	for _, snap := range snapshots {
		scope, sessionID := decodeStorageKey(snap.StorageKey, s.localScope, snap.SessionID)
		if sessionID == "" || strings.TrimSpace(snap.Email) == "" {
			s.counter.RecordSkippedMissingSession(scope)
			summary.SkippedMissingRow++
			continue
		}
		passCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
		info, err := s.lookup.LookupSession(passCtx, snap.Email, scope, sessionID)
		cancel()
		if err != nil {
			s.counter.RecordSampleError("session_lookup")
			summary.SkippedSessionErr++
			s.logger.Warn("conversation read state sampler: session lookup failed",
				"scope", scope,
				"session_id", sessionID,
				"error", err,
			)
			continue
		}
		if info == nil {
			s.counter.RecordSkippedMissingSession(scope)
			summary.SkippedMissingRow++
			continue
		}
		mode := info.Mode
		if mode == "" {
			mode = "unknown"
		}
		if !isDurablyIdle(info) {
			s.counter.RecordSkippedActiveTurn(mode, scope)
			summary.SkippedActiveTurn++
			continue
		}
		readCtx, cancelRead := context.WithTimeout(ctx, s.queryTimeout)
		readState, err := s.readStates(scope).Get(readCtx, snap.Email, sessionID)
		cancelRead()
		if err != nil {
			s.counter.RecordSampleError("read_state_lookup")
			summary.SkippedReadStateErr++
			s.logger.Warn("conversation read state sampler: read state lookup failed",
				"scope", scope,
				"session_id", sessionID,
				"error", err,
			)
			continue
		}
		if !cursorLags(info.LastDurableOrderKey, readState) {
			s.counter.RecordSkippedIdleCaughtUp(mode, scope)
			summary.SkippedCaughtUp++
			continue
		}
		s.counter.RecordStagnant(mode, scope)
		summary.Stagnant++
	}
	s.logger.Info("conversation read state sampler: pass complete",
		"streams", summary.Streams,
		"stagnant", summary.Stagnant,
		"skipped_active_turn", summary.SkippedActiveTurn,
		"skipped_caught_up", summary.SkippedCaughtUp,
		"skipped_missing_row", summary.SkippedMissingRow,
		"skipped_read_state_err", summary.SkippedReadStateErr,
		"skipped_session_err", summary.SkippedSessionErr,
	)
	return summary
}

// isDurablyIdle is the predicate that distinguishes "session is
// over, user should be at the live tail" from "turn is still in
// flight, lag is expected." It is also the boundary that defines
// what the new alert can actually claim: only the durably-idle
// failure case is countable; in-flight turns are excluded so the
// counter's rate does not pulse with normal traffic.
func isDurablyIdle(info *SessionInfo) bool {
	if info == nil {
		return false
	}
	if strings.TrimSpace(info.ActiveTurnID) != "" {
		return false
	}
	switch strings.TrimSpace(info.Status) {
	case "ready", "Active":
		return true
	default:
		return false
	}
}

// cursorLags is the comparison between the durable tail and the
// per-user read cursor. The cursor is strictly less than the durable
// tail's order key — equal cursors are caught up. order_key strings
// are lexicographically comparable by the session_events schema.
func cursorLags(lastDurable string, readState *store.ConversationReadStateRecord) bool {
	durable := strings.TrimSpace(lastDurable)
	if durable == "" {
		return false
	}
	if readState == nil {
		// No durable read state means the user has never read this
		// session. Whether to flag that depends on whether the
		// session has any durable events — which `durable != ""`
		// already confirms. So yes, a session with events and no
		// read state is stagnant from the cursor's perspective.
		return true
	}
	return durable > strings.TrimSpace(readState.LastReadOrderKey)
}

// decodeStorageKey extracts the scope and session_id from the
// stream's storage_key. The shape is `<scope>:<session_id>` per
// sessionmodel.SessionStorageKey; older streams that pre-date scope
// prefixing fall back to the orchestrator's local scope.
func decodeStorageKey(storageKey, localScope, fallbackSessionID string) (string, string) {
	storageKey = strings.TrimSpace(storageKey)
	fallbackSessionID = strings.TrimSpace(fallbackSessionID)
	if storageKey == "" {
		return localScope, fallbackSessionID
	}
	idx := strings.LastIndex(storageKey, ":")
	if idx <= 0 || idx == len(storageKey)-1 {
		return localScope, fallbackSessionID
	}
	scope := storageKey[:idx]
	sessionID := storageKey[idx+1:]
	if scope == "" {
		scope = localScope
	}
	if sessionID == "" {
		sessionID = fallbackSessionID
	}
	return scope, sessionID
}

// SessionLookupFromQuery binds a pgxQuerier to the SessionLookup
// interface. Production wires this against the pgxpool.Pool; tests
// pass a stub pgxQuerier.
type SessionLookupFromQuery struct {
	Pool PGQuerier
}

// PGQuerier is the minimal pgx interface the sampler needs.
type PGQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LookupSession is the Postgres-backed implementation. It joins the
// activity_summary JSON shape inline so the sampler runs one query
// per stream instead of two (the sampler already runs one more for
// the read-state lookup, so we're at 2-per-stream in the hot loop).
func (l SessionLookupFromQuery) LookupSession(ctx context.Context, email, scope, sessionID string) (*SessionInfo, error) {
	if l.Pool == nil {
		return nil, errors.New("pg pool not configured")
	}
	if email == "" || sessionID == "" {
		return nil, nil
	}
	const q = `
		SELECT mode, status, activity_summary
		FROM sessions
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	var (
		mode, status string
		activityRaw  []byte
	)
	if err := l.Pool.QueryRow(ctx, q, email, scope, sessionID).Scan(&mode, &status, &activityRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	active, last := decodeActivityFields(activityRaw)
	return &SessionInfo{
		Mode:                mode,
		Status:              status,
		ActiveTurnID:        active,
		LastDurableOrderKey: last,
	}, nil
}

// decodeActivityFields tolerates an `active_turn_id` that is either
// a string or JSON null. The wire shape evolves; the sampler treats
// any non-string as empty.
func decodeActivityFields(raw []byte) (activeTurnID, lastDurable string) {
	if len(raw) == 0 {
		return "", ""
	}
	var payload struct {
		ActiveTurnID any    `json:"active_turn_id"`
		LastOrderKey string `json:"last_order_key"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	if s, ok := payload.ActiveTurnID.(string); ok {
		activeTurnID = s
	}
	lastDurable = payload.LastOrderKey
	return activeTurnID, lastDurable
}
