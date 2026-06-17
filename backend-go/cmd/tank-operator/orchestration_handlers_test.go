package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

func TestHandleCreateOrchestrationApprovesAndDispatchesRoots(t *testing.T) {
	store := &fakeOrchStore{}
	sp := newRecordingSpawner()
	gh := &fakeMCPGitHub{}
	app := &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		sessionScope:      "default",
		mcpGitHub:         gh,
		orchestrationRuns: store,
	}
	app.orchestrations = newOrchestrationEngine(store, sp.spawn)

	req := httptest.NewRequest(http.MethodPost, "/api/orchestrations", strings.NewReader(`{
		"repo_owner": "romaine-life",
		"repo_name": "tank-operator",
		"phases": [
			{"phase_key": "root", "brief": "build the root", "target": "integration"},
			{"phase_key": "after", "brief": "build after root", "depends_on": ["root"], "target": "main"}
		]
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "nelson@example.test"))
	rec := httptest.NewRecorder()

	app.handleCreateOrchestration(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.orch.State != pgstore.OrchestrationRunning {
		t.Fatalf("run state = %q, want running", store.orch.State)
	}
	if got := store.snapshot(phaseID("root")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("root status = %q, want running", got)
	}
	if got := store.snapshot(phaseID("after")).Status; got != pgstore.PhasePending {
		t.Fatalf("after status = %q, want pending", got)
	}
	if sp.count("root") != 1 {
		t.Fatalf("root spawned %d times, want 1", sp.count("root"))
	}
	if len(gh.createBranchCalls) != 1 {
		t.Fatalf("create branch calls = %#v, want 1 call", gh.createBranchCalls)
	}
	if !strings.Contains(gh.createBranchCalls[0], "tank/orchestration/") {
		t.Fatalf("create branch call = %q, want generated integration branch", gh.createBranchCalls[0])
	}
}
