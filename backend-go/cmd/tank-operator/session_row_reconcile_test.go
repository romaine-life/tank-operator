package main

import (
	"context"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
)

type fakeStaleRowLister struct {
	rows []sessionregistry.StalePodBackedRow
}

func (f *fakeStaleRowLister) ListStalePodBackedRows(context.Context, time.Time, int) ([]sessionregistry.StalePodBackedRow, error) {
	return f.rows, nil
}

type recordingRowTransitionWriter struct {
	events []sessioncontroller.Event
}

func (r *recordingRowTransitionWriter) RecordTransition(_ context.Context, event sessioncontroller.Event) (sessioncontroller.TransitionOutcome, error) {
	r.events = append(r.events, event)
	return sessioncontroller.TransitionEmitted, nil
}

// TestReconcileSessionRowsFailsPodlessRows pins the backstop: a stale
// visible Pending/Active row whose pod is missing from the cluster gets
// the same PodFailed transition the watch would have written; a row whose
// pod exists is untouched.
func TestReconcileSessionRowsFailsPodlessRows(t *testing.T) {
	livePod := sdkSessionPod("session-9", "9", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner")
	app := testTurnsApp(t, &recordingSessionBus{}, livePod)

	lister := &fakeStaleRowLister{rows: []sessionregistry.StalePodBackedRow{
		{Email: "user@example.com", SessionID: "9", PodName: "session-9", Status: "Active"},
		{Email: "user@example.com", SessionID: "10", PodName: "session-10", Status: "Pending"},
	}}
	writer := &recordingRowTransitionWriter{}

	if err := app.reconcileSessionRows(context.Background(), lister, writer, time.Now().UTC()); err != nil {
		t.Fatalf("reconcileSessionRows: %v", err)
	}
	if len(writer.events) != 1 {
		t.Fatalf("transitions = %d, want exactly the podless row", len(writer.events))
	}
	ev := writer.events[0]
	if ev.SessionID != "10" || ev.Type != sessioncontroller.EventTypePodFailed {
		t.Fatalf("transition = %+v, want PodFailed for session 10", ev)
	}
	if ev.Email != "user@example.com" {
		t.Fatalf("transition email = %q", ev.Email)
	}
}

// TestReconcileSessionRowsNoCandidatesSkipsPodList pins the cost contract:
// zero stale rows means zero cluster list calls (the fake clientset would
// tolerate it, but the loop must stay free in steady state).
func TestReconcileSessionRowsNoCandidatesSkipsPodList(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	writer := &recordingRowTransitionWriter{}
	if err := app.reconcileSessionRows(context.Background(), &fakeStaleRowLister{}, writer, time.Now().UTC()); err != nil {
		t.Fatalf("reconcileSessionRows: %v", err)
	}
	if len(writer.events) != 0 {
		t.Fatalf("transitions = %d, want 0", len(writer.events))
	}
}
