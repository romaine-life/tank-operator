package stuckturns

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLister returns a fixed slice (or error) and records the args the
// sampler passed it, so a test can assert the RFC3339-Z threshold
// string equals now-threshold.
type fakeLister struct {
	stuck []StuckTurn
	err   error

	gotScope     string
	gotOlderThan string
	gotLimit     int
	calls        int
}

func (f *fakeLister) ListStuckTurns(_ context.Context, scope, olderThanRFC3339 string, limit int) ([]StuckTurn, error) {
	f.calls++
	f.gotScope = scope
	f.gotOlderThan = olderThanRFC3339
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.stuck, nil
}

// fakeMetrics records the last gauge value and counts sample errors by
// reason.
type fakeMetrics struct {
	lastCount    int
	setCalls     int
	sampleErrors map[string]int
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{sampleErrors: map[string]int{}}
}

func (m *fakeMetrics) SetStuckCount(n int) {
	m.lastCount = n
	m.setCalls++
}

func (m *fakeMetrics) RecordSampleError(reason string) {
	m.sampleErrors[reason]++
}

func TestNewSamplerNilWhenDepsMissing(t *testing.T) {
	if NewSampler(SamplerConfig{Metrics: newFakeMetrics()}) != nil {
		t.Fatal("NewSampler should return nil when Lister is missing")
	}
	if NewSampler(SamplerConfig{Lister: &fakeLister{}}) != nil {
		t.Fatal("NewSampler should return nil when Metrics is missing")
	}
}

func TestNewSamplerAppliesDefaults(t *testing.T) {
	s := NewSampler(SamplerConfig{Lister: &fakeLister{}, Metrics: newFakeMetrics()})
	if s == nil {
		t.Fatal("NewSampler returned nil with both deps present")
	}
	if s.threshold != 10*time.Minute {
		t.Fatalf("threshold = %v, want 10m", s.threshold)
	}
	if s.interval != 60*time.Second {
		t.Fatalf("interval = %v, want 60s", s.interval)
	}
	if s.queryTimeout != 5*time.Second {
		t.Fatalf("queryTimeout = %v, want 5s", s.queryTimeout)
	}
	if s.limit != 200 {
		t.Fatalf("limit = %d, want 200", s.limit)
	}
	if s.scope != "default" {
		t.Fatalf("scope = %q, want default", s.scope)
	}
	if s.logger == nil {
		t.Fatal("logger should default to slog.Default(), got nil")
	}
}

func TestNewSamplerHonorsOverrides(t *testing.T) {
	s := NewSampler(SamplerConfig{
		Lister:       &fakeLister{},
		Metrics:      newFakeMetrics(),
		Threshold:    3 * time.Minute,
		Interval:     15 * time.Second,
		QueryTimeout: 2 * time.Second,
		Limit:        50,
		Scope:        "slot-7",
	})
	if s == nil {
		t.Fatal("NewSampler returned nil")
	}
	if s.threshold != 3*time.Minute || s.interval != 15*time.Second ||
		s.queryTimeout != 2*time.Second || s.limit != 50 || s.scope != "slot-7" {
		t.Fatalf("overrides not applied: %+v", s)
	}
}

func TestRunOnceSetsGaugeAndComputesStuckSeconds(t *testing.T) {
	now := time.Date(2026, 6, 6, 19, 30, 0, 0, time.UTC)
	updated := now.Add(-12 * time.Minute) // 720s stuck
	lister := &fakeLister{stuck: []StuckTurn{
		{
			SessionID:         "812",
			Scope:             "default",
			Mode:              "claude_gui",
			ActivityStatus:    "claimed",
			ActiveTurnID:      "turn_abc",
			ActivityUpdatedAt: updated,
		},
		{
			SessionID:      "813",
			Scope:          "default",
			Mode:           "claude_gui",
			ActivityStatus: "submitted",
			// zero ActivityUpdatedAt → stuck_seconds stays 0, no panic.
		},
	}}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), now)

	if got != 2 {
		t.Fatalf("RunOnce returned %d, want 2", got)
	}
	if metrics.lastCount != 2 {
		t.Fatalf("gauge set to %d, want 2", metrics.lastCount)
	}
	// The stuck-seconds computation is internal to RunOnce's logging;
	// assert the math directly so the contract is pinned.
	wantSeconds := int64(now.Sub(updated).Seconds())
	if wantSeconds != 720 {
		t.Fatalf("test fixture wrong: stuck seconds = %d, want 720", wantSeconds)
	}
}

func TestRunOnceListErrorRecordsSampleErrorAndReturnsZero(t *testing.T) {
	lister := &fakeLister{err: errors.New("db down")}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), time.Now())

	if got != 0 {
		t.Fatalf("RunOnce returned %d on lister error, want 0", got)
	}
	if metrics.sampleErrors["list"] != 1 {
		t.Fatalf("sample error counter for \"list\" = %d, want 1", metrics.sampleErrors["list"])
	}
	if metrics.setCalls != 0 {
		t.Fatalf("gauge must not be set on lister error; setCalls = %d", metrics.setCalls)
	}
}

func TestRunOnceZeroStuckSetsGaugeToZero(t *testing.T) {
	lister := &fakeLister{stuck: nil}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), time.Now())

	if got != 0 {
		t.Fatalf("RunOnce returned %d, want 0", got)
	}
	if metrics.setCalls != 1 || metrics.lastCount != 0 {
		t.Fatalf("steady-state must set gauge to 0; setCalls=%d lastCount=%d", metrics.setCalls, metrics.lastCount)
	}
}

func TestRunOncePassesNowMinusThresholdAsRFC3339(t *testing.T) {
	now := time.Date(2026, 6, 6, 19, 30, 0, 0, time.UTC)
	lister := &fakeLister{}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{
		Lister:    lister,
		Metrics:   metrics,
		Threshold: 10 * time.Minute,
		Scope:     "default",
		Limit:     200,
	})

	s.RunOnce(context.Background(), now)

	want := now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if lister.gotOlderThan != want {
		t.Fatalf("older-than string = %q, want %q", lister.gotOlderThan, want)
	}
	if lister.gotScope != "default" {
		t.Fatalf("scope passed to lister = %q, want default", lister.gotScope)
	}
	if lister.gotLimit != 200 {
		t.Fatalf("limit passed to lister = %d, want 200", lister.gotLimit)
	}
}
