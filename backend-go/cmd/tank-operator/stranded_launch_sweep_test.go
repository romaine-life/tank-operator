package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// fakeStrandedLaunchStore returns canned stranded-launch rows and captures
// the backend-owned turn.command_failed Upserts the sweep emits, plus the
// age-window arguments the loop computed.
type fakeStrandedLaunchStore struct {
	store.StubSessionEventStore
	rows      []store.StrandedLaunchTurn
	upserts   []map[string]any
	findCalls int
	gotOlder  time.Time
	gotBefore time.Time
	gotLimit  int
}

func (f *fakeStrandedLaunchStore) FindStrandedLaunchTurns(_ context.Context, olderThan, notBefore time.Time, limit int) ([]store.StrandedLaunchTurn, error) {
	f.findCalls++
	f.gotOlder = olderThan
	f.gotBefore = notBefore
	f.gotLimit = limit
	return f.rows, nil
}

func (f *fakeStrandedLaunchStore) Upsert(_ context.Context, event map[string]any) error {
	f.upserts = append(f.upserts, event)
	return nil
}

func TestProcessStrandedLaunchesMarksUndispatchedLaunchFailed(t *testing.T) {
	fake := &fakeStrandedLaunchStore{rows: []store.StrandedLaunchTurn{{
		TankSessionID: "523",
		SessionID:     "523",
		TurnID:        "turn_86feb9e4",
		ClientNonce:   "86feb9e4",
		Email:         "nelson@romaine.life",
		Runtime:       "claude",
		CreatedAt:     time.Date(2026, 6, 3, 19, 28, 42, 0, time.UTC),
	}}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	now := time.Date(2026, 6, 3, 20, 0, 0, 0, time.UTC)
	if err := app.processStrandedLaunches(context.Background(), now); err != nil {
		t.Fatalf("processStrandedLaunches returned error: %v", err)
	}

	if len(fake.upserts) != 1 {
		t.Fatalf("command_failed upserts = %d, want 1", len(fake.upserts))
	}
	ev := fake.upserts[0]
	if got, _ := ev["type"].(string); got != "turn.command_failed" {
		t.Fatalf("event type = %q, want turn.command_failed", got)
	}
	if got, _ := ev["turn_id"].(string); got != "turn_86feb9e4" {
		t.Fatalf("turn_id = %q, want turn_86feb9e4", got)
	}
	if got, _ := ev["client_nonce"].(string); got != "86feb9e4" {
		t.Fatalf("client_nonce = %q, want 86feb9e4", got)
	}
	if got, _ := ev["tank_session_id"].(string); got != "523" {
		t.Fatalf("tank_session_id = %q, want 523", got)
	}
	if got, _ := ev["actor"].(string); got != "system" {
		t.Fatalf("actor = %q, want system", got)
	}
	if got, _ := ev["source"].(string); got != "tank" {
		t.Fatalf("source = %q, want tank", got)
	}
	if got, _ := ev["event_id"].(string); got != "turn_86feb9e4:turn.command_failed" {
		t.Fatalf("event_id = %q, want deterministic turn_86feb9e4:turn.command_failed", got)
	}
	payload, _ := ev["payload"].(map[string]any)
	if reason, _ := payload["reason"].(string); !strings.HasPrefix(reason, "launch_never_dispatched") {
		t.Fatalf("reason = %q, want launch_never_dispatched prefix", reason)
	}
}

func TestProcessStrandedLaunchesUsesGenerousAgeWindow(t *testing.T) {
	fake := &fakeStrandedLaunchStore{}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	now := time.Date(2026, 6, 3, 20, 0, 0, 0, time.UTC)
	if err := app.processStrandedLaunches(context.Background(), now); err != nil {
		t.Fatalf("processStrandedLaunches returned error: %v", err)
	}
	if fake.findCalls != 1 {
		t.Fatalf("FindStrandedLaunchTurns calls = %d, want 1", fake.findCalls)
	}
	if want := now.Add(-strandedLaunchMinAge); !fake.gotOlder.Equal(want) {
		t.Fatalf("olderThan = %s, want %s", fake.gotOlder, want)
	}
	if want := now.Add(-strandedLaunchMaxAge); !fake.gotBefore.Equal(want) {
		t.Fatalf("notBefore = %s, want %s", fake.gotBefore, want)
	}
	if fake.gotLimit != strandedLaunchBatchLimit {
		t.Fatalf("limit = %d, want %d", fake.gotLimit, strandedLaunchBatchLimit)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("upserts with no rows = %d, want 0", len(fake.upserts))
	}
}

func TestProcessStrandedLaunchesSkipsIncompleteRow(t *testing.T) {
	fake := &fakeStrandedLaunchStore{rows: []store.StrandedLaunchTurn{{
		// No SessionID / TurnID — cannot address a well-formed terminal.
		TankSessionID: "523",
		Email:         "nelson@romaine.life",
		Runtime:       "claude",
	}}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedLaunches(context.Background(), time.Date(2026, 6, 3, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("processStrandedLaunches returned error: %v", err)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("upserts for incomplete row = %d, want 0", len(fake.upserts))
	}
}
