package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// cacheFakeEventStore is a minimal SessionEventStore for the cache tests:
// per-turn event sets with call counting, embedding the stub for the rest of
// the interface.
type cacheFakeEventStore struct {
	store.StubSessionEventStore
	mu        sync.Mutex
	turns     map[string][]map[string]any
	turnReads int
	maxProbes int
}

func (s *cacheFakeEventStore) eventsFor(turnID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.turns[turnID]))
	copy(out, s.turns[turnID])
	return out
}

func (s *cacheFakeEventStore) EventsForTurnAfter(_ context.Context, _, turnID, afterOrderKey string, _ int) (store.SessionEventPage, error) {
	s.mu.Lock()
	s.turnReads++
	s.mu.Unlock()
	var out []map[string]any
	for _, event := range s.eventsFor(turnID) {
		if key, _ := event["order_key"].(string); afterOrderKey == "" || key > afterOrderKey {
			out = append(out, event)
		}
	}
	return store.SessionEventPage{Events: out, FoundNewest: true}, nil
}

func (s *cacheFakeEventStore) MaxOrderKeyForTurn(_ context.Context, _, turnID string) (string, error) {
	s.mu.Lock()
	s.maxProbes++
	s.mu.Unlock()
	max := ""
	for _, event := range s.eventsFor(turnID) {
		if key, _ := event["order_key"].(string); key > max {
			max = key
		}
	}
	return max, nil
}

func (s *cacheFakeEventStore) addEvent(turnID string, event map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = map[string][]map[string]any{}
	}
	s.turns[turnID] = append(s.turns[turnID], event)
}

func cacheTurnEvent(turnID, orderKey, eventType string) map[string]any {
	return map[string]any{
		"event_id":   turnID + ":" + eventType + ":" + orderKey,
		"session_id": "63",
		"turn_id":    turnID,
		"type":       eventType,
		"actor":      "runner",
		"source":     "claude",
		"order_key":  orderKey,
	}
}

// TestTurnActivityCacheHitAvoidsRefold pins the #1077-item-1 contract: an
// unchanged turn serves the SECOND request from cache (freshness probes
// only), and a new event invalidates exactly once.
func TestTurnActivityCacheHitAvoidsRefold(t *testing.T) {
	fake := &cacheFakeEventStore{}
	fake.addEvent("turn_t1", cacheTurnEvent("turn_t1", "001", "turn.started"))
	fake.addEvent("turn_t1", cacheTurnEvent("turn_t1", "002", "turn.completed"))

	cache := newTurnActivityCache()
	ctx := context.Background()

	first, err := cache.projectionFor(ctx, fake, "default", "63", "turn_t1")
	if err != nil {
		t.Fatal(err)
	}
	readsAfterFirst := fake.turnReads

	second, err := cache.projectionFor(ctx, fake, "default", "63", "turn_t1")
	if err != nil {
		t.Fatal(err)
	}
	if fake.turnReads != readsAfterFirst {
		t.Fatalf("cache hit must not re-read the turn: reads went %d → %d", readsAfterFirst, fake.turnReads)
	}
	if fake.maxProbes == 0 {
		t.Fatal("cache hit must verify freshness via MaxOrderKeyForTurn")
	}
	if len(second.Pages) != len(first.Pages) || second.TotalEventCount != first.TotalEventCount {
		t.Fatalf("cached projection diverged: %d/%d pages, %d/%d events",
			len(second.Pages), len(first.Pages), second.TotalEventCount, first.TotalEventCount)
	}

	// A new event moves the turn's high-water mark — the next request refolds.
	fake.addEvent("turn_t1", cacheTurnEvent("turn_t1", "003", "turn.usage"))
	third, err := cache.projectionFor(ctx, fake, "default", "63", "turn_t1")
	if err != nil {
		t.Fatal(err)
	}
	if fake.turnReads == readsAfterFirst {
		t.Fatal("a stale entry must refold")
	}
	if third.TotalEventCount != first.TotalEventCount+1 {
		t.Fatalf("refold missed the new event: %d events", third.TotalEventCount)
	}
}

func TestTurnActivityCacheTracksAskUserQuestionAskingFinalAnswerContext(t *testing.T) {
	fake := &cacheFakeEventStore{}
	fake.addEvent("turn-ask", projectionTestEvent("final", "001", "item.completed", "assistant", "claude", "turn-ask", "turn-ask:item:final", map[string]any{
		"kind": "message",
		"text": "The migration looks ready.",
	}))
	fake.addEvent("turn-question", projectionTestEvent("submit", "002", "turn.submitted", "runner", "tank", "turn-question", "", map[string]any{"status": "submitted"}))
	fake.addEvent("turn-question", projectionTestEvent("await", "003", "turn.awaiting_input", "runner", "claude", "turn-question", "turn-question:item:ask", map[string]any{
		"asking_turn_id":   "turn-ask",
		"question_turn_id": "turn-question",
		"provider_item_id": "toolu_ask",
		"timeline_id":      "turn-question:item:ask",
		"asking_turn_final_answer": map[string]any{
			"timeline_ids": []any{"turn-ask:item:final"},
		},
		"questions": []any{map[string]any{"question": "Proceed?"}},
	}))

	cache := newTurnActivityCache()
	ctx := context.Background()
	first, err := cache.projectionFor(ctx, fake, "default", "63", "turn-question")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Pages) != 1 || len(first.Pages[0].Entries) != 2 {
		t.Fatalf("first projection missing linked final-answer context: %#v", first.Pages)
	}
	readsAfterFirst := fake.turnReads

	if _, err := cache.projectionFor(ctx, fake, "default", "63", "turn-question"); err != nil {
		t.Fatal(err)
	}
	if fake.turnReads != readsAfterFirst {
		t.Fatalf("unchanged question page should hit cache: reads went %d → %d", readsAfterFirst, fake.turnReads)
	}

	fake.addEvent("turn-ask", cacheTurnEvent("turn-ask", "004", "turn.usage"))
	if _, err := cache.projectionFor(ctx, fake, "default", "63", "turn-question"); err != nil {
		t.Fatal(err)
	}
	if fake.turnReads == readsAfterFirst {
		t.Fatal("asking-turn freshness change must invalidate the question-page cache")
	}
}

// TestTurnActivityCacheEvictsByEventBudget pins the LRU memory bound: the
// total cached projected-event count never exceeds the budget (except a
// single oversized entry), and eviction removes least-recently-used turns.
func TestTurnActivityCacheEvictsByEventBudget(t *testing.T) {
	fake := &cacheFakeEventStore{}
	cache := newTurnActivityCache()
	cache.maxEntries = 100
	cache.maxEvents = 5 // tiny budget: each turn below holds 2 events

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		turnID := fmt.Sprintf("turn_t%d", i)
		fake.addEvent(turnID, cacheTurnEvent(turnID, "001", "turn.started"))
		fake.addEvent(turnID, cacheTurnEvent(turnID, "002", "turn.completed"))
		if _, err := cache.projectionFor(ctx, fake, "default", "63", turnID); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.totalEvents > cache.maxEvents {
		t.Fatalf("event budget exceeded: %d > %d", cache.totalEvents, cache.maxEvents)
	}
	if len(cache.entries) == 0 {
		t.Fatal("the most recent entry must survive eviction")
	}
	if _, ok := cache.entries[turnActivityCacheKey("default", "63", "turn_t3")]; !ok {
		t.Fatal("the just-stored entry must never be the eviction victim")
	}
}

// TestTurnActivityCacheKeysByScopeAndSession — two sessions with a same-named
// turn must not share projections.
func TestTurnActivityCacheKeysByScopeAndSession(t *testing.T) {
	a := turnActivityCacheKey("default", "63", "turn_t1")
	b := turnActivityCacheKey("default", "64", "turn_t1")
	c := turnActivityCacheKey("slot-1", "63", "turn_t1")
	if a == b || a == c || b == c {
		t.Fatalf("cache keys collide: %q %q %q", a, b, c)
	}
}
