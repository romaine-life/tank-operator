package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type fakeBackgroundTaskWakeStore struct {
	rows            []pgstore.BackgroundTaskWake
	firedID         string
	firedTurn       string
	failedID        string
	failReason      string
	releasedID      string
	cancelCalls     int
	cancelSessionID string
	cancelReturn    int64
}

func (f *fakeBackgroundTaskWakeStore) Register(context.Context, pgstore.RegisterBackgroundTaskWakeRequest) (pgstore.BackgroundTaskWake, error) {
	return pgstore.BackgroundTaskWake{}, nil
}

func (f *fakeBackgroundTaskWakeStore) ClaimDue(context.Context, time.Time, int, time.Duration) ([]pgstore.BackgroundTaskWake, error) {
	return f.rows, nil
}

func (f *fakeBackgroundTaskWakeStore) MarkFired(_ context.Context, wakeID, turnID string) error {
	f.firedID = wakeID
	f.firedTurn = turnID
	return nil
}

func (f *fakeBackgroundTaskWakeStore) MarkFailed(_ context.Context, wakeID, reason string) error {
	f.failedID = wakeID
	f.failReason = reason
	return nil
}

func (f *fakeBackgroundTaskWakeStore) Release(_ context.Context, wakeID string) error {
	f.releasedID = wakeID
	return nil
}

func (f *fakeBackgroundTaskWakeStore) DueCount(context.Context, time.Time) (int, error) {
	return len(f.rows), nil
}

func (f *fakeBackgroundTaskWakeStore) CancelPendingForSession(_ context.Context, _, sessionID string) (int64, error) {
	f.cancelCalls++
	f.cancelSessionID = sessionID
	return f.cancelReturn, nil
}

func backgroundWakeRow() pgstore.BackgroundTaskWake {
	return pgstore.BackgroundTaskWake{
		WakeID:            "bgwake_abc",
		SessionScope:      "default",
		SessionID:         "63",
		TankSessionID:     "63",
		OwnerEmail:        "user@example.com",
		Provider:          "claude",
		TaskID:            "bocpzxcm3",
		TaskStatus:        "completed",
		Prompt:            "A background task you started earlier has finished.",
		ClientNonce:       "bgtask-bocpzxcm3",
		SessionStatus:     "Active",
		SessionTerminated: false,
		SessionNeedsInput: false,
	}
}

func TestFireBackgroundTaskWakeUsesDurableTurnBoundary(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "claude-runner"))
	wakes := &fakeBackgroundTaskWakeStore{}
	app.backgroundTaskWakes = wakes
	app.sessionEvents = &recordingSessionEventStore{}
	row := backgroundWakeRow()

	if err := app.fireBackgroundTaskWake(context.Background(), row, time.Date(2026, 6, 3, 0, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("fireBackgroundTaskWake returned error: %v", err)
	}
	if wakes.firedID != row.WakeID {
		t.Fatalf("fired wake id = %q, want %q", wakes.firedID, row.WakeID)
	}
	if wakes.firedTurn != "turn_bgtask-bocpzxcm3" {
		t.Fatalf("fired turn = %q", wakes.firedTurn)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	cmd := bus.commands[0]
	if cmd.Source != "background-task" || cmd.ClientNonce != row.ClientNonce || cmd.Prompt != row.Prompt {
		t.Fatalf("command = %+v", cmd)
	}
	events := app.sessionEvents.(*recordingSessionEventStore).upserts
	if len(events) != 1 {
		t.Fatalf("boundary upserts = %d, want 1", len(events))
	}
	if got, _ := events[0]["type"].(string); got != "turn.submitted" {
		t.Fatalf("event type = %q, want turn.submitted", got)
	}
	payload, _ := events[0]["payload"].(map[string]any)
	if got, _ := payload["source"].(string); got != "background-task" {
		t.Fatalf("turn.submitted payload.source = %q, want background-task", got)
	}
	if got, _ := payload["prompt"].(string); got != row.Prompt {
		t.Fatalf("turn.submitted payload.prompt = %q, want wake prompt", got)
	}
	for _, event := range events {
		if got, _ := event["type"].(string); got == "user_message.created" {
			t.Fatalf("background-task wake prompt leaked into main transcript event: %#v", event)
		}
	}
}

func TestFireBackgroundTaskWakeDefersWhenAwaitingInput(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "claude-runner"))
	wakes := &fakeBackgroundTaskWakeStore{}
	app.backgroundTaskWakes = wakes
	app.sessionEvents = &recordingSessionEventStore{}
	row := backgroundWakeRow()
	row.SessionNeedsInput = true

	if err := app.fireBackgroundTaskWake(context.Background(), row, time.Now().UTC()); err != nil {
		t.Fatalf("fireBackgroundTaskWake returned error: %v", err)
	}
	if wakes.releasedID != row.WakeID {
		t.Fatalf("released id = %q, want %q (must defer, not clobber the pending question)", wakes.releasedID, row.WakeID)
	}
	if wakes.firedID != "" {
		t.Fatalf("fired id = %q, want empty (deferred)", wakes.firedID)
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0 while awaiting input", len(bus.commands))
	}
}

func TestFireBackgroundTaskWakeFailsInactiveSessionDurably(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	wakes := &fakeBackgroundTaskWakeStore{}
	app.backgroundTaskWakes = wakes
	row := backgroundWakeRow()
	row.SessionStatus = "Failed"

	if err := app.fireBackgroundTaskWake(context.Background(), row, time.Now().UTC()); err == nil {
		t.Fatal("fireBackgroundTaskWake error = nil, want failure")
	}
	if wakes.failedID != row.WakeID || wakes.failReason != "session_not_active" {
		t.Fatalf("failed = (%q, %q), want (%q, session_not_active)", wakes.failedID, wakes.failReason, row.WakeID)
	}
}

func TestSdkTurnSourceIncludesBackgroundTask(t *testing.T) {
	if got := sdkTurnSource("background-task"); got != "background-task" {
		t.Fatalf("sdkTurnSource(background-task) = %q, want background-task", got)
	}
	if got := sdkTurnSource("launch-dispatch"); got != "launch-dispatch" {
		t.Fatalf("sdkTurnSource(launch-dispatch) = %q, want launch-dispatch", got)
	}
	if got := sdkTurnSource("something-else"); got != "sdk" {
		t.Fatalf("sdkTurnSource(something-else) = %q, want sdk", got)
	}
}

func TestBuildBackgroundTaskWakePromptIncludesTaskContext(t *testing.T) {
	p := buildBackgroundTaskWakePrompt("taskX", "completed", "Wait for CI", "all green", "Bash", "")
	for _, want := range []string{"taskX", "completed", "Wait for CI", "all green", "BashOutput"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestBackgroundTaskWakeClientNonceIsTurnIDSafe(t *testing.T) {
	// A normal alnum task id is used verbatim; a colon-bearing id (allowed by
	// backgroundTaskIDPattern but not turnIDPattern) falls back to a hash. Both
	// must satisfy turnIDPattern so the derived turn id is valid.
	for _, taskID := range []string{"bocpzxcm3", "shell:weird/id", strings.Repeat("x", 200)} {
		nonce := pgstore.BackgroundTaskWakeClientNonce(taskID)
		if !turnIDPattern.MatchString(nonce) {
			t.Fatalf("client nonce %q for task %q does not match turnIDPattern", nonce, taskID)
		}
	}
}
