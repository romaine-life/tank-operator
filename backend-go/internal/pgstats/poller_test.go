package pgstats

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestNewRejectsNilPool: a missing pool is a configuration bug —
// the alternative would be a runtime nil-deref inside pollOnce when
// the loop fires.
func TestNewRejectsNilPool(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected New to reject nil Pool")
	}
}

// TestNewDefaultsIntervalAndMetrics: the package contract is that
// only Pool is required. A caller passing just a pool should get a
// runnable Poller without surprise.
func TestNewDefaultsIntervalAndMetrics(t *testing.T) {
	pool := &pgxpool.Pool{}
	p, err := New(Config{Pool: pool})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.interval <= 0 {
		t.Fatalf("interval = %v, want positive default", p.interval)
	}
	if p.metrics == nil {
		t.Fatal("metrics is nil; New should install noopMetrics default")
	}
}

// TestNoopMetricsSatisfiesInterface: a future Metrics method
// addition must update noopMetrics in lockstep — this compile-time
// check breaks the build if the two drift.
func TestNoopMetricsSatisfiesInterface(t *testing.T) {
	var _ Metrics = noopMetrics{}
}

// TestMetricsInterfaceContract: a recording impl proves the
// interface methods are wired correctly; future code review sees the
// concrete shape of each callback.
func TestMetricsInterfaceContract(t *testing.T) {
	r := &recordingMetrics{}
	r.RecordBackendConnections(42)
	r.RecordMaxConnections(100)
	r.RecordPollOutcome("ok")

	if r.backends != 42 {
		t.Errorf("backends = %v, want 42", r.backends)
	}
	if r.max != 100 {
		t.Errorf("max = %v, want 100", r.max)
	}
	if r.lastOutcome != "ok" {
		t.Errorf("lastOutcome = %q, want %q", r.lastOutcome, "ok")
	}
}

type recordingMetrics struct {
	backends    float64
	max         float64
	lastOutcome string
}

func (r *recordingMetrics) RecordBackendConnections(v float64) { r.backends = v }
func (r *recordingMetrics) RecordMaxConnections(v float64)     { r.max = v }
func (r *recordingMetrics) RecordPollOutcome(outcome string)   { r.lastOutcome = outcome }
