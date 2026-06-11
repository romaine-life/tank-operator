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
	cancelTaskID    string
	cancelReturn    int64
}

func (f *fakeBackgroundTaskWakeStore) Register(context.Context, pgstore.RegisterBackgroundTaskWakeRequest) (pgstore.BackgroundTaskWake, pgstore.BackgroundTaskWakeRegisterOutcome, error) {
	return pgstore.BackgroundTaskWake{}, pgstore.BackgroundTaskWakeRegisterScheduled, nil
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

func (f *fakeBackgroundTaskWakeStore) CancelPendingForTask(_ context.Context, _, sessionID, taskID, _ string) (int64, error) {
	f.cancelCalls++
	f.cancelSessionID = sessionID
	f.cancelTaskID = taskID
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
		TaskDescription:   "Wait for CI",
		TaskSummary:       "all green",
		TaskLastTool:      "Bash",
		Generation:        1,
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
	if cmd.Source != "background-task" || cmd.ClientNonce != row.ClientNonce {
		t.Fatalf("command = %+v", cmd)
	}
	// The prompt is composed provider-aware at fire time from the row's
	// structured task facts.
	if want := buildBackgroundTaskWakePromptForProvider(row); cmd.Prompt != want {
		t.Fatalf("command prompt = %q, want fire-time composed prompt %q", cmd.Prompt, want)
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
	if got, _ := payload["prompt"].(string); got != buildBackgroundTaskWakePromptForProvider(row) {
		t.Fatalf("turn.submitted payload.prompt = %q, want fire-time composed wake prompt", got)
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
	if got := sdkTurnSource("agent-continuation"); got != "agent-continuation" {
		t.Fatalf("sdkTurnSource(agent-continuation) = %q, want agent-continuation", got)
	}
	if got := sdkTurnSource("something-else"); got != "sdk" {
		t.Fatalf("sdkTurnSource(something-else) = %q, want sdk", got)
	}
}

func TestBuildBackgroundTaskWakePromptIsProviderAwareAndDemandsReport(t *testing.T) {
	row := backgroundWakeRow()
	row.TaskID = "taskX"

	claude := buildBackgroundTaskWakePromptForProvider(row)
	for _, want := range []string{"taskX", "completed", "Wait for CI", "all green", "BashOutput", "reporting the task's outcome", "Do not end the turn silently"} {
		if !strings.Contains(claude, want) {
			t.Fatalf("claude prompt missing %q:\n%s", want, claude)
		}
	}

	// Codex has no BashOutput/TaskOutput tools; sending Claude tool names plus
	// an "end without taking action" escape produced zero fulfilled reports
	// across every fired wake of the session-161 bug museum.
	row.Provider = "codex"
	codex := buildBackgroundTaskWakePromptForProvider(row)
	if strings.Contains(codex, "BashOutput") || strings.Contains(codex, "TaskOutput") {
		t.Fatalf("codex prompt names Claude tools:\n%s", codex)
	}
	for _, want := range []string{"taskX", "your shell", "reporting the task's outcome", "Do not end the turn silently"} {
		if !strings.Contains(codex, want) {
			t.Fatalf("codex prompt missing %q:\n%s", want, codex)
		}
	}
	if strings.Contains(codex, "end the turn without taking action") {
		t.Fatalf("codex prompt kept the silent-obedience escape:\n%s", codex)
	}

	// Unknown status = observability honestly lost, never claimed completion.
	row.TaskStatus = "unknown"
	unknown := buildBackgroundTaskWakePromptForProvider(row)
	if !strings.Contains(unknown, "lost the ability to observe") {
		t.Fatalf("unknown-status prompt does not state lost observability:\n%s", unknown)
	}
	if strings.Contains(unknown, "has finished while this session was idle") {
		t.Fatalf("unknown-status prompt claims completion:\n%s", unknown)
	}
	if !strings.Contains(unknown, "you will be re-invoked once more") {
		t.Fatalf("codex unknown-status prompt missing the re-arm follow-up note:\n%s", unknown)
	}

	// A re-armed generation says so: the agent should know the earlier
	// notification may have been premature.
	row.TaskStatus = "completed"
	row.Generation = 2
	rearmed := buildBackgroundTaskWakePromptForProvider(row)
	if !strings.Contains(rearmed, "premature") {
		t.Fatalf("generation-2 prompt missing premature-observation note:\n%s", rearmed)
	}
}

func TestFireBackgroundTaskWakeDefersWhileTurnActive(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", "claude_gui", "claude-runner"))
	wakes := &fakeBackgroundTaskWakeStore{}
	app.backgroundTaskWakes = wakes
	app.sessionEvents = &recordingSessionEventStore{}
	row := backgroundWakeRow()
	row.SessionActivityStatus = "streaming"

	if err := app.fireBackgroundTaskWake(context.Background(), row, time.Now().UTC()); err != nil {
		t.Fatalf("fireBackgroundTaskWake returned error: %v", err)
	}
	if wakes.releasedID != row.WakeID {
		t.Fatalf("released id = %q, want %q (must defer while a turn is in flight)", wakes.releasedID, row.WakeID)
	}
	if wakes.firedID != "" || len(bus.commands) != 0 {
		t.Fatalf("fired id = %q commands = %d, want deferred fire", wakes.firedID, len(bus.commands))
	}

	// ready / scheduled / empty statuses do not block.
	for _, status := range []string{"", "ready", "scheduled", "error", "stopped", "needs_input"} {
		if backgroundTaskWakeActivityStatusBlocksFire(status) {
			t.Fatalf("activity status %q must not block wake fire", status)
		}
	}
	for _, status := range []string{"submitted", "claimed", "streaming", "stopping"} {
		if !backgroundTaskWakeActivityStatusBlocksFire(status) {
			t.Fatalf("activity status %q must block wake fire", status)
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

// TestProviderSelfContinues pins the realm-split predicate: only antigravity
// self-continues, so only it is rejected by the Tank-owned wake paths (scheduled
// wakeup, background-task wake) and accepted by the agent-continuation relay.
// Claude/Codex are not self-continuing — Tank owns their wake rows. See
// backend-go/cmd/antigravity-runner/ARCHITECTURE.md.
func TestProviderSelfContinues(t *testing.T) {
	if !providerSelfContinues("antigravity") {
		t.Fatal("providerSelfContinues(antigravity) = false, want true")
	}
	// Tolerant of surrounding whitespace (matches sdkProviderForMode's trimmed output).
	if !providerSelfContinues("  antigravity  ") {
		t.Fatal(`providerSelfContinues("  antigravity  ") = false, want true (trimmed)`)
	}
	for _, provider := range []string{"claude", "codex", "", "antigravity-ish"} {
		if providerSelfContinues(provider) {
			t.Fatalf("providerSelfContinues(%q) = true, want false", provider)
		}
	}
}

// TestProjectUnresolvedBackgroundTasks pins the runner-restart re-adoption
// feed: a started task with no exited is listed with the fields a runner
// needs; an exited task is not; a task whose exit arrived before a later
// restart never resurfaces.
func TestProjectUnresolvedBackgroundTasks(t *testing.T) {
	events := []map[string]any{
		projectionTestEvent("started-a", "001", "shell_task.started", "tool", "codex", "turn-1", "turn-1:shell_task:taska", map[string]any{
			"task_id": "taska", "status": "running",
			"command": "/bin/sh -lc 'sleep 600'", "process_id": "4242",
		}),
		projectionTestEvent("started-b", "002", "shell_task.started", "tool", "claude", "turn-2", "turn-2:shell_task:taskb", map[string]any{
			"task_id": "taskb", "status": "running", "description": "Wait for CI",
		}),
		projectionTestEvent("exited-b", "003", "shell_task.exited", "tool", "claude", "turn-2", "turn-2:shell_task:taskb", map[string]any{
			"task_id": "taskb", "status": "completed",
		}),
	}
	out := projectUnresolvedBackgroundTasks(events)
	if len(out) != 1 {
		t.Fatalf("unresolved tasks = %d, want 1: %#v", len(out), out)
	}
	got := out[0]
	if got["task_id"] != "taska" || got["turn_id"] != "turn-1" {
		t.Fatalf("unresolved task identity = %#v", got)
	}
	if got["command"] != "/bin/sh -lc 'sleep 600'" || got["process_id"] != "4242" {
		t.Fatalf("unresolved task facts = %#v", got)
	}
	if got["started_event_id"] != "started-a" {
		t.Fatalf("started_event_id = %#v, want started-a", got["started_event_id"])
	}
}

// TestBackgroundWakeChipTitleTracksLostObservability pins the composer and
// the projection chip together: the unknown-status prompt opens with the
// lost-observability prefix the chip keys on, and the chip then refuses to
// claim the task finished.
func TestBackgroundWakeChipTitleTracksLostObservability(t *testing.T) {
	row := backgroundWakeRow()
	row.TaskStatus = "unknown"
	prompt := buildBackgroundTaskWakePromptForProvider(row)
	if !strings.HasPrefix(prompt, backgroundWakeLostObservabilityPromptPrefix) {
		t.Fatalf("unknown-status prompt does not open with the chip prefix %q:\n%s", backgroundWakeLostObservabilityPromptPrefix, prompt)
	}
	// No observer remains for claude after a restart closure: the prompt must
	// forbid the model from promising a follow-up report the harness can
	// never deliver — "I'll report when it finishes" was the residual lie of
	// the slot-6 restart round.
	if !strings.Contains(prompt, "do not promise an automatic follow-up report") {
		t.Fatalf("claude unknown-status prompt missing the no-follow-up instruction:\n%s", prompt)
	}

	events := []map[string]any{
		projectionTestEvent("wake-submitted", "001", "turn.submitted", "runner", "tank", "turn_bgtask-x", "", map[string]any{
			"status": "submitted", "source": "background-task", "task_id": "x", "prompt": prompt,
		}),
	}
	projection := projectTranscriptEvents(events)
	body, ok := projection.ActivityBodies["turn_bgtask-x"]
	if !ok || len(body.Entries) == 0 {
		t.Fatalf("wake body missing: %#v", projection.ActivityBodies)
	}
	meta, _ := body.Entries[0]["meta"].(map[string]any)
	if meta == nil || meta["title"] != "Background task lost from view — agent re-invoked" {
		t.Fatalf("chip title = %#v, want lost-from-view title", meta)
	}
}
