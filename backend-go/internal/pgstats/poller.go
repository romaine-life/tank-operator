// Package pgstats polls the Postgres server for connection-saturation
// metrics that the orchestrator's existing query tracer cannot
// observe — the tracer sees its own pool's traffic, not the server's
// total backend count or the max_connections ceiling.
//
// The 2026-05-25 incident shape this package exists to surface in
// advance: the B1ms Flex Server has max_connections=50; 12
// orchestrator pods × pool.MaxConns each were exceeding the cap,
// producing SQLSTATE 53300 "remaining connection slots are reserved
// for roles with the SUPERUSER attribute" crash-loops on cold boot.
// The only signal beforehand was the crash itself. After this
// package lands, the TankPgConnectionSaturation alert fires at 70%
// utilization so the SKU bump can be planned, not reactive.
//
// Auth: this poller reuses the orchestrator's AAD-aware pgxpool
// instead of running a separate pg_exporter sidecar. The upstream
// pg_exporter image expects username/password and has no native
// Entra ID workload-identity path; building one would require a
// wrapper container that fetches an AAD token and rewrites a DSN at
// connection time. The orchestrator's pool already does that
// correctly via pgstore.BeforeConnect, so the in-process poller is
// the lower-complexity surface.
package pgstats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Metrics receives the per-poll connection counts. Wired to
// prometheus in cmd/tank-operator/observability.go; tests can pass a
// noop or recording implementation.
//
// Outcome labels are bounded at code: "ok" on a successful poll,
// "query_failed" when a SQL call errors. The prometheus side
// validates against the same closed set so a future poller change
// can't quietly inflate the label space.
type Metrics interface {
	RecordBackendConnections(count float64)
	RecordMaxConnections(count float64)
	RecordPollOutcome(outcome string)
}

// noopMetrics is the default when Config.Metrics is nil. The poller
// can still run usefully without a metrics implementation — the
// gauges just don't update.
type noopMetrics struct{}

func (noopMetrics) RecordBackendConnections(float64) {}
func (noopMetrics) RecordMaxConnections(float64)     {}
func (noopMetrics) RecordPollOutcome(string)         {}

// Config wires the poller's dependencies. Pool is required; the
// other fields default sensibly.
type Config struct {
	Pool     *pgxpool.Pool
	Metrics  Metrics
	Interval time.Duration
}

// Poller queries pg_stat_database for the live backend count and
// current_setting('max_connections') for the server cap on a fixed
// interval. Run blocks until the context is canceled; transient query
// failures are logged + counted but do not stop the loop.
type Poller struct {
	pool     *pgxpool.Pool
	metrics  Metrics
	interval time.Duration
}

// New builds a Poller with sensible defaults. Returns an error only
// if Pool is nil — every other Config field is optional.
func New(cfg Config) (*Poller, error) {
	if cfg.Pool == nil {
		return nil, fmt.Errorf("pgstats: pool is required")
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	interval := cfg.Interval
	if interval <= 0 {
		// 30s gives operators a useful sample rate without making
		// every orchestrator pod hammer the server: with N pods
		// × 1 connection per poll × 1 poll / 30s, total poller load
		// is N/30 conn-seconds/sec, negligible against a 50-conn cap.
		interval = 30 * time.Second
	}
	return &Poller{pool: cfg.Pool, metrics: metrics, interval: interval}, nil
}

// Run blocks until ctx is canceled. The first poll fires
// immediately so the gauges have a value before the first scrape
// window elapses; subsequent polls fire on the interval ticker.
func (p *Poller) Run(ctx context.Context) error {
	p.pollOnce(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

// pollOnce reads both values inside a single 5s timeout. A failure
// at either query records query_failed and returns without updating
// the gauges, so a stale value never overwrites with a wrong one.
func (p *Poller) pollOnce(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var backends int
	if err := p.pool.QueryRow(
		queryCtx,
		`SELECT COALESCE(SUM(numbackends), 0)::int FROM pg_stat_database`,
	).Scan(&backends); err != nil {
		p.metrics.RecordPollOutcome("query_failed")
		slog.Warn("pgstats backend connections poll failed", "error", err)
		return
	}

	var maxConns int
	if err := p.pool.QueryRow(
		queryCtx,
		`SELECT current_setting('max_connections')::int`,
	).Scan(&maxConns); err != nil {
		p.metrics.RecordPollOutcome("query_failed")
		slog.Warn("pgstats max_connections poll failed", "error", err)
		return
	}

	p.metrics.RecordBackendConnections(float64(backends))
	p.metrics.RecordMaxConnections(float64(maxConns))
	p.metrics.RecordPollOutcome("ok")
}
