package main

import (
	"context"
	"errors"
	"testing"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
)

type fakeReapClaimer struct {
	rows      []sessionregistry.ReapedSession
	err       error
	calls     int
	gotCutoff time.Time
	gotLimit  int
}

func (f *fakeReapClaimer) ClaimIdleForReap(_ context.Context, cutoff time.Time, limit int) ([]sessionregistry.ReapedSession, error) {
	f.calls++
	f.gotCutoff = cutoff
	f.gotLimit = limit
	return f.rows, f.err
}

// TestReapIdleSessionsDeletesClaimedPods pins the executor half of the
// durable reaper: rows the registry claimed (already invisible) get their
// pods deleted through the same Manager.Delete path a user-initiated
// delete uses. The claim half — every guard that keeps live sessions out
// of the candidate set — is pinned by the DSN-gated
// sessionregistry.TestClaimIdleForReap.
func TestReapIdleSessionsDeletesClaimedPods(t *testing.T) {
	pod := sdkSessionPod("session-9", "9", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner")
	app := testTurnsApp(t, &recordingSessionBus{}, pod)
	cutoff := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	claimer := &fakeReapClaimer{rows: []sessionregistry.ReapedSession{{
		Email:     "user@example.com",
		SessionID: "9",
		PodName:   "session-9",
	}}}

	if err := app.reapIdleSessions(context.Background(), claimer, cutoff); err != nil {
		t.Fatalf("reapIdleSessions: %v", err)
	}
	if claimer.calls != 1 || !claimer.gotCutoff.Equal(cutoff) || claimer.gotLimit != idleReapBatchLimit {
		t.Fatalf("claimer called %d times cutoff %s limit %d", claimer.calls, claimer.gotCutoff, claimer.gotLimit)
	}
	if _, err := app.k8s.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), "session-9", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("claimed session's pod still exists (err=%v)", err)
	}
}

// TestReapIdleSessionsSurfacesClaimErrors pins that a registry failure is
// returned (logged + retried next tick by the loop) instead of swallowed.
func TestReapIdleSessionsSurfacesClaimErrors(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	claimer := &fakeReapClaimer{err: errors.New("pg down")}
	if err := app.reapIdleSessions(context.Background(), claimer, time.Now()); err == nil {
		t.Fatalf("claim error swallowed")
	}
}
