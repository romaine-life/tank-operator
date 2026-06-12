package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// orderRecordingRowsStore records the relative order of projection writes so
// the refresh-then-wake contract is checkable.
type orderRecordingRowsStore struct {
	recordingTranscriptRowsStore
	mu       sync.Mutex
	replaces int
}

func (s *orderRecordingRowsStore) ReplaceForTurn(ctx context.Context, sessionID string, turnID string, entries []map[string]any) error {
	s.mu.Lock()
	s.replaces++
	s.mu.Unlock()
	return s.recordingTranscriptRowsStore.ReplaceForTurn(ctx, sessionID, turnID, entries)
}

func (s *orderRecordingRowsStore) replaceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaces
}

func asyncRefresherTestEvent(turnID, eventID, orderKey string) map[string]any {
	return projectionTestEvent(eventID, orderKey, "item.completed", "tool", "codex", turnID, turnID+":item:"+eventID, map[string]any{
		"kind":   "command_execution",
		"output": "ok",
	})
}

// TestAsyncTranscriptRefresherWakesAfterRefresh pins the ordering contract
// inherited from the bus persister: the SSE wake fires only after the
// projection refresh for the queued events has completed, so a woken reader
// always sees the refreshed rows.
func TestAsyncTranscriptRefresherWakesAfterRefresh(t *testing.T) {
	events := []map[string]any{asyncRefresherTestEvent("turn-1", "a", "001")}
	rowStore := &orderRecordingRowsStore{}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	var mu sync.Mutex
	var wakes []int // replace-count observed at each wake
	r := newAsyncTranscriptRefresher(context.Background(), materializer, func(string) {
		mu.Lock()
		wakes = append(wakes, rowStore.replaceCount())
		mu.Unlock()
	})

	r.enqueue("63", events[0])

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := len(wakes) >= 1
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wake never fired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if wakes[0] < 1 {
		t.Fatalf("wake fired before any projection write (replaces at wake = %d)", wakes[0])
	}
}

// TestAsyncTranscriptRefresherCoalescesQueuedEvents pins the cost contract:
// events queued for one session while a refresh is in flight are drained as
// one batch — at most one extra projection pass, not one per event.
func TestAsyncTranscriptRefresherCoalescesQueuedEvents(t *testing.T) {
	turnEvents := []map[string]any{
		asyncRefresherTestEvent("turn-1", "a", "001"),
		asyncRefresherTestEvent("turn-1", "b", "002"),
		asyncRefresherTestEvent("turn-1", "c", "003"),
		asyncRefresherTestEvent("turn-1", "d", "004"),
	}
	gate := make(chan struct{})
	rowStore := &orderRecordingRowsStore{}
	eventStore := blockingFirstReadEventStore{
		fakeSessionEventStore: fakeSessionEventStore{
			pages: map[string]store.SessionEventPage{
				"": {Events: turnEvents, FoundOldest: true, FoundNewest: true},
			},
		},
		gate: gate,
	}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	var mu sync.Mutex
	wakeCount := 0
	r := newAsyncTranscriptRefresher(context.Background(), materializer, func(string) {
		mu.Lock()
		wakeCount++
		mu.Unlock()
	})

	// First event starts a worker that blocks inside its ledger read; the
	// next three queue behind it and must drain as ONE batch.
	r.enqueue("63", turnEvents[0])
	eventStore.waitForFirstRead(t)
	r.enqueue("63", turnEvents[1])
	r.enqueue("63", turnEvents[2])
	r.enqueue("63", turnEvents[3])
	close(gate)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := wakeCount >= 2
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 wakes (first event, then coalesced batch); got %d", wakeCount)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give a stray per-event worker a moment to disprove coalescing.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if wakeCount != 2 {
		t.Fatalf("wakes = %d, want exactly 2 (one per drain, not one per event)", wakeCount)
	}
}

// blockingFirstReadEventStore blocks every per-turn ledger read until the
// gate opens, so the coalescing test can stack a queue behind an in-flight
// refresh deterministically.
type blockingFirstReadEventStore struct {
	fakeSessionEventStore
	gate chan struct{}
}

func (s blockingFirstReadEventStore) EventsForTurnAfter(ctx context.Context, sessionID, turnID, afterOrderKey string, limit int) (store.SessionEventPage, error) {
	<-s.gate
	return s.fakeSessionEventStore.EventsForTurnAfter(ctx, sessionID, turnID, afterOrderKey, limit)
}

func (s *blockingFirstReadEventStore) waitForFirstRead(t *testing.T) {
	t.Helper()
	// The worker is in flight once it blocks on the gate; a short settle is
	// sufficient because enqueue spawned it synchronously.
	time.Sleep(20 * time.Millisecond)
}

// TestAsyncTranscriptRefresherWakesOnRefreshFailure pins the degraded-path
// contract: a projection failure still wakes SSE (the woken reader's
// staleness check triggers the on-read resync) and never panics the worker.
func TestAsyncTranscriptRefresherWakesOnRefreshFailure(t *testing.T) {
	rowStore := &failingTranscriptRowsStore{}
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: []map[string]any{asyncRefresherTestEvent("turn-1", "a", "001")}, FoundOldest: true, FoundNewest: true},
		},
	}
	materializer := transcriptRowsMaterializer{events: eventStore, rows: rowStore}

	woke := make(chan struct{}, 1)
	r := newAsyncTranscriptRefresher(context.Background(), materializer, func(string) {
		select {
		case woke <- struct{}{}:
		default:
		}
	})

	r.enqueue("63", asyncRefresherTestEvent("turn-1", "a", "001"))

	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("failure path never woke SSE")
	}
}

type failingTranscriptRowsStore struct {
	recordingTranscriptRowsStore
}

func (s *failingTranscriptRowsStore) ReplaceForTurn(context.Context, string, string, []map[string]any) error {
	return context.DeadlineExceeded
}
