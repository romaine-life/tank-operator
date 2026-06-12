package main

import (
	"fmt"
	"testing"
	"time"
)

// TestSpawnQuotaTrackerEvictsExpiredWindows pins the #1079-item-6 bound:
// a long-lived replica that has seen many distinct service principals
// must not grow the tracker maps forever. Expired windows are evicted
// lazily once the map crosses the threshold; live windows survive.
func TestSpawnQuotaTrackerEvictsExpiredWindows(t *testing.T) {
	q := NewSpawnQuotaTracker()
	// Overfill with expired windows.
	expired := time.Now().Add(-2 * time.Minute)
	for i := 0; i < quotaEvictThreshold+10; i++ {
		sub := fmt.Sprintf("sub-%d", i)
		q.windowStart[sub] = expired
		q.counts[sub] = 1
	}
	// A live window that must survive the sweep.
	q.windowStart["sub-live"] = time.Now()
	q.counts["sub-live"] = 1

	if !q.CheckRate("sub-new", 5) {
		t.Fatal("fresh sub must be admitted")
	}
	if len(q.windowStart) > 3 {
		t.Fatalf("expired windows not evicted: %d entries remain", len(q.windowStart))
	}
	if _, ok := q.windowStart["sub-live"]; !ok {
		t.Fatal("live window must survive eviction")
	}
	if _, ok := q.windowStart["sub-new"]; !ok {
		t.Fatal("the admitting sub must be tracked")
	}
}
