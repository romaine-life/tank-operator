package conversationreadstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type fakeLookup struct {
	sessions map[string]*SessionInfo
	err      error
}

func (f *fakeLookup) LookupSession(_ context.Context, email, scope, sessionID string) (*SessionInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sessions[key(email, scope, sessionID)], nil
}

type fakeReadState struct {
	records map[string]*store.ConversationReadStateRecord
	err     error
}

func (f *fakeReadState) Get(_ context.Context, email, sessionID string) (*store.ConversationReadStateRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.records[email+"\x1f"+sessionID], nil
}

type countingCounter struct {
	stagnant         map[labelPair]int
	skippedActive    map[labelPair]int
	skippedCaughtUp  map[labelPair]int
	skippedMissing   map[string]int
	sampleErrors     map[string]int
}

type labelPair struct{ mode, scope string }

func newCountingCounter() *countingCounter {
	return &countingCounter{
		stagnant:        map[labelPair]int{},
		skippedActive:   map[labelPair]int{},
		skippedCaughtUp: map[labelPair]int{},
		skippedMissing:  map[string]int{},
		sampleErrors:    map[string]int{},
	}
}

func (c *countingCounter) RecordStagnant(mode, scope string) {
	c.stagnant[labelPair{mode, scope}]++
}
func (c *countingCounter) RecordSkippedActiveTurn(mode, scope string) {
	c.skippedActive[labelPair{mode, scope}]++
}
func (c *countingCounter) RecordSkippedIdleCaughtUp(mode, scope string) {
	c.skippedCaughtUp[labelPair{mode, scope}]++
}
func (c *countingCounter) RecordSkippedMissingSession(scope string) {
	c.skippedMissing[scope]++
}
func (c *countingCounter) RecordSampleError(reason string) {
	c.sampleErrors[reason]++
}

func key(email, scope, sessionID string) string {
	return email + "\x1f" + scope + "\x1f" + sessionID
}

// newSampler wires the sampler with stubbed deps + a fresh registry
// and pre-registered streams from the supplied descriptors.
func newSampler(t *testing.T, lookup *fakeLookup, readStates *fakeReadState, counter *countingCounter, streams []streamDescriptor) *Sampler {
	t.Helper()
	reg := sessionstream.NewRegistry()
	for _, d := range streams {
		state := sessionstream.NewStreamState(d.id, d.sessionID, d.storageKey, d.email, time.Now(), "")
		reg.Register(state)
	}
	s := NewSampler(SamplerConfig{
		Registry:   reg,
		Lookup:     lookup,
		ReadStates: func(string) ReadStateLookup { return readStates },
		Counter:    counter,
		LocalScope: "default",
	})
	if s == nil {
		t.Fatal("NewSampler returned nil")
	}
	return s
}

type streamDescriptor struct {
	id, sessionID, storageKey, email string
}

func TestRunOnceCountsStagnantIdleSession(t *testing.T) {
	// The session-269 fixture: status=ready, no active turn, durable
	// tail is past the user_message.created cursor. The user's
	// browser thinks it is not at the live tail (the bug); the
	// durable footprint is "cursor lags."
	lookup := &fakeLookup{sessions: map[string]*SessionInfo{
		key("u@example.com", "default", "269"): {
			Mode:                "claude_gui",
			Status:              "ready",
			ActiveTurnID:        "",
			LastDurableOrderKey: "1779859204107-00000216-turn.completed",
		},
	}}
	readStates := &fakeReadState{records: map[string]*store.ConversationReadStateRecord{
		"u@example.com\x1f269": {
			Email:            "u@example.com",
			SessionID:        "269",
			LastReadOrderKey: "1779859051926-00000014-user_message.created",
		},
	}}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "269", storageKey: "default:269", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.Stagnant != 1 {
		t.Fatalf("Stagnant = %d, want 1", summary.Stagnant)
	}
	if counter.stagnant[labelPair{"claude_gui", "default"}] != 1 {
		t.Fatalf("stagnant counter for (claude_gui, default) = %d, want 1",
			counter.stagnant[labelPair{"claude_gui", "default"}])
	}
}

func TestRunOnceSkipsActiveTurn(t *testing.T) {
	// A turn is streaming; the lag is expected and must not be
	// counted as stagnant. This is the load-bearing predicate that
	// keeps the counter quiet during normal traffic.
	lookup := &fakeLookup{sessions: map[string]*SessionInfo{
		key("u@example.com", "default", "401"): {
			Mode:                "claude_gui",
			Status:              "ready",
			ActiveTurnID:        "turn_abc",
			LastDurableOrderKey: "1779859204107-00000216-item.completed",
		},
	}}
	readStates := &fakeReadState{}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "401", storageKey: "default:401", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.Stagnant != 0 {
		t.Fatalf("Stagnant = %d, want 0", summary.Stagnant)
	}
	if summary.SkippedActiveTurn != 1 {
		t.Fatalf("SkippedActiveTurn = %d, want 1", summary.SkippedActiveTurn)
	}
	if counter.skippedActive[labelPair{"claude_gui", "default"}] != 1 {
		t.Fatalf("skipped-active counter = %d, want 1",
			counter.skippedActive[labelPair{"claude_gui", "default"}])
	}
}

func TestRunOnceSkipsCaughtUpCursor(t *testing.T) {
	// Cursor caught up to the durable tail — no lag, no count.
	lookup := &fakeLookup{sessions: map[string]*SessionInfo{
		key("u@example.com", "default", "402"): {
			Mode:                "codex_gui",
			Status:              "ready",
			ActiveTurnID:        "",
			LastDurableOrderKey: "1779859204107-00000216-turn.completed",
		},
	}}
	readStates := &fakeReadState{records: map[string]*store.ConversationReadStateRecord{
		"u@example.com\x1f402": {
			LastReadOrderKey: "1779859204107-00000216-turn.completed",
		},
	}}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "402", storageKey: "default:402", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.Stagnant != 0 {
		t.Fatalf("Stagnant = %d, want 0", summary.Stagnant)
	}
	if summary.SkippedCaughtUp != 1 {
		t.Fatalf("SkippedCaughtUp = %d, want 1", summary.SkippedCaughtUp)
	}
}

func TestRunOnceCountsMissingReadStateAsStagnant(t *testing.T) {
	// A session with durable events but no read state at all means
	// the user has never marked anything read. The cursor lags by
	// definition; it should be counted.
	lookup := &fakeLookup{sessions: map[string]*SessionInfo{
		key("u@example.com", "default", "403"): {
			Mode:                "claude_gui",
			Status:              "ready",
			ActiveTurnID:        "",
			LastDurableOrderKey: "1779859204107-00000216-turn.completed",
		},
	}}
	readStates := &fakeReadState{}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "403", storageKey: "default:403", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.Stagnant != 1 {
		t.Fatalf("Stagnant = %d, want 1", summary.Stagnant)
	}
}

func TestRunOnceSkipsSessionRowMissing(t *testing.T) {
	// Stream is open but the session row is gone — soft-deleted or
	// scope mismatch. Counts as a skipped-missing observation, not
	// as a stagnant session.
	lookup := &fakeLookup{sessions: map[string]*SessionInfo{}}
	readStates := &fakeReadState{}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "404", storageKey: "default:404", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.Stagnant != 0 {
		t.Fatalf("Stagnant = %d, want 0", summary.Stagnant)
	}
	if summary.SkippedMissingRow != 1 {
		t.Fatalf("SkippedMissingRow = %d, want 1", summary.SkippedMissingRow)
	}
	if counter.skippedMissing["default"] != 1 {
		t.Fatalf("skipped-missing counter = %d, want 1", counter.skippedMissing["default"])
	}
}

func TestRunOnceLogsSessionLookupErrors(t *testing.T) {
	// A lookup error must not stop the loop; it must count and
	// continue to the next stream.
	lookup := &fakeLookup{err: errors.New("db down")}
	readStates := &fakeReadState{}
	counter := newCountingCounter()
	s := newSampler(t, lookup, readStates, counter, []streamDescriptor{
		{id: "s1", sessionID: "405", storageKey: "default:405", email: "u@example.com"},
		{id: "s2", sessionID: "406", storageKey: "default:406", email: "u@example.com"},
	})

	summary := s.RunOnce(context.Background(), time.Now())

	if summary.SkippedSessionErr != 2 {
		t.Fatalf("SkippedSessionErr = %d, want 2", summary.SkippedSessionErr)
	}
	if counter.sampleErrors["session_lookup"] != 2 {
		t.Fatalf("session_lookup error counter = %d, want 2",
			counter.sampleErrors["session_lookup"])
	}
}

func TestDecodeStorageKeyFallsBackToLocalScope(t *testing.T) {
	scope, id := decodeStorageKey("", "default", "203")
	if scope != "default" || id != "203" {
		t.Fatalf("empty storage_key fallback: scope=%q id=%q", scope, id)
	}

	scope, id = decodeStorageKey("slot-a:204", "default", "204")
	if scope != "slot-a" || id != "204" {
		t.Fatalf("scope-prefixed: scope=%q id=%q", scope, id)
	}

	scope, id = decodeStorageKey("malformed", "default", "207")
	if scope != "default" || id != "207" {
		t.Fatalf("malformed key fallback: scope=%q id=%q", scope, id)
	}
}

func TestCursorLagsHandlesEmptyDurable(t *testing.T) {
	// A session with no durable events is not lagging — there's
	// nothing to be behind. Treat empty durable as "skip."
	if cursorLags("", &store.ConversationReadStateRecord{LastReadOrderKey: "x"}) {
		t.Fatal("empty durable should not count as lagging")
	}
}

func TestIsDurablyIdleSwitchesOnStatus(t *testing.T) {
	idle := &SessionInfo{Status: "ready", ActiveTurnID: ""}
	if !isDurablyIdle(idle) {
		t.Fatal("ready + no active turn must be idle")
	}
	active := &SessionInfo{Status: "ready", ActiveTurnID: "turn_abc"}
	if isDurablyIdle(active) {
		t.Fatal("active turn id must NOT be idle even when status=ready")
	}
	streaming := &SessionInfo{Status: "streaming", ActiveTurnID: ""}
	if isDurablyIdle(streaming) {
		t.Fatal("status=streaming must NOT be idle")
	}
}
