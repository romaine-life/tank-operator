package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// fakeStrandedTurnStore returns canned stranded-turn rows and captures the
// backend-owned turn.command_failed Upserts the sweep emits, plus the window
// arguments the loop computed.
type fakeStrandedTurnStore struct {
	store.StubSessionEventStore
	rows []store.StrandedTurn
	// pipelineQuiet simulates a fleet with zero runner-produced events in
	// the quiet window (persister / session-bus outage). The zero value
	// means "pipeline alive", so the pre-gate tests exercise the normal
	// sweep path unchanged.
	pipelineQuiet    bool
	livenessCalls    int
	gotLivenessSince time.Time
	upserts          []map[string]any
	gotOlder         time.Time
	gotQuiet         time.Time
	gotFloor         time.Time
	gotLimit         int
}

func (f *fakeStrandedTurnStore) FindStrandedTurns(_ context.Context, olderThan, quietSince, notBefore time.Time, limit int) ([]store.StrandedTurn, error) {
	f.gotOlder = olderThan
	f.gotQuiet = quietSince
	f.gotFloor = notBefore
	f.gotLimit = limit
	return f.rows, nil
}

func (f *fakeStrandedTurnStore) HasRecentRunnerEvent(_ context.Context, since time.Time) (bool, error) {
	f.livenessCalls++
	f.gotLivenessSince = since
	return !f.pipelineQuiet, nil
}

func (f *fakeStrandedTurnStore) Upsert(_ context.Context, event map[string]any) (bool, error) {
	f.upserts = append(f.upserts, event)
	return true, nil
}

func strandedTurnSweepReason(t *testing.T, event map[string]any) string {
	t.Helper()
	payload, _ := event["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("swept terminal carries no payload: %#v", event)
	}
	reason, _ := payload["reason"].(string)
	return reason
}

// TestProcessStrandedTurnsFailsNeverClaimedTurn pins the command-lost class:
// a turn durably submitted, never claimed, in a quiet session gets a durable
// turn.command_failed addressed to the same turn id and client nonce.
func TestProcessStrandedTurnsFailsNeverClaimedTurn(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{rows: []store.StrandedTurn{{
		TankSessionID: "816",
		SessionID:     "816",
		TurnID:        "turn_06fa1847",
		ClientNonce:   "06fa1847",
		Email:         "nelson@romaine.life",
		Runtime:       "codex",
		Progressed:    false,
		CreatedAt:     now.Add(-time.Hour),
	}}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if len(fake.upserts) != 1 {
		t.Fatalf("command_failed upserts = %d, want 1", len(fake.upserts))
	}
	ev := fake.upserts[0]
	if got, _ := ev["type"].(string); got != "turn.command_failed" {
		t.Fatalf("event type = %q, want turn.command_failed", got)
	}
	if got, _ := ev["turn_id"].(string); got != "turn_06fa1847" {
		t.Fatalf("turn_id = %q", got)
	}
	if got, _ := ev["client_nonce"].(string); got != "06fa1847" {
		t.Fatalf("client_nonce = %q", got)
	}
	if got, _ := ev["event_id"].(string); got != "turn_06fa1847:turn.command_failed" {
		t.Fatalf("event_id = %q, want deterministic id for replica dedupe", got)
	}
	if reason := strandedTurnSweepReason(t, ev); !strings.HasPrefix(reason, "submit_command_lost") {
		t.Fatalf("reason = %q, want submit_command_lost prefix", reason)
	}
}

// TestProcessStrandedTurnsDefersYoungProgressedTurn pins the two-floor
// contract: a claimed/started turn younger than the progressed floor is left
// alone this tick (it stays a candidate while the session stays silent), and
// one older than the floor is swept with the progress-lost reason.
func TestProcessStrandedTurnsDefersYoungProgressedTurn(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{rows: []store.StrandedTurn{
		{
			TankSessionID: "817", SessionID: "817", TurnID: "turn_young",
			Runtime: "codex", Progressed: true,
			CreatedAt: now.Add(-time.Hour), // > submitted floor, < progressed floor
		},
		{
			TankSessionID: "817", SessionID: "817", TurnID: "turn_old",
			Runtime: "codex", Progressed: true,
			CreatedAt: now.Add(-3 * time.Hour),
		},
	}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if len(fake.upserts) != 1 {
		t.Fatalf("upserts = %d, want only the old progressed turn swept", len(fake.upserts))
	}
	if got, _ := fake.upserts[0]["turn_id"].(string); got != "turn_old" {
		t.Fatalf("swept turn = %q, want turn_old", got)
	}
	if reason := strandedTurnSweepReason(t, fake.upserts[0]); !strings.HasPrefix(reason, "turn_progress_lost") {
		t.Fatalf("reason = %q, want turn_progress_lost prefix", reason)
	}
}

// TestProcessStrandedTurnsClassifiesContinuationsAsAwayErrors pins the
// summon-invariant wiring: a stranded background-wake / scheduled-wakeup /
// agent-continuation turn carries the away-error reason so the sidebar rings
// the bell (the agent promised to resume while the user was away and the
// resume silently died); an ordinary user turn does not.
func TestProcessStrandedTurnsClassifiesContinuationsAsAwayErrors(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		row      store.StrandedTurn
		wantAway bool
	}{
		{
			name: "bgtask wake turn by id",
			row: store.StrandedTurn{
				TankSessionID: "815", SessionID: "815",
				TurnID: "turn_bgtask-bo6om79c2", Runtime: "claude",
				CreatedAt: now.Add(-time.Hour),
			},
			wantAway: true,
		},
		{
			name: "schedule wakeup by source",
			row: store.StrandedTurn{
				TankSessionID: "815", SessionID: "815",
				TurnID: "turn_sched1", Runtime: "claude", Source: "schedule-wakeup",
				CreatedAt: now.Add(-time.Hour),
			},
			wantAway: true,
		},
		{
			name: "ordinary user turn",
			row: store.StrandedTurn{
				TankSessionID: "816", SessionID: "816",
				TurnID: "turn_user1", Runtime: "codex",
				CreatedAt: now.Add(-time.Hour),
			},
			wantAway: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStrandedTurnStore{rows: []store.StrandedTurn{tc.row}}
			app := &appServer{sessionEvents: fake, sessionScope: "default"}
			if err := app.processStrandedTurns(context.Background(), now); err != nil {
				t.Fatalf("processStrandedTurns: %v", err)
			}
			if len(fake.upserts) != 1 {
				t.Fatalf("upserts = %d, want 1", len(fake.upserts))
			}
			reason := strandedTurnSweepReason(t, fake.upserts[0])
			isAway := reason == sessionactivity.AwayErrorReasonStrandedContinuation
			if isAway != tc.wantAway {
				t.Fatalf("reason = %q, away=%v, want away=%v", reason, isAway, tc.wantAway)
			}
		})
	}
}

// TestProcessStrandedTurnsWindowArguments pins the scan bounds: candidates
// are at least the submitted floor old, at most the max age old, and their
// session must be quiet for the whole quiet window.
func TestProcessStrandedTurnsWindowArguments(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if want := now.Add(-strandedTurnMinAgeSubmitted); !fake.gotOlder.Equal(want) {
		t.Fatalf("olderThan = %s, want %s", fake.gotOlder, want)
	}
	if want := now.Add(-strandedTurnQuietWindow); !fake.gotQuiet.Equal(want) {
		t.Fatalf("quietSince = %s, want %s", fake.gotQuiet, want)
	}
	if want := now.Add(-strandedTurnMaxAge); !fake.gotFloor.Equal(want) {
		t.Fatalf("notBefore = %s, want %s", fake.gotFloor, want)
	}
	if fake.gotLimit != strandedTurnBatchLimit {
		t.Fatalf("limit = %d, want %d", fake.gotLimit, strandedTurnBatchLimit)
	}
}

// TestProcessStrandedTurnsSkipsIncompleteRow mirrors the launch sweep: a row
// without an addressable turn/session cannot carry a well-formed terminal.
func TestProcessStrandedTurnsSkipsIncompleteRow(t *testing.T) {
	now := time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{rows: []store.StrandedTurn{{
		TankSessionID: "816",
		Email:         "nelson@romaine.life",
		Runtime:       "codex",
		CreatedAt:     now.Add(-time.Hour),
	}}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0 for incomplete row", len(fake.upserts))
	}
}

// TestProcessStrandedTurnsDefersWhenPipelineQuiet pins the liveness gate:
// candidates found while ZERO runner-produced events landed fleet-wide in
// the quiet window mean the persister/session-bus pipeline itself is
// suspect (turn.submitted lands over HTTP even during an outage, so every
// active session looks quiet) — the sweep must write nothing and leave the
// candidates for a later tick.
func TestProcessStrandedTurnsDefersWhenPipelineQuiet(t *testing.T) {
	now := time.Date(2026, 6, 12, 3, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{
		pipelineQuiet: true,
		rows: []store.StrandedTurn{
			{
				TankSessionID: "901", SessionID: "901", TurnID: "turn_a",
				ClientNonce: "a", Runtime: "claude",
				CreatedAt: now.Add(-time.Hour),
			},
			{
				TankSessionID: "902", SessionID: "902", TurnID: "turn_b",
				ClientNonce: "b", Runtime: "codex", Progressed: true,
				CreatedAt: now.Add(-3 * time.Hour),
			},
		},
	}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0 while the pipeline is quiet", len(fake.upserts))
	}
	if fake.livenessCalls != 1 {
		t.Fatalf("liveness probes = %d, want 1", fake.livenessCalls)
	}
	if want := now.Add(-strandedTurnQuietWindow); !fake.gotLivenessSince.Equal(want) {
		t.Fatalf("liveness since = %s, want quietSince %s", fake.gotLivenessSince, want)
	}
}

// TestProcessStrandedTurnsSkipsLivenessProbeWithoutCandidates pins the cost
// contract: the liveness probe is one extra indexed query per tick that
// found candidates, not per tick.
func TestProcessStrandedTurnsSkipsLivenessProbeWithoutCandidates(t *testing.T) {
	now := time.Date(2026, 6, 12, 3, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if fake.livenessCalls != 0 {
		t.Fatalf("liveness probes = %d, want 0 when no candidates", fake.livenessCalls)
	}
}

// TestProcessStrandedTurnsSweepsWhenPipelineAlive is the positive control
// for the gate: with runner progress in the window, candidates are swept
// exactly as before the gate existed.
func TestProcessStrandedTurnsSweepsWhenPipelineAlive(t *testing.T) {
	now := time.Date(2026, 6, 12, 3, 0, 0, 0, time.UTC)
	fake := &fakeStrandedTurnStore{rows: []store.StrandedTurn{{
		TankSessionID: "903", SessionID: "903", TurnID: "turn_c",
		ClientNonce: "c", Runtime: "claude",
		CreatedAt: now.Add(-time.Hour),
	}}}
	app := &appServer{sessionEvents: fake, sessionScope: "default"}

	if err := app.processStrandedTurns(context.Background(), now); err != nil {
		t.Fatalf("processStrandedTurns: %v", err)
	}
	if fake.livenessCalls != 1 {
		t.Fatalf("liveness probes = %d, want 1", fake.livenessCalls)
	}
	if len(fake.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1 with a live pipeline", len(fake.upserts))
	}
}
