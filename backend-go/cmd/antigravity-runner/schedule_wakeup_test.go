package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseScheduleWakeup(t *testing.T) {
	cases := []struct {
		name      string
		args      string
		wantDelay int64
		wantOK    bool
	}{
		{"string duration", `{"DurationSeconds":"5","Prompt":"back in 5"}`, 5000, true},
		{"numeric duration", `{"DurationSeconds":15,"Prompt":"check back"}`, 15000, true},
		{"fractional duration", `{"DurationSeconds":"0.5","Prompt":"soon"}`, 500, true},
		{"negative duration", `{"DurationSeconds":"-1","Prompt":"later"}`, 0, false},
		{"empty prompt", `{"DurationSeconds":"5","Prompt":""}`, 0, false},
		{"missing prompt", `{"DurationSeconds":"5"}`, 0, false},
		{"missing duration", `{"Prompt":"hi"}`, 0, false},
		{"empty args", `{}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := AgyToolCall{Name: "schedule", Args: json.RawMessage(tc.args)}
			delayMs, prompt, ok := parseScheduleWakeup(call)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if delayMs != tc.wantDelay {
				t.Fatalf("delayMs = %d, want %d", delayMs, tc.wantDelay)
			}
			if strings.TrimSpace(prompt) == "" {
				t.Fatalf("prompt should be non-empty")
			}
		})
	}
}

// TestObserveStepRegistersAntigravityScheduleWakeup is the regression guard for the
// timer-wake bug: agy emits a `schedule` tool call (DurationSeconds + Prompt) and
// the runner must register a durable Tank scheduled wakeup. The Go rewrite (#996)
// dropped this, so the session never woke.
func TestObserveStepRegistersAntigravityScheduleWakeup(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	state := &runnerState{}

	type regCall struct {
		providerItemID  string
		delayMs         int64
		prompt          string
		scheduledTurnID string
	}
	calls := make(chan regCall, 1)

	run := newTurnRun(builder, func(map[string]any) error { return nil }, "turn-sched", "nonce-sched")
	run.state = state
	run.scheduleRegister = func(providerItemID string, delayMs int64, prompt, scheduledTurnID string) error {
		calls <- regCall{providerItemID, delayMs, prompt, scheduledTurnID}
		return nil
	}

	line := `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"schedule","args":{"DurationSeconds":"5","Prompt":"Timer of 5 seconds has expired."}}]}`
	var step AgyStep
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/t/transcript_full.jsonl", line, step); err != nil {
		t.Fatalf("observeStep: %v", err)
	}

	select {
	case c := <-calls:
		if c.delayMs != 5000 {
			t.Fatalf("delayMs = %d, want 5000", c.delayMs)
		}
		if c.prompt != "Timer of 5 seconds has expired." {
			t.Fatalf("prompt = %q", c.prompt)
		}
		if c.scheduledTurnID != "turn-sched" {
			t.Fatalf("scheduledTurnID = %q, want turn-sched", c.scheduledTurnID)
		}
		if strings.TrimSpace(c.providerItemID) == "" {
			t.Fatalf("providerItemID must be non-empty (idempotency key)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduleRegister was never called for a schedule tool call")
	}
}

func TestObserveStepIgnoresNonScheduleTool(t *testing.T) {
	builder := eventBuilder{sessionID: "17", sessionStorageKey: "17"}
	run := newTurnRun(builder, func(map[string]any) error { return nil }, "turn-x", "nonce-x")
	run.state = &runnerState{}
	run.scheduleRegister = func(string, int64, string, string) error {
		t.Fatal("scheduleRegister must not fire for a non-schedule tool")
		return nil
	}
	line := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"read_file","args":{"path":"x"}}]}`
	var step AgyStep
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	if err := run.observeStep("/t/transcript_full.jsonl", line, step); err != nil {
		t.Fatalf("observeStep: %v", err)
	}
	// Give any errant goroutine a beat to (incorrectly) fire.
	time.Sleep(50 * time.Millisecond)
}

func TestIsWaitIntentText(t *testing.T) {
	waits := []string{
		"I will now wait for the build to finish.",
		"I'll wait and report back.",
		"I am waiting for the deployment.",
		"I will check back shortly.",
		"Waiting for the workflow to complete.",
	}
	for _, w := range waits {
		if !isWaitIntentText(w) {
			t.Errorf("expected wait intent for %q", w)
		}
	}
	nonWaits := []string{
		"I have scheduled a timer for 5 seconds.",
		"Done. The build passed.",
		"",
	}
	for _, n := range nonWaits {
		if isWaitIntentText(n) {
			t.Errorf("did not expect wait intent for %q", n)
		}
	}
}

func TestIsNativeTimerEchoStep(t *testing.T) {
	yes := []AgyStep{
		{Source: "SYSTEM", Type: "timer_fired"},
		{Source: "system", Type: "BACKGROUND_TASK_COMPLETED"},
		{Source: "SYSTEM", Type: "schedule_wake"},
	}
	for _, s := range yes {
		if !isNativeTimerEchoStep(s) {
			t.Errorf("expected native timer echo for %+v", s)
		}
	}
	no := []AgyStep{
		{Source: "MODEL", Type: "PLANNER_RESPONSE"},
		{Source: "SYSTEM", Type: "ERROR_MESSAGE"},
		{Source: "TOOL", Type: "read_file"},
	}
	for _, s := range no {
		if isNativeTimerEchoStep(s) {
			t.Errorf("did not expect native timer echo for %+v", s)
		}
	}
}

// TestScheduledWakeupParksNativeTimerEcho proves Tank owns the single wake: once a
// durable scheduled wakeup is registered, agy's own native timer echo must not also
// drive a background-task wake (which would resume the session twice).
func TestScheduledWakeupParksNativeTimerEcho(t *testing.T) {
	echo := AgyStep{Source: "SYSTEM", Type: "timer_fired"}

	parked := &runnerState{}
	parked.scheduledWakeups.Store(1)
	if err := parked.handleStep("/t/transcript_full.jsonl", "{}", echo, runnerConfig{}); err != nil {
		t.Fatal(err)
	}
	if parked.wakeRequested {
		t.Fatal("native timer echo must be parked while a scheduled wakeup is pending")
	}

	unparked := &runnerState{}
	if err := unparked.handleStep("/t/transcript_full.jsonl", "{}", echo, runnerConfig{}); err != nil {
		t.Fatal(err)
	}
	if !unparked.wakeRequested {
		t.Fatal("idle timer echo must drive a background-task wake when no scheduled wakeup is pending")
	}
}
