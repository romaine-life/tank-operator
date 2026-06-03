package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type fakeScheduledWakeupStore struct {
	rows       []pgstore.ScheduledWakeup
	firedID    string
	firedTurn  string
	failedID   string
	failReason string
}

func (f *fakeScheduledWakeupStore) Register(context.Context, pgstore.RegisterScheduledWakeupRequest) (pgstore.ScheduledWakeup, error) {
	return pgstore.ScheduledWakeup{}, nil
}

func (f *fakeScheduledWakeupStore) ClaimDue(context.Context, time.Time, int, time.Duration) ([]pgstore.ScheduledWakeup, error) {
	return f.rows, nil
}

func (f *fakeScheduledWakeupStore) ListBySession(context.Context, string, string) ([]pgstore.ScheduledWakeup, error) {
	return f.rows, nil
}

func (f *fakeScheduledWakeupStore) MarkFired(_ context.Context, wakeupID, turnID string) error {
	f.firedID = wakeupID
	f.firedTurn = turnID
	return nil
}

func (f *fakeScheduledWakeupStore) MarkFailed(_ context.Context, wakeupID, reason string) error {
	f.failedID = wakeupID
	f.failReason = reason
	return nil
}

func (f *fakeScheduledWakeupStore) ScheduledDueCount(context.Context, time.Time) (int, error) {
	return len(f.rows), nil
}

func TestFireScheduledWakeupUsesDurableTurnBoundary(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "agent-runner"))
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
	if len(events) != 2 {
		t.Fatalf("boundary upserts = %d, want 2", len(events))
	}
	if got, _ := events[0]["type"].(string); got != "user_message.created" {
		t.Fatalf("first event type = %q", got)
	}
	if got, _ := events[1]["type"].(string); got != "turn.submitted" {
		t.Fatalf("second event type = %q", got)
	}
	if got, _ := events[0]["author_kind"].(string); got != "system" {
		t.Fatalf("author_kind = %q, want system", got)
	}
}

func TestFireScheduledWakeupFailsInactiveSessionDurably(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	schedules := &fakeScheduledWakeupStore{}
	app.scheduledWakeups = schedules
	row := pgstore.ScheduledWakeup{
		WakeupID:      "wakeup_inactive",
		SessionID:     "63",
		OwnerEmail:    "user@example.com",
		Provider:      "claude",
		ClientNonce:   "schedule_wakeup-wakeup_inactive",
		SessionStatus: "Failed",
	}

	if err := app.fireScheduledWakeup(context.Background(), row, time.Now().UTC()); err == nil {
		t.Fatal("fireScheduledWakeup error = nil, want failure")
	}
	if schedules.failedID != row.WakeupID || schedules.failReason != "session_not_active" {
		t.Fatalf("failed = (%q, %q), want (%q, session_not_active)", schedules.failedID, schedules.failReason, row.WakeupID)
	}
}

func TestListScheduledWakeupsSurfacesDurableRows(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "agent-runner"))
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
