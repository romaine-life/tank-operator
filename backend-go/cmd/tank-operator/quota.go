package main

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Service-principal session-creation quota.
//
// Per-`sub` rate limit on spawn calls in a sliding minute. Caps a
// runaway agent loop inside a single session pod — the failure mode
// where the in-pod LLM gets stuck in a state that keeps calling
// spawn_service_session.
//
// A per-`actor_email` concurrent-active-session cap was previously
// enforced here (nelsong6/tank-operator#486 stage 6) but was removed:
// it tripped on normal multi-session usage by a single human (UI-driven
// session creation is uncapped, so service-principal spawns from inside
// a session bumped against an unrelated count). When we add a
// blast-radius guard back, design it so legitimate human concurrency
// doesn't trip it — exempt super-admins, filter to recently-active
// sessions, or scope the count to service-spawned siblings.
//
// Rate-limit ceiling is env-driven with a conservative default.

const (
	defaultServiceSpawnRatePerMin = 5
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

// QuotaReason is the closed set surfaced as the `reason` Prometheus
// label on tank_service_role_quota_throttle_total. Sub and actor_email
// are intentionally NOT labels (unbounded cardinality).
type QuotaReason string

const (
	QuotaReasonRate QuotaReason = "rate_limit"
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

// SpawnQuotaTracker enforces the per-sub rate limit. Goroutine-safe.
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

