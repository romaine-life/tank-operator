package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// startTestWorkflowRequest builds an authenticated POST to the interactive
// test-workflow endpoint for the testWorkflowApp session (owner / session 77).
func startTestWorkflowRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	return req
}

// TestStartTestWorkflow_SecondTriggerWhileInFlightRefused proves the Slice-5
// double-trigger guard: once a provision is in flight (a non-terminal pending
// record), a second trigger for the same target is refused 409 and does NOT
// launch a second gate run / second glimmung checkout.
func TestStartTestWorkflow_SecondTriggerWhileInFlightRefused(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	// First trigger: accepted, one launch, one pending record.
	rec1 := httptest.NewRecorder()
	app.handleStartTestWorkflow(rec1, startTestWorkflowRequest(t))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first trigger status=%d body=%s, want 202", rec1.Code, rec1.Body.String())
	}
	if len(*launched) != 1 {
		t.Fatalf("first trigger launched %d gate runs, want 1", len(*launched))
	}

	// Second trigger while the first is still 'pending': refused.
	rec2 := httptest.NewRecorder()
	app.handleStartTestWorkflow(rec2, startTestWorkflowRequest(t))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second trigger status=%d body=%s, want 409", rec2.Code, rec2.Body.String())
	}
	if len(*launched) != 1 {
		t.Fatalf("second trigger launched another gate run; launched=%d, want 1", len(*launched))
	}
	if store.registerCalls != 2 {
		t.Fatalf("register calls = %d, want 2 (both triggers reached the atomic guard)", store.registerCalls)
	}
}

// TestStartTestWorkflow_RetriggerAfterTerminalAllowed proves the guard is not a
// permanent lock: once the in-flight provision terminalizes, a fresh trigger for
// the same target is accepted again (the conditional re-arm).
func TestStartTestWorkflow_RetriggerAfterTerminalAllowed(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	rec1 := httptest.NewRecorder()
	app.handleStartTestWorkflow(rec1, startTestWorkflowRequest(t))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first trigger status=%d, want 202", rec1.Code)
	}

	// Terminalize the in-flight record (the gate run finished).
	id := store.idFor(pendingReqForSession77())
	if _, err := store.MarkTerminal(t.Context(), id, pgstore.PendingTestProvisionDone, "done", ""); err != nil {
		t.Fatalf("mark terminal: %v", err)
	}

	rec2 := httptest.NewRecorder()
	app.handleStartTestWorkflow(rec2, startTestWorkflowRequest(t))
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("re-trigger after terminal status=%d body=%s, want 202", rec2.Code, rec2.Body.String())
	}
	if len(*launched) != 2 {
		t.Fatalf("re-trigger launched %d gate runs total, want 2", len(*launched))
	}
}

// TestStartTestWorkflow_ActiveTestStateRefused proves a trigger is refused 409
// when a test environment is already active for the session, before any pending
// record is even written.
func TestStartTestWorkflow_ActiveTestStateRefused(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	record := testWorkflowSessionRecord("romaine-life/tank-operator")
	record.TestState = map[string]any{"active": true}
	app, _, _, launched := testWorkflowApp(t, record, gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	rec := httptest.NewRecorder()
	app.handleStartTestWorkflow(rec, startTestWorkflowRequest(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409 when a test env is already active", rec.Code, rec.Body.String())
	}
	if len(*launched) != 0 {
		t.Fatalf("active-test-state trigger must not launch a gate run; launched=%d", len(*launched))
	}
	if store.registerCalls != 0 {
		t.Fatalf("active-test-state guard must short-circuit before Register; register calls=%d", store.registerCalls)
	}
}

// TestStartTestWorkflow_ConcurrentTriggersLaunchOnce proves a rapid burst of
// concurrent triggers (the double-click race) launches exactly one gate run: the
// atomic Register guard admits one winner; every other caller gets 409.
func TestStartTestWorkflow_ConcurrentTriggersLaunchOnce(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	// Capture launches under a mutex since concurrent handlers may race to the
	// launch hook (only the guard winner should reach it).
	var launchMu sync.Mutex
	launchCount := 0
	app.interactiveTestWorkflowLaunch = func(provisionTestSlotRequest) {
		launchMu.Lock()
		launchCount++
		launchMu.Unlock()
	}

	const n = 8
	var accepted, conflicted int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			<-start
			app.handleStartTestWorkflow(rec, startTestWorkflowRequest(t))
			switch rec.Code {
			case http.StatusAccepted:
				atomic.AddInt32(&accepted, 1)
			case http.StatusConflict:
				atomic.AddInt32(&conflicted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("accepted=%d, want exactly one winner", accepted)
	}
	if conflicted != n-1 {
		t.Fatalf("conflicted=%d, want %d", conflicted, n-1)
	}
	launchMu.Lock()
	defer launchMu.Unlock()
	if launchCount != 1 {
		t.Fatalf("launched %d gate runs under a concurrent burst, want exactly 1", launchCount)
	}
}

// pendingReqForSession77 mirrors the register request the handler builds for the
// testWorkflowApp single-repo session, so a test can recompute the provision_id.
func pendingReqForSession77() pgstore.RegisterPendingTestProvisionRequest {
	return pgstore.RegisterPendingTestProvisionRequest{
		SessionScope: "default",
		SessionID:    "77",
		OwnerEmail:   provisionTestOwner,
		RepoOwner:    "romaine-life",
		RepoName:     "tank-operator",
		Branch:       "tank/session/77/tank-operator",
		Project:      "tank-operator",
		Workflow:     interactiveTestWorkflowLabel,
		Kind:         pgstore.PendingTestProvisionInteractive,
	}
}
