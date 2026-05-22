package sessionstream

import (
	"sync"
	"testing"
	"time"
)

func TestRegisterDeregisterBalance(t *testing.T) {
	reg := NewRegistry()
	now := time.Now()

	a := NewStreamState("stream-a", "42", "42", "alice@example.com", now, "")
	b := NewStreamState("stream-b", "42", "42", "bob@example.com", now, "100")
	reg.Register(a)
	reg.Register(b)
	if got := reg.Len(); got != 2 {
		t.Fatalf("Len after Register x2 = %d, want 2", got)
	}
	reg.Deregister("stream-a")
	if got := reg.Len(); got != 1 {
		t.Fatalf("Len after one Deregister = %d, want 1", got)
	}
	reg.Deregister("missing")
	if got := reg.Len(); got != 1 {
		t.Fatalf("Deregister of unknown stream changed Len: %d", got)
	}
	reg.Deregister("stream-b")
	if got := reg.Len(); got != 0 {
		t.Fatalf("Len after final Deregister = %d, want 0", got)
	}
}

func TestSnapshotCarriesIdentityAndCounters(t *testing.T) {
	reg := NewRegistry()
	opened := time.Now().Add(-3 * time.Second)
	s := NewStreamState("stream-x", "160", "160", "user@example.com", opened, "")
	reg.Register(s)

	wakeAt := opened.Add(1 * time.Second)
	s.RecordWake(wakeAt, "tank.live.160.wake")
	s.RecordPageRead(wakeAt.Add(1*time.Millisecond), 2)
	s.RecordEmit(wakeAt.Add(2*time.Millisecond), "abc123", "user_message.created", "abc123")
	s.RecordEmit(wakeAt.Add(3*time.Millisecond), "abc124", "turn.submitted", "abc124")
	s.RecordHeartbeat(wakeAt.Add(15 * time.Second))

	now := opened.Add(20 * time.Second)
	snap := reg.Snapshot(now)
	if len(snap) != 1 {
		t.Fatalf("len snap = %d, want 1", len(snap))
	}
	got := snap[0]
	if got.StreamID != "stream-x" || got.SessionID != "160" || got.StorageKey != "160" || got.Email != "user@example.com" {
		t.Fatalf("identity drift: %+v", got)
	}
	if got.WakesReceived != 1 {
		t.Fatalf("WakesReceived = %d, want 1", got.WakesReceived)
	}
	if got.PagesReadNonEmpty != 1 || got.PagesReadEmpty != 0 {
		t.Fatalf("PagesRead split = (empty=%d, non=%d), want (0, 1)", got.PagesReadEmpty, got.PagesReadNonEmpty)
	}
	if got.EmitsTotal != 2 {
		t.Fatalf("EmitsTotal = %d, want 2", got.EmitsTotal)
	}
	if got.HeartbeatsSent != 1 {
		t.Fatalf("HeartbeatsSent = %d, want 1", got.HeartbeatsSent)
	}
	if got.LastEmitOrderKey != "abc124" || got.LastEmitEventType != "turn.submitted" {
		t.Fatalf("LastEmit drift: order_key=%q type=%q", got.LastEmitOrderKey, got.LastEmitEventType)
	}
	if got.CursorAfterOrderKey != "abc124" {
		t.Fatalf("CursorAfterOrderKey = %q, want abc124", got.CursorAfterOrderKey)
	}
	if got.LastWakeSubject != "tank.live.160.wake" {
		t.Fatalf("LastWakeSubject = %q", got.LastWakeSubject)
	}
	if got.OpenSeconds < 20 || got.OpenSeconds > 21 {
		t.Fatalf("OpenSeconds = %v, want ~20", got.OpenSeconds)
	}
	if got.OpenedAt == "" {
		t.Fatalf("OpenedAt should be formatted, got empty string")
	}
}

// TestEmptyPageReadDistinct ensures pages_read_empty vs pages_read_non_empty
// correctly distinguishes the candidate-B signature: wake fires but the
// read returns no rows. That divergence (wakes_received > 0,
// pages_read_non_empty == 0, pages_read_empty > 0) is the smoking gun.
func TestEmptyPageReadDistinct(t *testing.T) {
	s := NewStreamState("stream-y", "100", "100", "u@e.com", time.Now(), "")
	now := time.Now()
	s.RecordPageRead(now, 0)
	s.RecordPageRead(now.Add(time.Millisecond), 0)
	s.RecordPageRead(now.Add(2*time.Millisecond), 5)
	snap := s.Snapshot(now.Add(time.Second))
	if snap.PagesReadEmpty != 2 {
		t.Fatalf("PagesReadEmpty = %d, want 2", snap.PagesReadEmpty)
	}
	if snap.PagesReadNonEmpty != 1 {
		t.Fatalf("PagesReadNonEmpty = %d, want 1", snap.PagesReadNonEmpty)
	}
	if snap.LastPageEmitCount != 5 {
		t.Fatalf("LastPageEmitCount = %d, want 5 (most recent)", snap.LastPageEmitCount)
	}
}

func TestSnapshotSortStableByOpenedAt(t *testing.T) {
	reg := NewRegistry()
	base := time.Now()
	// Insert out of order; expect snapshots sorted ascending by OpenedAt.
	reg.Register(NewStreamState("c", "3", "3", "c@e.com", base.Add(2*time.Second), ""))
	reg.Register(NewStreamState("a", "1", "1", "a@e.com", base, ""))
	reg.Register(NewStreamState("b", "2", "2", "b@e.com", base.Add(1*time.Second), ""))
	snaps := reg.Snapshot(base.Add(10 * time.Second))
	if len(snaps) != 3 {
		t.Fatalf("snap len = %d", len(snaps))
	}
	if snaps[0].StreamID != "a" || snaps[1].StreamID != "b" || snaps[2].StreamID != "c" {
		t.Fatalf("snapshot order = %s/%s/%s, want a/b/c", snaps[0].StreamID, snaps[1].StreamID, snaps[2].StreamID)
	}
}

func TestNilReceiversAreNoOps(t *testing.T) {
	// Guards: SSE handler defers RecordWake / Deregister; nil-safety
	// keeps a stub-mode deployment (no NATS, no registry) from
	// panicking on first wake.
	var r *Registry
	r.Register(NewStreamState("x", "1", "1", "u@e.com", time.Now(), ""))
	r.Deregister("x")
	if r.Len() != 0 {
		t.Fatalf("nil registry should report Len() == 0")
	}
	if got := r.Snapshot(time.Now()); got != nil {
		t.Fatalf("nil registry Snapshot() should be nil")
	}

	var s *StreamState
	s.RecordWake(time.Now(), "x")
	s.RecordPageRead(time.Now(), 1)
	s.RecordEmit(time.Now(), "1", "session.status", "1")
	s.RecordHeartbeat(time.Now())
	if got := (s.Snapshot(time.Now())); got != (Snapshot{}) {
		t.Fatalf("nil state Snapshot() should be zero value, got %+v", got)
	}
}

// TestConcurrentRecordAndSnapshot exercises the locking around
// per-stream state updates. RecordWake / RecordEmit run from the
// stream's own goroutine, but Snapshot reads from the admin
// endpoint goroutine. The race detector should stay quiet.
func TestConcurrentRecordAndSnapshot(t *testing.T) {
	s := NewStreamState("stream-z", "9", "9", "u@e.com", time.Now(), "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.RecordWake(time.Now(), "tank.live.9.wake")
			s.RecordEmit(time.Now(), "k", "user_message.created", "k")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = s.Snapshot(time.Now())
		}
	}()
	wg.Wait()
	snap := s.Snapshot(time.Now())
	if snap.WakesReceived != 200 || snap.EmitsTotal != 200 {
		t.Fatalf("counter loss under concurrent updates: wakes=%d emits=%d", snap.WakesReceived, snap.EmitsTotal)
	}
}
