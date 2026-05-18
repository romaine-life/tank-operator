package main

import (
	"testing"
)

// Per-`sub` rate-limit window behavior. CheckRate slides by sub: each
// distinct sub gets its own budget, so a noisy pod cannot starve a
// quiet one. The same sub crossing the ceiling within a window is
// rejected; the throttle counter is incremented in the same call so
// the dashboard sees the reject the moment it happens.

func TestSpawnQuotaTracker_AllowsCallsUnderCeiling(t *testing.T) {
	q := NewSpawnQuotaTracker()
	for i := 0; i < 5; i++ {
		if !q.CheckRate("svc:tank:session-a", 5) {
			t.Fatalf("call #%d rejected; want allowed (under ceiling 5)", i+1)
		}
	}
}

func TestSpawnQuotaTracker_RejectsAtCeiling(t *testing.T) {
	q := NewSpawnQuotaTracker()
	for i := 0; i < 3; i++ {
		if !q.CheckRate("svc:tank:session-a", 3) {
			t.Fatalf("call #%d rejected within ceiling", i+1)
		}
	}
	if q.CheckRate("svc:tank:session-a", 3) {
		t.Fatal("call past ceiling accepted; want rejected")
	}
}

func TestSpawnQuotaTracker_PerSubIsolation(t *testing.T) {
	// One sub exhausting its budget must not starve another sub.
	q := NewSpawnQuotaTracker()
	for i := 0; i < 5; i++ {
		_ = q.CheckRate("svc:tank:session-loud", 5)
	}
	if q.CheckRate("svc:tank:session-loud", 5) {
		t.Fatal("loud sub past ceiling accepted")
	}
	if !q.CheckRate("svc:tank:session-quiet", 5) {
		t.Fatal("quiet sub rejected; want isolated from loud sub")
	}
}

func TestSpawnQuotaTracker_ZeroCeilingDisablesGate(t *testing.T) {
	// Ceiling 0 (or negative) means "no limit" — useful for opt-out.
	q := NewSpawnQuotaTracker()
	for i := 0; i < 100; i++ {
		if !q.CheckRate("svc:tank:session-a", 0) {
			t.Fatalf("call #%d rejected with ceiling 0; want unlimited", i+1)
		}
	}
}

func TestSpawnQuotaTracker_EmptySubBypasses(t *testing.T) {
	// Defensive: if Verifier ever returns a User with an empty Sub
	// (shouldn't happen — actor_email check would fire first), don't
	// crash. Don't apply the rate limit either; the rate limit is the
	// only spawn gate today (the actor_email concurrent-cap was
	// removed; see quota.go).
	q := NewSpawnQuotaTracker()
	for i := 0; i < 100; i++ {
		if !q.CheckRate("", 5) {
			t.Fatalf("empty-sub call #%d rejected; want bypass", i+1)
		}
	}
}

func TestServiceSpawnRatePerMin_DefaultAndEnvOverride(t *testing.T) {
	if got, want := serviceSpawnRatePerMin(), defaultServiceSpawnRatePerMin; got != want {
		t.Fatalf("default = %d, want %d", got, want)
	}
	t.Setenv("SERVICE_SPAWN_RATE_PER_MIN", "42")
	if got := serviceSpawnRatePerMin(); got != 42 {
		t.Fatalf("env override = %d, want 42", got)
	}
}

func TestServiceSpawn_InvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("SERVICE_SPAWN_RATE_PER_MIN", "not-a-number")
	if got, want := serviceSpawnRatePerMin(), defaultServiceSpawnRatePerMin; got != want {
		t.Fatalf("invalid env should fall back to default %d; got %d", want, got)
	}
	t.Setenv("SERVICE_SPAWN_RATE_PER_MIN", "-3")
	if got, want := serviceSpawnRatePerMin(), defaultServiceSpawnRatePerMin; got != want {
		t.Fatalf("negative env should fall back to default %d; got %d", want, got)
	}
}
