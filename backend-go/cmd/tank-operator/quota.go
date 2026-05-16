package main

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

// Service-principal session-creation quota. Two distinct caps:
//
//   1. Per-`sub` rate limit on spawn calls in a sliding minute. Caps a
//      runaway agent loop inside a single session pod — the failure mode
//      where the in-pod LLM gets stuck in a state that keeps calling
//      spawn_service_session.
//   2. Per-`actor_email` concurrent active session count. Caps the
//      human owner's blast radius even if an attacker forges service
//      tokens for many distinct subs sharing the same actor.
//
// Ceilings are env-driven with conservative defaults. See
// nelsong6/tank-operator#486 stage 6.

const (
	defaultServiceSpawnRatePerMin       = 5
	defaultServiceSpawnConcurrentPerActor = 10
)

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func serviceSpawnRatePerMin() int {
	return envInt("SERVICE_SPAWN_RATE_PER_MIN", defaultServiceSpawnRatePerMin)
}

func serviceSpawnConcurrentPerActor() int {
	return envInt("SERVICE_SPAWN_CONCURRENT_PER_ACTOR", defaultServiceSpawnConcurrentPerActor)
}

// QuotaReason is the closed set surfaced as the `reason` Prometheus
// label on tank_service_role_quota_throttle_total. Sub and actor_email
// are intentionally NOT labels (unbounded cardinality).
type QuotaReason string

const (
	QuotaReasonRate       QuotaReason = "rate_limit"
	QuotaReasonConcurrent QuotaReason = "concurrent_cap"
)

var quotaThrottleTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_service_role_quota_throttle_total",
		Help: "Service-principal spawn attempts rejected by quota, by reason.",
	},
	[]string{"reason"},
)

func recordQuotaThrottle(reason QuotaReason) {
	quotaThrottleTotal.WithLabelValues(string(reason)).Inc()
}

// SpawnQuotaTracker enforces the per-sub rate limit. Concurrent-cap
// enforcement queries the session manager fresh per call so it doesn't
// need internal state. The tracker is goroutine-safe.
type SpawnQuotaTracker struct {
	mu          sync.Mutex
	windowStart map[string]time.Time
	counts      map[string]int
	windowSize  time.Duration
}

func NewSpawnQuotaTracker() *SpawnQuotaTracker {
	return &SpawnQuotaTracker{
		windowStart: map[string]time.Time{},
		counts:      map[string]int{},
		windowSize:  time.Minute,
	}
}

// CheckRate returns true if `sub` is within the per-minute ceiling and
// records the attempt. Sliding windows are minute-aligned per sub:
// crossing into a new minute resets the count. Returns false (and
// increments the throttle counter) when the ceiling is reached.
func (q *SpawnQuotaTracker) CheckRate(sub string, ceiling int) bool {
	if ceiling <= 0 || sub == "" {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	start, ok := q.windowStart[sub]
	if !ok || now.Sub(start) >= q.windowSize {
		q.windowStart[sub] = now
		q.counts[sub] = 1
		return true
	}
	if q.counts[sub] >= ceiling {
		recordQuotaThrottle(QuotaReasonRate)
		return false
	}
	q.counts[sub]++
	return true
}

// CheckConcurrentCap counts the actor's currently-active sessions via
// the session manager. Returns true when below the cap. Active means
// any pod the manager surfaces in ListSessions — pending, ready,
// running. Terminated sessions don't count because ListSessions
// filters them out by label/annotation lifecycle.
//
// Distinct from CheckRate: the rate limit is per-pod (sub), the
// concurrent cap is per-human (actor_email).
func CheckConcurrentCap(ctx context.Context, mgr *sessions.Manager, actorEmail string, ceiling int) (bool, error) {
	if ceiling <= 0 || actorEmail == "" || mgr == nil {
		return true, nil
	}
	infos, err := mgr.ListSessions(ctx, actorEmail)
	if err != nil {
		return false, err
	}
	if len(infos) >= ceiling {
		recordQuotaThrottle(QuotaReasonConcurrent)
		return false, nil
	}
	return true, nil
}
