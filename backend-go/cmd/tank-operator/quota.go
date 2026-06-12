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
// enforced here (romaine-life/tank-operator#486 stage 6) but was removed:
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
//
// Scope notes (issue #1079 item 6):
//   - State is per-replica memory, so the effective fleet-wide ceiling is
//     ceiling × replicas (×2 today). That slack is deliberate: this guard
//     exists to stop a runaway in-pod agent loop, not to meter usage —
//     a durable shared counter would buy precision this failure mode
//     doesn't need at the cost of a DB round-trip per spawn.
//   - Expired windows are evicted lazily once the map crosses
//     quotaEvictThreshold, so a long-lived replica seeing many distinct
//     `sub`s no longer grows these maps forever.
type SpawnQuotaTracker struct {
	mu          sync.Mutex
	windowStart map[string]time.Time
	counts      map[string]int
	windowSize  time.Duration
}

// quotaEvictThreshold bounds the tracker maps: far above any legitimate
// concurrent-principal count, far below "leak worth paging about".
const quotaEvictThreshold = 1024

// evictExpiredLocked drops every sub whose window has lapsed. Caller holds mu.
func (q *SpawnQuotaTracker) evictExpiredLocked(now time.Time) {
	for sub, start := range q.windowStart {
		if now.Sub(start) >= q.windowSize {
			delete(q.windowStart, sub)
			delete(q.counts, sub)
		}
	}
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
	if len(q.windowStart) > quotaEvictThreshold {
		q.evictExpiredLocked(now)
	}
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
