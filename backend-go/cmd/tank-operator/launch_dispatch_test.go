package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

func TestComposeLaunchDispatchPrompt(t *testing.T) {
	cases := []struct {
		name      string
		runtime   string
		skill     string
		base      string
		paths     []string
		want      string
		skillTurn bool
	}{
		{
			name: "claude skill with attachments", runtime: "claude", skill: "test", base: "do it",
			paths:     []string{"/workspace/.attachments/turn_x-0-a.zip", "/workspace/.attachments/turn_x-1-b.png"},
			want:      "/test\n\ndo it\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.zip\n- /workspace/.attachments/turn_x-1-b.png",
			skillTurn: true,
		},
		{
			name: "codex skill with attachments", runtime: "codex", skill: "test", base: "do it",
			paths:     []string{"/workspace/.attachments/turn_x-0-a.zip"},
			want:      "$test\n\ndo it\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.zip",
			skillTurn: true,
		},
		{
			name: "antigravity skill with attachments", runtime: "antigravity", skill: "test", base: "do it",
			paths:     []string{"/workspace/.attachments/turn_x-0-a.zip"},
			want:      "$test\n\ndo it\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.zip",
			skillTurn: true,
		},
		{
			name: "codex skill already triggered", runtime: "codex", skill: "test", base: "$test\n\ndo it",
			paths:     []string{"/workspace/.attachments/turn_x-0-a.zip"},
			want:      "$test\n\ndo it\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.zip",
			skillTurn: true,
		},
		{
			name: "no skill with attachments", runtime: "codex", skill: "", base: "compare",
			paths: []string{"/workspace/.attachments/turn_x-0-a.png"},
			want:  "compare\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.png",
		},
		{
			name: "skill no attachments", runtime: "claude", skill: "test", base: "go",
			want: "/test\n\ngo", skillTurn: true,
		},
		{
			name: "skill empty base with attachments", runtime: "claude", skill: "test", base: "",
			paths:     []string{"/workspace/.attachments/turn_x-0-a.png"},
			want:      "/test\n\nAttachments:\n- /workspace/.attachments/turn_x-0-a.png",
			skillTurn: true,
		},
		{
			name: "skill empty base no attachments", runtime: "claude", skill: "test", base: "",
			want: "/test", skillTurn: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composeLaunchDispatchPrompt(tc.runtime, tc.skill, tc.base, tc.paths)
			if got != tc.want {
				t.Fatalf("prompt =\n%q\nwant\n%q", got, tc.want)
			}
			// Whatever we compose for a skill launch must satisfy the gate
			// enqueueSDKTurn enforces, or the dispatch would 400.
			if tc.skillTurn && !promptMatchesSkillTrigger(tc.runtime, tc.skill, got) {
				t.Fatalf("composed prompt does not match skill trigger: %q", got)
			}
		})
	}
}

func TestProcessPendingLaunchesFailsAtAttemptCap(t *testing.T) {
	// No pods registered, so GetByOwner fails (session unresolvable) — a
	// transient error. With AttemptCount at the cap, the reconciler must fail
	// the launch durably and emit turn.command_failed rather than retry forever.
	app := testTurnsApp(t, &recordingSessionBus{})
	app.sessionEvents = &recordingSessionEventStore{}
	fake := &fakePendingLaunchStore{claimRows: []pgstore.PendingLaunchTurn{{
		TankSessionID: "523", SessionID: "523", TurnID: "turn_x", ClientNonce: "x",
		OwnerEmail: "user@example.com", Runtime: "claude", AttemptCount: maxLaunchDispatchAttempts,
	}}}
	app.pendingLaunch = fake

	if err := app.processPendingLaunches(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processPendingLaunches: %v", err)
	}
	if fake.failedTurn != "turn_x" {
		t.Fatalf("failed turn = %q, want turn_x", fake.failedTurn)
	}
	if !strings.HasPrefix(fake.failReason, "launch_dispatch_failed") {
		t.Fatalf("fail reason = %q, want launch_dispatch_failed prefix", fake.failReason)
	}
	upserts := app.sessionEvents.(*recordingSessionEventStore).upserts
	var sawCommandFailed bool
	for _, ev := range upserts {
		if ev["type"] == "turn.command_failed" && ev["turn_id"] == "turn_x" {
			sawCommandFailed = true
		}
	}
	if !sawCommandFailed {
		t.Fatalf("no turn.command_failed emitted for the stranded launch; upserts=%v", upserts)
	}
}

func TestProcessPendingLaunchesFailsSkillLaunchWithoutRuntime(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", "codex_gui", "codex-runner"))
	app.sessionEvents = &recordingSessionEventStore{}
	fake := &fakePendingLaunchStore{claimRows: []pgstore.PendingLaunchTurn{{
		TankSessionID: "64",
		SessionID:     "64",
		TurnID:        "turn_missing_runtime",
		ClientNonce:   "missing-runtime",
		OwnerEmail:    "user@example.com",
		SkillName:     "test",
		BasePrompt:    "$test\n\nrun it",
	}}}
	app.pendingLaunch = fake

	if err := app.processPendingLaunches(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processPendingLaunches: %v", err)
	}
	if fake.failedTurn != "turn_missing_runtime" {
		t.Fatalf("failed turn = %q, want turn_missing_runtime", fake.failedTurn)
	}
	if !strings.Contains(fake.failReason, "skill launch runtime is invalid") {
		t.Fatalf("fail reason = %q, want invalid runtime", fake.failReason)
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
	upserts := app.sessionEvents.(*recordingSessionEventStore).upserts
	var sawCommandFailed bool
	for _, ev := range upserts {
		if ev["type"] == "turn.command_failed" && ev["turn_id"] == "turn_missing_runtime" {
			sawCommandFailed = true
		}
	}
	if !sawCommandFailed {
		t.Fatalf("no turn.command_failed emitted for invalid launch runtime; upserts=%v", upserts)
	}
}

func TestProcessStaleLaunchesFailsStuckLaunches(t *testing.T) {
	// A launch still awaiting_bytes long past the deadline (the browser never
	// finished staging) must be failed durably with turn.command_failed so the
	// row self-cleans and the SPA stops showing it pending.
	app := testTurnsApp(t, &recordingSessionBus{})
	app.sessionEvents = &recordingSessionEventStore{}
	fake := &fakePendingLaunchStore{staleRows: []pgstore.PendingLaunchTurn{{
		TankSessionID: "523", SessionID: "523", TurnID: "turn_stale", ClientNonce: "stale",
		OwnerEmail: "user@example.com", Runtime: "claude", Status: pgstore.PendingLaunchAwaitingBytes,
	}}}
	app.pendingLaunch = fake

	if err := app.processStaleLaunches(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processStaleLaunches: %v", err)
	}
	if fake.failedTurn != "turn_stale" {
		t.Fatalf("failed turn = %q, want turn_stale", fake.failedTurn)
	}
	if !strings.HasPrefix(fake.failReason, "launch_never_completed") {
		t.Fatalf("fail reason = %q, want launch_never_completed prefix", fake.failReason)
	}
	upserts := app.sessionEvents.(*recordingSessionEventStore).upserts
	var sawCommandFailed bool
	for _, ev := range upserts {
		if ev["type"] == "turn.command_failed" && ev["turn_id"] == "turn_stale" {
			sawCommandFailed = true
		}
	}
	if !sawCommandFailed {
		t.Fatalf("no turn.command_failed emitted for the stale launch; upserts=%v", upserts)
	}
}

// TestProcessPendingLaunchesSkipsAlreadyTerminalTurn pins the dispatch half of
// the #1079 item 3 deferred-launch race: a claimed launch whose turn already
// carries a durable terminal (a pre-guard sweep terminal, a sibling replica's
// stale-launch fail, a prior publish whose MarkDispatched write was lost and
// whose turn already finished) must NOT be dispatched — publishing would
// append turn.submitted after the terminal and wedge the session durably
// 'submitted'. The reconciler must fail the pending row with the bounded
// terminal_already_present reason, count the skip, and emit NO durable event:
// the turn already has its terminal, and writing a second one is exactly the
// false-terminal class the guard exists to prevent.
func TestProcessPendingLaunchesSkipsAlreadyTerminalTurn(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", "claude_gui", "claude-runner"))
	events := &recordingSessionEventStore{terminalTurns: map[string]map[string]any{
		"turn_already_done": {"type": "turn.command_failed"},
	}}
	app.sessionEvents = events
	fake := &fakePendingLaunchStore{claimRows: []pgstore.PendingLaunchTurn{{
		TankSessionID: "64", SessionID: "64", TurnID: "turn_already_done", ClientNonce: "alreadydone",
		OwnerEmail: "user@example.com", Runtime: "claude", BasePrompt: "go",
		Status: pgstore.PendingLaunchClaiming, AttemptCount: 1,
	}}}
	app.pendingLaunch = fake

	before := testutil.ToFloat64(launchDispatchTotal.WithLabelValues("skipped_already_terminal"))
	if err := app.processPendingLaunches(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processPendingLaunches: %v", err)
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published %d commands for an already-terminal launch turn, want 0", len(bus.commands))
	}
	if fake.failedTurn != "turn_already_done" {
		t.Fatalf("failed turn = %q, want turn_already_done (pending row must leave the live statuses)", fake.failedTurn)
	}
	if !strings.HasPrefix(fake.failReason, "terminal_already_present") {
		t.Fatalf("fail reason = %q, want terminal_already_present prefix", fake.failReason)
	}
	if n := len(events.upserts); n != 0 {
		t.Fatalf("emitted %d durable events, want 0 — the turn already has its terminal", n)
	}
	if after := testutil.ToFloat64(launchDispatchTotal.WithLabelValues("skipped_already_terminal")); after != before+1 {
		t.Fatalf("skipped_already_terminal count = %v, want %v", after, before+1)
	}
}

func TestProcessPendingLaunchesRetriesBelowCap(t *testing.T) {
	// Same unresolvable session, but below the attempt cap: the reconciler
	// must leave it for retry (no durable fail, no command_failed).
	app := testTurnsApp(t, &recordingSessionBus{})
	app.sessionEvents = &recordingSessionEventStore{}
	fake := &fakePendingLaunchStore{claimRows: []pgstore.PendingLaunchTurn{{
		TankSessionID: "523", SessionID: "523", TurnID: "turn_x", ClientNonce: "x",
		OwnerEmail: "user@example.com", Runtime: "claude", AttemptCount: 1,
	}}}
	app.pendingLaunch = fake

	if err := app.processPendingLaunches(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processPendingLaunches: %v", err)
	}
	if fake.failedTurn != "" {
		t.Fatalf("launch failed below attempt cap (turn %q); want retry", fake.failedTurn)
	}
	if n := len(app.sessionEvents.(*recordingSessionEventStore).upserts); n != 0 {
		t.Fatalf("emitted %d events on a retryable failure, want 0", n)
	}
}
