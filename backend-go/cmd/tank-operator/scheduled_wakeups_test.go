package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

type fakeScheduledWakeupStore struct {
	rows             []pgstore.ScheduledWakeup
	exceededRows     []pgstore.ScheduledWakeup
	failExceededErr  error
	failExceededCall int
	registerRow      pgstore.ScheduledWakeup
	firedID          string
	firedTurn        string
	failedID         string
	failReason       string
	releasedID       string
	releaseCalls     int
	cancelCalls      int
	cancelSessionID  string
	cancelReturn     int64
}

func (f *fakeScheduledWakeupStore) Register(_ context.Context, req pgstore.RegisterScheduledWakeupRequest) (pgstore.ScheduledWakeup, error) {
	if f.registerRow.WakeupID != "" {
		return f.registerRow, nil
	}
	return pgstore.ScheduledWakeup{
		WakeupID:          "wakeup_registered",
		SessionScope:      req.SessionScope,
		SessionID:         req.SessionID,
		TankSessionID:     sessionmodel.SessionStorageKey(req.SessionScope, req.SessionID),
		OwnerEmail:        req.OwnerEmail,
		Provider:          req.Provider,
		Prompt:            req.Prompt,
		ClientNonce:       "schedule_wakeup-wakeup_registered",
		ScheduledTurnID:   req.ScheduledTurnID,
		ProviderItemID:    req.ProviderItemID,
		ScheduledAt:       req.ScheduledAt,
		DueAt:             req.DueAt,
		Status:            pgstore.ScheduledWakeupScheduled,
		SessionStatus:     "Active",
		SessionTerminated: false,
	}, nil
}

func (f *fakeScheduledWakeupStore) ClaimDue(context.Context, time.Time, int, time.Duration) ([]pgstore.ScheduledWakeup, error) {
	return f.rows, nil
}

// FailExceeded returns the seeded capped-out rows as already-terminaled
// 'failed' snapshots, mirroring the store's SQL terminal.
func (f *fakeScheduledWakeupStore) FailExceeded(context.Context, time.Time, int, time.Duration) ([]pgstore.ScheduledWakeup, error) {
	f.failExceededCall++
	if f.failExceededErr != nil {
		return nil, f.failExceededErr
	}
	out := make([]pgstore.ScheduledWakeup, 0, len(f.exceededRows))
	for _, row := range f.exceededRows {
		row.Status = pgstore.ScheduledWakeupFailed
		row.LastError = "attempt_cap_exceeded: gave up after " + strconv.Itoa(row.AttemptCount) + " attempts"
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeScheduledWakeupStore) ListBySession(context.Context, string, string) ([]pgstore.ScheduledWakeup, error) {
	return f.rows, nil
}

func (f *fakeScheduledWakeupStore) MarkFired(_ context.Context, wakeupID, turnID string) (pgstore.ScheduledWakeup, error) {
	f.firedID = wakeupID
	f.firedTurn = turnID
	for _, row := range f.rows {
		if row.WakeupID == wakeupID {
			row.Status = pgstore.ScheduledWakeupFired
			row.FiredTurnID = turnID
			return row, nil
		}
	}
	return pgstore.ScheduledWakeup{WakeupID: wakeupID, Status: pgstore.ScheduledWakeupFired, FiredTurnID: turnID}, nil
}

func (f *fakeScheduledWakeupStore) MarkFailed(_ context.Context, wakeupID, reason string) (pgstore.ScheduledWakeup, error) {
	f.failedID = wakeupID
	f.failReason = reason
	for _, row := range f.rows {
		if row.WakeupID == wakeupID {
			row.Status = pgstore.ScheduledWakeupFailed
			row.LastError = reason
			return row, nil
		}
	}
	return pgstore.ScheduledWakeup{WakeupID: wakeupID, Status: pgstore.ScheduledWakeupFailed, LastError: reason}, nil
}

func (f *fakeScheduledWakeupStore) ReleaseRetainingAttempt(_ context.Context, wakeupID string) error {
	f.releasedID = wakeupID
	f.releaseCalls++
	return nil
}

func (f *fakeScheduledWakeupStore) ScheduledDueCount(context.Context, time.Time) (int, error) {
	return len(f.rows), nil
}

func (f *fakeScheduledWakeupStore) CancelPendingForSession(_ context.Context, _, sessionID string) ([]pgstore.ScheduledWakeup, error) {
	f.cancelCalls++
	f.cancelSessionID = sessionID
	rows := make([]pgstore.ScheduledWakeup, 0, f.cancelReturn)
	for i := int64(0); i < f.cancelReturn; i++ {
		rows = append(rows, pgstore.ScheduledWakeup{
			WakeupID:      "wakeup_cancelled",
			SessionScope:  "default",
			SessionID:     sessionID,
			TankSessionID: sessionmodel.SessionStorageKey("default", sessionID),
			ClientNonce:   "schedule_wakeup-wakeup_cancelled",
			Prompt:        "cancelled wake",
			ScheduledAt:   time.Date(2026, 6, 3, 15, 20, 0, 0, time.UTC),
			DueAt:         time.Date(2026, 6, 3, 15, 25, 0, 0, time.UTC),
			Status:        pgstore.ScheduledWakeupCancelled,
		})
	}
	return rows, nil
}

// TestCancelPendingWakesForSession pins the cancel fan-out used by both the
// explicit cancel control and the prompt-mid-sleep take-over: it cancels pending
// scheduled-wakeup and background-task wakes for the session and sums the count.
func TestCancelPendingWakesForSession(t *testing.T) {
	sched := &fakeScheduledWakeupStore{cancelReturn: 1}
	bg := &fakeBackgroundTaskWakeStore{cancelReturn: 2}
	app := &appServer{scheduledWakeups: sched, backgroundTaskWakes: bg, sessionScope: "default"}

	total := app.cancelPendingWakesForSession(context.Background(), "63")

	if total != 3 {
		t.Fatalf("total cancelled = %d, want 3", total)
	}
	if sched.cancelCalls != 1 || sched.cancelSessionID != "63" {
		t.Fatalf("scheduled cancel = calls %d session %q, want 1 / 63", sched.cancelCalls, sched.cancelSessionID)
	}
	if bg.cancelCalls != 1 || bg.cancelSessionID != "63" {
		t.Fatalf("background cancel = calls %d session %q, want 1 / 63", bg.cancelCalls, bg.cancelSessionID)
	}
}

// TestSupportsScheduledWakeupsRejectsAntigravity pins the long-running-agent
// harness contract on the orchestrator: only Claude is fired by the scheduled-wakeup
// loop. Antigravity self-continues natively (agy fires its own timer/task and emits
// the continuation), so Tank must NOT own a clock for it — that double-wakes a
// self-managing agent and is the trap that cost ~20 prior attempts. The runner
// relays agy's self-continuation through /agent-continuation instead. See
// backend-go/cmd/antigravity-runner/ARCHITECTURE.md.
func TestSupportsScheduledWakeupsRejectsAntigravity(t *testing.T) {
	if !supportsScheduledWakeups("claude") {
		t.Fatal("supportsScheduledWakeups(claude) = false, want true")
	}
	for _, provider := range []string{"antigravity", "codex", ""} {
		if supportsScheduledWakeups(provider) {
			t.Fatalf("supportsScheduledWakeups(%q) = true, want false (only claude is fired by Tank)", provider)
		}
	}
}

func TestFireScheduledWakeupUsesDurableTurnBoundary(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "claude-runner"))
	schedules := &fakeScheduledWakeupStore{}
	app.scheduledWakeups = schedules
	app.sessionEvents = &recordingSessionEventStore{}
	row := pgstore.ScheduledWakeup{
		WakeupID:          "wakeup_123",
		SessionScope:      "default",
		SessionID:         "63",
		TankSessionID:     "63",
		OwnerEmail:        "user@example.com",
		Provider:          "claude",
		Prompt:            "check the deploy",
		ClientNonce:       "schedule_wakeup-wakeup_123",
		ProviderItemID:    "toolu_wake",
		ScheduledAt:       time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
		DueAt:             time.Date(2026, 6, 3, 0, 5, 0, 0, time.UTC),
		SessionStatus:     "Active",
		SessionTerminated: false,
	}

	if err := app.fireScheduledWakeup(context.Background(), row, time.Date(2026, 6, 3, 0, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("fireScheduledWakeup returned error: %v", err)
	}
	if schedules.firedID != row.WakeupID {
		t.Fatalf("fired wakeup id = %q, want %q", schedules.firedID, row.WakeupID)
	}
	if schedules.firedTurn != "turn_schedule_wakeup-wakeup_123" {
		t.Fatalf("fired turn = %q", schedules.firedTurn)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	cmd := bus.commands[0]
	if cmd.Source != "schedule-wakeup" || cmd.ClientNonce != row.ClientNonce || cmd.Prompt != row.Prompt {
		t.Fatalf("command = %+v", cmd)
	}
	events := app.sessionEvents.(*recordingSessionEventStore).upserts
	if len(events) != 3 {
		t.Fatalf("event upserts = %d, want 3", len(events))
	}
	if got, _ := events[0]["type"].(string); got != "user_message.created" {
		t.Fatalf("first event type = %q", got)
	}
	if got, _ := events[1]["type"].(string); got != "turn.submitted" {
		t.Fatalf("second event type = %q", got)
	}
	if got, _ := events[2]["type"].(string); got != "scheduled_wakeup.updated" {
		t.Fatalf("third event type = %q", got)
	}
	if got, _ := events[0]["author_kind"].(string); got != "system" {
		t.Fatalf("author_kind = %q, want system", got)
	}
	userPayload, _ := events[0]["payload"].(map[string]any)
	if got, _ := userPayload["text"].(string); got != "Timer went off!" {
		t.Fatalf("user_message.created payload.text = %q, want timer announcement", got)
	}
	if got, _ := userPayload["source"].(string); got != "schedule-wakeup" {
		t.Fatalf("user_message.created payload.source = %q, want schedule-wakeup", got)
	}
	if got, _ := userPayload["prompt"].(string); got != row.Prompt {
		t.Fatalf("user_message.created payload.prompt = %q, want wake prompt", got)
	}
	submitPayload, _ := events[1]["payload"].(map[string]any)
	if got, _ := submitPayload["source"].(string); got != "schedule-wakeup" {
		t.Fatalf("turn.submitted payload.source = %q, want schedule-wakeup", got)
	}
	if got, _ := submitPayload["prompt"].(string); got != row.Prompt {
		t.Fatalf("turn.submitted payload.prompt = %q, want wake prompt", got)
	}
	wakeupPayload, _ := events[2]["payload"].(map[string]any)
	if got, _ := wakeupPayload["status"].(string); got != "fired" {
		t.Fatalf("scheduled_wakeup.updated payload.status = %q, want fired", got)
	}
	if got, _ := wakeupPayload["wakeup_id"].(string); got != row.WakeupID {
		t.Fatalf("scheduled_wakeup.updated payload.wakeup_id = %q, want %q", got, row.WakeupID)
	}
}

// TestFireScheduledWakeupFailsDeadSessionDurablyAndRings pins the truly-dead
// half of the fire ladder (docs/scheduled-turn-continuity.md "Failure model"):
// a missing session row, a terminating session, and a Failed session each get
// the immediate durable MarkFailed terminal AND the away-error ring — the
// scheduled_wakeup.updated wake trail plus the away-tagged turn.command_failed
// carrier, the same two-event shape the attempt-cap path pins in
// TestProcessScheduledWakeupsTerminalsCappedRows. Pre-#1079 the dead paths
// resolved silently (recompute only), stranding the user with zero signal that
// the promised continuation died.
func TestFireScheduledWakeupFailsDeadSessionDurablyAndRings(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*pgstore.ScheduledWakeup)
		wantReason string
	}{
		{
			name:       "missing session row",
			mutate:     func(row *pgstore.ScheduledWakeup) { row.SessionStatus = "" },
			wantReason: "session_not_found",
		},
		{
			name: "terminating session",
			mutate: func(row *pgstore.ScheduledWakeup) {
				row.SessionStatus = "Active"
				row.SessionTerminated = true
			},
			wantReason: "session_not_active",
		},
		{
			name:       "failed session",
			mutate:     func(row *pgstore.ScheduledWakeup) { row.SessionStatus = "Failed" },
			wantReason: "session_not_active",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testTurnsApp(t, &recordingSessionBus{})
			schedules := &fakeScheduledWakeupStore{}
			app.scheduledWakeups = schedules
			app.sessionEvents = &recordingSessionEventStore{}
			row := pgstore.ScheduledWakeup{
				WakeupID:      "wakeup_inactive",
				SessionScope:  "default",
				SessionID:     "63",
				TankSessionID: "63",
				OwnerEmail:    "user@example.com",
				Provider:      "claude",
				Prompt:        "resume after sleep",
				ClientNonce:   "schedule_wakeup-wakeup_inactive",
				ScheduledAt:   time.Date(2026, 6, 3, 15, 20, 0, 0, time.UTC),
				DueAt:         time.Date(2026, 6, 3, 15, 25, 0, 0, time.UTC),
			}
			tc.mutate(&row)

			if err := app.fireScheduledWakeup(context.Background(), row, time.Now().UTC()); err == nil {
				t.Fatal("fireScheduledWakeup error = nil, want failure")
			}
			if schedules.failedID != row.WakeupID || schedules.failReason != tc.wantReason {
				t.Fatalf("failed = (%q, %q), want (%q, %s)", schedules.failedID, schedules.failReason, row.WakeupID, tc.wantReason)
			}
			if schedules.releaseCalls != 0 {
				t.Fatalf("release calls = %d, want 0 (dead sessions fail fast, never defer)", schedules.releaseCalls)
			}
			events := app.sessionEvents.(*recordingSessionEventStore).upserts
			if len(events) != 2 {
				t.Fatalf("event upserts = %d, want the wake trail AND the ring carrier", len(events))
			}
			if got, _ := events[0]["type"].(string); got != "scheduled_wakeup.updated" {
				t.Fatalf("event[0] type = %q, want scheduled_wakeup.updated", got)
			}
			payload, _ := events[0]["payload"].(map[string]any)
			if got, _ := payload["status"].(string); got != "failed" {
				t.Fatalf("scheduled_wakeup.updated payload.status = %q, want failed", got)
			}
			if got, _ := payload["last_error"].(string); got != tc.wantReason {
				t.Fatalf("scheduled_wakeup.updated payload.last_error = %q, want %s", got, tc.wantReason)
			}
			if got, _ := events[1]["type"].(string); got != "turn.command_failed" {
				t.Fatalf("event[1] type = %q, want turn.command_failed (the ring carrier)", got)
			}
			ringPayload, _ := events[1]["payload"].(map[string]any)
			if got, _ := ringPayload["reason"].(string); got != sessionactivity.AwayErrorReasonScheduledWakeup {
				t.Fatalf("ring reason = %q, want %q", got, sessionactivity.AwayErrorReasonScheduledWakeup)
			}
			if got, _ := events[1]["turn_id"].(string); got != "turn_schedule_wakeup-wakeup_inactive" {
				t.Fatalf("ring carrier turn_id = %q, want the wake's deterministic turn", got)
			}
		})
	}
}

// TestFireScheduledWakeupDefersWhileSessionPending pins the transient half of
// the fire ladder: the K8s watch flips the durable session row Active →
// Pending on ANY probe blip (sessioncontroller/writer.go), so a wake claimed
// during a 10s kubelet hiccup must release its claim and retry — not
// terminal-fail the agent's continuation. The release retains the attempt
// bump (no MarkFailed, no events, no command), leaving the attempt cap as the
// bound that rings through FailExceeded if the session never recovers.
func TestFireScheduledWakeupDefersWhileSessionPending(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	schedules := &fakeScheduledWakeupStore{}
	app.scheduledWakeups = schedules
	app.sessionEvents = &recordingSessionEventStore{}
	before := testutil.ToFloat64(scheduledWakeupFireTotal.WithLabelValues("claude", "deferred_session_not_active"))
	row := pgstore.ScheduledWakeup{
		WakeupID:      "wakeup_pending",
		SessionScope:  "default",
		SessionID:     "63",
		TankSessionID: "63",
		OwnerEmail:    "user@example.com",
		Provider:      "claude",
		Prompt:        "resume after sleep",
		ClientNonce:   "schedule_wakeup-wakeup_pending",
		ScheduledAt:   time.Date(2026, 6, 3, 15, 20, 0, 0, time.UTC),
		DueAt:         time.Date(2026, 6, 3, 15, 25, 0, 0, time.UTC),
		SessionStatus: "Pending",
	}

	if err := app.fireScheduledWakeup(context.Background(), row, time.Now().UTC()); err != nil {
		t.Fatalf("fireScheduledWakeup returned error: %v", err)
	}
	if schedules.releasedID != row.WakeupID || schedules.releaseCalls != 1 {
		t.Fatalf("release = (%q, %d), want (%q, 1) — Pending must defer, not die", schedules.releasedID, schedules.releaseCalls, row.WakeupID)
	}
	if schedules.failedID != "" {
		t.Fatalf("MarkFailed called with %q, want no terminal for a transient blip", schedules.failedID)
	}
	if schedules.firedID != "" || len(bus.commands) != 0 {
		t.Fatalf("fired id = %q commands = %d, want no fire while Pending", schedules.firedID, len(bus.commands))
	}
	if events := app.sessionEvents.(*recordingSessionEventStore).upserts; len(events) != 0 {
		t.Fatalf("event upserts = %d, want 0 (a defer is not a durable outcome)", len(events))
	}
	if after := testutil.ToFloat64(scheduledWakeupFireTotal.WithLabelValues("claude", "deferred_session_not_active")); after != before+1 {
		t.Fatalf("deferred_session_not_active counter = %v, want %v", after, before+1)
	}
}

func TestListScheduledWakeupsSurfacesDurableRows(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "claude-runner"))
	scheduledAt := time.Date(2026, 6, 3, 15, 20, 0, 0, time.UTC)
	dueAt := scheduledAt.Add(5 * time.Minute)
	app.scheduledWakeups = &fakeScheduledWakeupStore{rows: []pgstore.ScheduledWakeup{{
		WakeupID:          "wakeup_123",
		SessionID:         "63",
		Provider:          "claude",
		Prompt:            "check CI",
		ClientNonce:       "schedule_wakeup-wakeup_123",
		ScheduledTurnID:   "turn_abc",
		ProviderItemID:    "toolu_123",
		ScheduledAt:       scheduledAt,
		DueAt:             dueAt,
		Status:            pgstore.ScheduledWakeupScheduled,
		AttemptCount:      0,
		FiredTurnID:       "",
		LastError:         "",
		SessionScope:      "default",
		TankSessionID:     "63",
		OwnerEmail:        "user@example.com",
		SessionStatus:     "Active",
		SessionTerminated: false,
	}}}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/scheduled-wakeups?session_scope=default", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	app.handleListScheduledWakeups(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ScheduledWakeups []struct {
			WakeupID        string `json:"wakeup_id"`
			Status          string `json:"status"`
			Prompt          string `json:"prompt"`
			ScheduledTurnID string `json:"scheduled_turn_id"`
			ProviderItemID  string `json:"provider_item_id"`
			DueAt           string `json:"due_at"`
		} `json:"scheduled_wakeups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.ScheduledWakeups) != 1 {
		t.Fatalf("rows = %d, want 1", len(body.ScheduledWakeups))
	}
	row := body.ScheduledWakeups[0]
	if row.WakeupID != "wakeup_123" || row.Status != "scheduled" || row.Prompt != "check CI" || row.ProviderItemID != "toolu_123" {
		t.Fatalf("row = %+v", row)
	}
	if row.ScheduledTurnID != "turn_abc" || row.DueAt != dueAt.Format(time.RFC3339Nano) {
		t.Fatalf("row timing/turn = %+v", row)
	}
}

// TestHandleInternalRegisterScheduledWakeupRefreshesActivityAndEchoesRowStatus
// pins the register boundary's two #1079 fixes. (1) The wake-row write is what
// parks the session, so the handler must recompute the durable activity
// summary (the cancel path always did; registration never had, leaving the
// fold to flash "ready" until the next chat event — docs/scheduled-turn-
// continuity.md "Race"). (2) The response must echo the row's ACTUAL status:
// pgstore Register's ON CONFLICT returns the existing row untouched, which can
// already be fired/cancelled on a runner retry, and an unconditional
// "scheduled" would report a resolved wake as pending.
func TestHandleInternalRegisterScheduledWakeupRefreshesActivityAndEchoesRowStatus(t *testing.T) {
	scheduledAt := time.Date(2026, 6, 12, 7, 0, 0, 0, time.UTC)
	baseRow := pgstore.ScheduledWakeup{
		WakeupID:       "wakeup_existing",
		SessionScope:   "slot-a",
		SessionID:      "12",
		TankSessionID:  sessionmodel.SessionStorageKey("slot-a", "12"),
		OwnerEmail:     "owner@example.test",
		Provider:       "claude",
		Prompt:         "resume after sleep",
		ClientNonce:    "schedule_wakeup-wakeup_existing",
		ProviderItemID: "toolu_wake",
		ScheduledAt:    scheduledAt,
		DueAt:          scheduledAt.Add(5 * time.Minute),
	}
	cases := []struct {
		name       string
		rowStatus  pgstore.ScheduledWakeupStatus
		wantStatus string
	}{
		{name: "fresh registration", rowStatus: pgstore.ScheduledWakeupScheduled, wantStatus: "scheduled"},
		{name: "conflict returns already-fired row", rowStatus: pgstore.ScheduledWakeupFired, wantStatus: "fired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := internalSessionRuntimeServer(t, "12")
			registry := newTestSessionRegistry(sessionmodel.SessionRecord{
				ID:      "12",
				Email:   "owner@example.test",
				Mode:    sessionmodel.ClaudeGUIMode,
				Visible: true,
				Status:  "Active",
			})
			server.mgr = sessions.NewManager(server.k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})
			refresher := &recordingActivityRefresher{}
			server.activityRefresher = refresher
			server.sessionEvents = &recordingSessionEventStore{}
			row := baseRow
			row.Status = tc.rowStatus
			server.scheduledWakeups = &fakeScheduledWakeupStore{registerRow: row}

			req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/12/scheduled-wakeups",
				strings.NewReader(`{"delay_ms":300000,"prompt":"resume after sleep","provider_item_id":"toolu_wake"}`))
			req.SetPathValue("session_id", "12")
			req.Header.Set("Authorization", "Bearer session-token")
			rec := httptest.NewRecorder()

			server.handleInternalRegisterScheduledWakeup(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Status      string `json:"status"`
				WakeupID    string `json:"wakeup_id"`
				ClientNonce string `json:"client_nonce"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Status != tc.wantStatus {
				t.Fatalf("response status = %q, want %q (echo the durable row, not a constant)", body.Status, tc.wantStatus)
			}
			if body.WakeupID != row.WakeupID || body.ClientNonce != row.ClientNonce {
				t.Fatalf("response identity = %+v, want row identity", body)
			}
			if len(refresher.calls) != 1 {
				t.Fatalf("activity refresh calls = %d, want 1 (registration parks the session)", len(refresher.calls))
			}
			if call := refresher.calls[0]; call.owner != "owner@example.test" || call.scope != "slot-a" || call.sessionID != "12" {
				t.Fatalf("activity refresh call = %#v, want owner/scope/session", call)
			}
			events := server.sessionEvents.(*recordingSessionEventStore).upserts
			if len(events) != 1 {
				t.Fatalf("event upserts = %d, want the wake trail event", len(events))
			}
			if got, _ := events[0]["type"].(string); got != "scheduled_wakeup.updated" {
				t.Fatalf("event type = %q, want scheduled_wakeup.updated", got)
			}
			payload, _ := events[0]["payload"].(map[string]any)
			if got, _ := payload["status"].(string); got != tc.wantStatus {
				t.Fatalf("wake trail payload.status = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

// TestProcessScheduledWakeupsTerminalsCappedRows pins the attempt-cap
// bookkeeping: a wake stuck at pgstore.MaxScheduledWakeupAttempts is
// terminaled by the FailExceeded pass and gets the SAME durable trail a
// MarkFailed wake gets — the scheduled_wakeup.updated ledger event with the
// failed status and the cap reason — so the broken self-scheduled
// continuation rings the away-error summon instead of sitting in 'claiming'
// limbo forever while ClaimDue refuses it.
func TestProcessScheduledWakeupsTerminalsCappedRows(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	schedules := &fakeScheduledWakeupStore{exceededRows: []pgstore.ScheduledWakeup{{
		WakeupID:      "wakeup_capped",
		SessionScope:  "default",
		SessionID:     "63",
		TankSessionID: "63",
		OwnerEmail:    "user@example.com",
		Provider:      "claude",
		Prompt:        "resume after sleep",
		ClientNonce:   "schedule_wakeup-wakeup_capped",
		ScheduledAt:   time.Date(2026, 6, 12, 7, 0, 0, 0, time.UTC),
		DueAt:         time.Date(2026, 6, 12, 7, 5, 0, 0, time.UTC),
		AttemptCount:  pgstore.MaxScheduledWakeupAttempts,
	}}}
	app.scheduledWakeups = schedules
	app.sessionEvents = &recordingSessionEventStore{}

	if err := app.processScheduledWakeups(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("processScheduledWakeups: %v", err)
	}
	if schedules.failExceededCall != 1 {
		t.Fatalf("FailExceeded calls = %d, want 1 per tick", schedules.failExceededCall)
	}
	events := app.sessionEvents.(*recordingSessionEventStore).upserts
	if len(events) != 2 {
		t.Fatalf("event upserts = %d, want the wake trail AND the ring carrier", len(events))
	}
	if got, _ := events[0]["type"].(string); got != "scheduled_wakeup.updated" {
		t.Fatalf("event[0] type = %q, want scheduled_wakeup.updated", got)
	}
	payload, _ := events[0]["payload"].(map[string]any)
	if got, _ := payload["status"].(string); got != "failed" {
		t.Fatalf("payload.status = %q, want failed", got)
	}
	if got, _ := payload["last_error"].(string); !strings.HasPrefix(got, "attempt_cap_exceeded") {
		t.Fatalf("payload.last_error = %q, want attempt_cap_exceeded prefix", got)
	}
	// The capped wake is a broken self-scheduled continuation: it must ring
	// the away-error summon exactly like any failed wake — resolveFailedWake
	// persists the away-tagged turn.command_failed the activity fold and SPA
	// ring key off.
	if got, _ := events[1]["type"].(string); got != "turn.command_failed" {
		t.Fatalf("event[1] type = %q, want turn.command_failed (the ring carrier)", got)
	}
	ringPayload, _ := events[1]["payload"].(map[string]any)
	if got, _ := ringPayload["reason"].(string); got != sessionactivity.AwayErrorReasonScheduledWakeup {
		t.Fatalf("ring reason = %q, want %q", got, sessionactivity.AwayErrorReasonScheduledWakeup)
	}
}
