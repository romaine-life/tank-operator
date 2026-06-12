package main

import (
	"context"
	"strings"
	"sync"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// turnActivityCache memoizes projectTurnPages results (issue #1077 item 1).
//
// The turn-activity endpoint used to re-read the WHOLE turn (plus its
// transitively-adopted background-wake chain) and re-render EVERY page on
// EVERY request, returning one page — while the SPA refetches per live
// transcript batch on a 120ms debounce and Turns is the default landing
// view. One viewer on a flood-class turn reproduced the #1051 write-side
// incident on the read side of the shared B1ms.
//
// The cache exploits the page model's own invariant ("a sealed page is
// immutable; only the live last page changes as events arrive") one level
// up: the ENTIRE projection is a pure function of the turn's event set, so
// it is reusable until any event lands on the origin turn or any turn of
// its wake chain. Freshness is a per-candidate-turn high-water mark:
//
//   - candidates = the origin turn + every wake-continuation turn id
//     DERIVABLE from the events that were projected (a new chain member can
//     only appear via a new parent event, which moves the parent's mark);
//   - the stored mark per candidate is the max order_key OBSERVED in the
//     projected events (identical to the DB max at read time, because
//     readAllTurnEvents reads each candidate to exhaustion);
//   - a request probes MaxOrderKeyForTurn per candidate (one backward scan
//     of the session_events_turn_order index each) and reuses the cached
//     projection on exact equality.
//
// Misses single-flight per key so N concurrent viewers of one flood turn
// cost one fold, not N. Entries are per-process (each replica warms its
// own) and evicted LRU by total projected-event count, so one giant turn
// cannot pin unbounded memory.
//
// Callers MUST treat the returned projection as immutable — it is shared
// across requests.

type turnActivityCacheEntry struct {
	candidates []string
	freshness  string
	projection turnPagesProjection
	eventCount int
	lastUse    uint64
}

type turnActivityCache struct {
	mu          sync.Mutex
	entries     map[string]*turnActivityCacheEntry
	inflight    map[string]chan struct{}
	useSeq      uint64
	totalEvents int
	maxEntries  int
	maxEvents   int
}

func newTurnActivityCache() *turnActivityCache {
	return &turnActivityCache{
		entries:    map[string]*turnActivityCacheEntry{},
		inflight:   map[string]chan struct{}{},
		maxEntries: envInt("TURN_ACTIVITY_CACHE_MAX_ENTRIES", 128),
		maxEvents:  envInt("TURN_ACTIVITY_CACHE_MAX_EVENTS", 200_000),
	}
}

const turnActivityCacheKeySeparator = "\x1f"

func turnActivityCacheKey(scope, sessionID, turnID string) string {
	return scope + turnActivityCacheKeySeparator + sessionID + turnActivityCacheKeySeparator + turnID
}

// projectionFor returns the turn's full page projection, serving from cache
// when the turn (and its wake chain) has not advanced.
func (c *turnActivityCache) projectionFor(
	ctx context.Context,
	eventStore store.SessionEventStore,
	scope, sessionID, turnID string,
) (turnPagesProjection, error) {
	key := turnActivityCacheKey(scope, sessionID, turnID)
	for {
		c.mu.Lock()
		entry := c.entries[key]
		var candidates []string
		if entry != nil {
			candidates = entry.candidates
		}
		c.mu.Unlock()

		if entry != nil {
			probed, err := c.probeFreshness(ctx, eventStore, sessionID, candidates)
			if err != nil {
				return turnPagesProjection{}, err
			}
			c.mu.Lock()
			current := c.entries[key]
			if current == entry && current.freshness == probed {
				c.useSeq++
				current.lastUse = c.useSeq
				c.mu.Unlock()
				recordTurnActivityCache("hit")
				return current.projection, nil
			}
			c.mu.Unlock()
			recordTurnActivityCache("stale")
		}

		// Miss or stale: single-flight the fold per key.
		c.mu.Lock()
		if wait, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-wait:
				// The folding request finished — re-check the cache.
				continue
			case <-ctx.Done():
				return turnPagesProjection{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		projection, candidates, observed, err := c.foldAndStore(ctx, eventStore, key, sessionID, turnID)
		c.mu.Lock()
		delete(c.inflight, key)
		close(done)
		c.mu.Unlock()
		if err != nil {
			return turnPagesProjection{}, err
		}
		_ = candidates
		_ = observed
		if entry == nil {
			recordTurnActivityCache("miss")
		}
		return projection, nil
	}
}

func (c *turnActivityCache) probeFreshness(
	ctx context.Context,
	eventStore store.SessionEventStore,
	sessionID string,
	candidates []string,
) (string, error) {
	marks := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		mark, err := eventStore.MaxOrderKeyForTurn(ctx, sessionID, candidate)
		if err != nil {
			return "", err
		}
		marks = append(marks, candidate+"="+mark)
	}
	return strings.Join(marks, turnActivityCacheKeySeparator), nil
}

func freshnessFromObserved(candidates []string, observed map[string]string) string {
	marks := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		marks = append(marks, candidate+"="+observed[candidate])
	}
	return strings.Join(marks, turnActivityCacheKeySeparator)
}

func (c *turnActivityCache) foldAndStore(
	ctx context.Context,
	eventStore store.SessionEventStore,
	key, sessionID, turnID string,
) (turnPagesProjection, []string, map[string]string, error) {
	events, candidates, observed, err := readUserFacingTurnEventsWithChain(ctx, eventStore, sessionID, turnID)
	if err != nil {
		return turnPagesProjection{}, nil, nil, err
	}
	projection := projectTurnPages(turnID, events)
	entry := &turnActivityCacheEntry{
		candidates: candidates,
		freshness:  freshnessFromObserved(candidates, observed),
		projection: projection,
		eventCount: len(events),
	}
	c.mu.Lock()
	if prior := c.entries[key]; prior != nil {
		c.totalEvents -= prior.eventCount
	}
	c.useSeq++
	entry.lastUse = c.useSeq
	c.entries[key] = entry
	c.totalEvents += entry.eventCount
	c.evictLocked(key)
	recordTurnActivityCacheSize(len(c.entries), c.totalEvents)
	c.mu.Unlock()
	return projection, candidates, observed, nil
}

// evictLocked drops least-recently-used entries until the cache fits its
// bounds. `keep` (the entry just stored) is never evicted — a single turn
// larger than maxEvents still caches as the lone entry.
func (c *turnActivityCache) evictLocked(keep string) {
	for (len(c.entries) > c.maxEntries || c.totalEvents > c.maxEvents) && len(c.entries) > 1 {
		oldestKey := ""
		var oldestUse uint64
		for key, entry := range c.entries {
			if key == keep {
				continue
			}
			if oldestKey == "" || entry.lastUse < oldestUse {
				oldestKey = key
				oldestUse = entry.lastUse
			}
		}
		if oldestKey == "" {
			return
		}
		c.totalEvents -= c.entries[oldestKey].eventCount
		delete(c.entries, oldestKey)
		recordTurnActivityCache("evicted")
	}
}

// ensureTurnActivityCache lazily constructs the cache so test fixtures that
// build appServer literally (without main.go's constructor) still get one.
func (s *appServer) ensureTurnActivityCache() *turnActivityCache {
	if s.turnActivity == nil {
		s.turnActivity = newTurnActivityCache()
	}
	return s.turnActivity
}
