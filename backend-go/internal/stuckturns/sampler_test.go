package stuckturns

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLister returns fixed slices (or errors) per class and records the
// args the sampler passed it, so a test can assert the accepted
// RFC3339-Z threshold string and the streaming time.Time bound.
type fakeLister struct {
	stuck        []StuckTurn
	err          error
	streaming    []StuckTurn
	streamingErr error

	gotScope           string
	gotOlderThan       string
	gotLimit           int
	gotStreamingScope  string
	gotLastEventBefore time.Time
	gotStreamingLimit  int
	calls              int
	streamingCalls     int
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

func (f *fakeLister) ListStreamingStuckTurns(_ context.Context, scope string, lastEventBefore time.Time, limit int) ([]StuckTurn, error) {
	f.streamingCalls++
	f.gotStreamingScope = scope
	f.gotLastEventBefore = lastEventBefore
	f.gotStreamingLimit = limit
	if f.streamingErr != nil {
		return nil, f.streamingErr
	}
	return f.streaming, nil
}

// fakeMetrics records the last gauge value per phase and counts sample
// errors by reason.
type fakeMetrics struct {
	lastCount    map[string]int
	setCalls     map[string]int
	sampleErrors map[string]int
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		lastCount:    map[string]int{},
		setCalls:     map[string]int{},
		sampleErrors: map[string]int{},
	}
}

func (m *fakeMetrics) SetStuckCount(phase string, n int) {
	m.lastCount[phase] = n
	m.setCalls[phase]++
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
	if s.streamingThreshold != 20*time.Minute {
		t.Fatalf("streamingThreshold = %v, want 20m", s.streamingThreshold)
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
		Lister:             &fakeLister{},
		Metrics:            newFakeMetrics(),
		Threshold:          3 * time.Minute,
		StreamingThreshold: 7 * time.Minute,
		Interval:           15 * time.Second,
		QueryTimeout:       2 * time.Second,
		Limit:              50,
		Scope:              "slot-7",
	})
	if s == nil {
		t.Fatal("NewSampler returned nil")
	}
	if s.threshold != 3*time.Minute || s.streamingThreshold != 7*time.Minute ||
		s.interval != 15*time.Second || s.queryTimeout != 2*time.Second ||
		s.limit != 50 || s.scope != "slot-7" {
		t.Fatalf("overrides not applied: %+v", s)
	}
}

func TestRunOnceSetsGaugePerPhaseAndComputesStuckSeconds(t *testing.T) {
	now := time.Date(2026, 6, 12, 3, 39, 0, 0, time.UTC)
	updated := now.Add(-12 * time.Minute) // 720s stuck (accepted basis)
	lister := &fakeLister{
		stuck: []StuckTurn{
			{
				SessionID:         "812",
				Scope:             "default",
				Mode:              "claude_gui",
				Phase:             PhaseAccepted,
				ActivityStatus:    "claimed",
				ActiveTurnID:      "turn_abc",
				ActivityUpdatedAt: updated,
			},
			{
				SessionID:      "813",
				Scope:          "default",
				Mode:           "claude_gui",
				Phase:          PhaseAccepted,
				ActivityStatus: "submitted",
				// zero ActivityUpdatedAt → stuck_seconds stays 0, no panic.
			},
		},
		// Session 828's exact incident shape (tank-operator#1085): a
		// streaming session whose last ledger event (the final answer at
		// 03:05:59) is half an hour old with the turn still open.
		streaming: []StuckTurn{
			{
				SessionID:      "828",
				Scope:          "default",
				Mode:           "antigravity_gui",
				Phase:          PhaseStreaming,
				ActivityStatus: "streaming",
				ActiveTurnID:   "turn_7fcfb58b",
				LastEventAt:    time.Date(2026, 6, 12, 3, 5, 59, 0, time.UTC),
			},
		},
	}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), now)

	if got != 3 {
		t.Fatalf("RunOnce returned %d, want 3 (2 accepted + 1 streaming)", got)
	}
	if metrics.lastCount[PhaseAccepted] != 2 {
		t.Fatalf("accepted gauge set to %d, want 2", metrics.lastCount[PhaseAccepted])
	}
	if metrics.lastCount[PhaseStreaming] != 1 {
		t.Fatalf("streaming gauge set to %d, want 1", metrics.lastCount[PhaseStreaming])
	}
	// The stuck-seconds computation is internal to RunOnce's logging;
	// assert the basis math directly so the contract is pinned: accepted
	// rows anchor on ActivityUpdatedAt, streaming rows on LastEventAt.
	if wantSeconds := int64(now.Sub(updated).Seconds()); wantSeconds != 720 {
		t.Fatalf("accepted fixture wrong: stuck seconds = %d, want 720", wantSeconds)
	}
	if basis := lister.streaming[0].stuckBasis(); !basis.Equal(lister.streaming[0].LastEventAt) {
		t.Fatalf("streaming stuck basis = %v, want LastEventAt %v", basis, lister.streaming[0].LastEventAt)
	}
	if basis := lister.stuck[0].stuckBasis(); !basis.Equal(updated) {
		t.Fatalf("accepted stuck basis = %v, want ActivityUpdatedAt %v", basis, updated)
	}
}

func TestRunOnceAcceptedListErrorStillSamplesStreaming(t *testing.T) {
	lister := &fakeLister{
		err: errors.New("db down"),
		streaming: []StuckTurn{
			{SessionID: "829", Phase: PhaseStreaming, ActivityStatus: "streaming"},
		},
	}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), time.Now())

	if got != 1 {
		t.Fatalf("RunOnce returned %d, want 1 (streaming pass must survive an accepted-pass error)", got)
	}
	if metrics.sampleErrors["list"] != 1 {
		t.Fatalf("sample error counter for \"list\" = %d, want 1", metrics.sampleErrors["list"])
	}
	if metrics.setCalls[PhaseAccepted] != 0 {
		t.Fatalf("accepted gauge must not be set on its lister error; setCalls = %d", metrics.setCalls[PhaseAccepted])
	}
	if metrics.lastCount[PhaseStreaming] != 1 {
		t.Fatalf("streaming gauge = %d, want 1", metrics.lastCount[PhaseStreaming])
	}
}

func TestRunOnceStreamingListErrorStillSamplesAccepted(t *testing.T) {
	lister := &fakeLister{
		stuck:        []StuckTurn{{SessionID: "812", Phase: PhaseAccepted, ActivityStatus: "claimed"}},
		streamingErr: errors.New("db down"),
	}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), time.Now())

	if got != 1 {
		t.Fatalf("RunOnce returned %d, want 1 (accepted pass must survive a streaming-pass error)", got)
	}
	if metrics.sampleErrors["list_streaming"] != 1 {
		t.Fatalf("sample error counter for \"list_streaming\" = %d, want 1", metrics.sampleErrors["list_streaming"])
	}
	if metrics.setCalls[PhaseStreaming] != 0 {
		t.Fatalf("streaming gauge must not be set on its lister error; setCalls = %d", metrics.setCalls[PhaseStreaming])
	}
	if metrics.lastCount[PhaseAccepted] != 1 {
		t.Fatalf("accepted gauge = %d, want 1", metrics.lastCount[PhaseAccepted])
	}
}

func TestRunOnceZeroStuckSetsBothPhaseGaugesToZero(t *testing.T) {
	lister := &fakeLister{}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{Lister: lister, Metrics: metrics})

	got := s.RunOnce(context.Background(), time.Now())

	if got != 0 {
		t.Fatalf("RunOnce returned %d, want 0", got)
	}
	if metrics.setCalls[PhaseAccepted] != 1 || metrics.lastCount[PhaseAccepted] != 0 {
		t.Fatalf("steady-state must set accepted gauge to 0; setCalls=%d lastCount=%d",
			metrics.setCalls[PhaseAccepted], metrics.lastCount[PhaseAccepted])
	}
	if metrics.setCalls[PhaseStreaming] != 1 || metrics.lastCount[PhaseStreaming] != 0 {
		t.Fatalf("steady-state must set streaming gauge to 0; setCalls=%d lastCount=%d",
			metrics.setCalls[PhaseStreaming], metrics.lastCount[PhaseStreaming])
	}
}

func TestRunOncePassesThresholdsPerClass(t *testing.T) {
	now := time.Date(2026, 6, 12, 3, 39, 0, 0, time.UTC)
	lister := &fakeLister{}
	metrics := newFakeMetrics()
	s := NewSampler(SamplerConfig{
		Lister:             lister,
		Metrics:            metrics,
		Threshold:          10 * time.Minute,
		StreamingThreshold: 20 * time.Minute,
		Scope:              "default",
		Limit:              200,
	})

	s.RunOnce(context.Background(), now)

	want := now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if lister.gotOlderThan != want {
		t.Fatalf("accepted older-than string = %q, want %q", lister.gotOlderThan, want)
	}
	wantStreaming := now.Add(-20 * time.Minute).UTC()
	if !lister.gotLastEventBefore.Equal(wantStreaming) {
		t.Fatalf("streaming last-event-before = %v, want %v", lister.gotLastEventBefore, wantStreaming)
	}
	if lister.gotScope != "default" || lister.gotStreamingScope != "default" {
		t.Fatalf("scopes passed to lister = %q / %q, want default/default", lister.gotScope, lister.gotStreamingScope)
	}
	if lister.gotLimit != 200 || lister.gotStreamingLimit != 200 {
		t.Fatalf("limits passed to lister = %d / %d, want 200/200", lister.gotLimit, lister.gotStreamingLimit)
	}
}
