package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/glimmung"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

func TestHandleListOrchestrationsReturnsCurrentOwnerRuns(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "root", status: pgstore.PhaseRunning, target: pgstore.PhaseTargetIntegration, spoke: "spoke-root"},
		phaseSpec{key: "after", status: pgstore.PhasePending, target: pgstore.PhaseTargetMain},
	)
	app := &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		orchestrationRuns: store,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orchestrations", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleListOrchestrations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Orchestrations []struct {
			ID         string `json:"id"`
			Repo       string `json:"repo"`
			State      string `json:"state"`
			PhaseCount int    `json:"phase_count"`
		} `json:"orchestrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Orchestrations) != 1 {
		t.Fatalf("orchestrations=%#v, want one", body.Orchestrations)
	}
	got := body.Orchestrations[0]
	if got.ID != "orch-1" || got.Repo != "romaine-life/tank-operator" || got.State != "running" || got.PhaseCount != 2 {
		t.Fatalf("run = %#v, want orch-1 romaine-life/tank-operator running phase_count=2", got)
	}
}

func TestHandleGetOrchestrationReturnsRunWithPhases(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationAwaitingReview,
		phaseSpec{key: "root", status: pgstore.PhaseMerged, target: pgstore.PhaseTargetIntegration, spoke: "spoke-root"},
	)
	app := &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		orchestrationRuns: store,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orchestrations/orch-1", nil)
	req.SetPathValue("orchestration_id", "orch-1")
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleGetOrchestration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Orchestration struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"orchestration"`
		Phases []struct {
			Key            string `json:"key"`
			Status         string `json:"status"`
			SpokeSessionID string `json:"spoke_session_id"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Orchestration.ID != "orch-1" || body.Orchestration.State != "awaiting_review" {
		t.Fatalf("orchestration = %#v", body.Orchestration)
	}
	if len(body.Phases) != 1 || body.Phases[0].Key != "root" || body.Phases[0].Status != "merged" || body.Phases[0].SpokeSessionID != "spoke-root" {
		t.Fatalf("phases = %#v", body.Phases)
	}
}

func TestHandleGetOrchestrationHidesOtherOwnersRun(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "root", status: pgstore.PhaseRunning},
	)
	app := &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		orchestrationRuns: store,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orchestrations/orch-1", nil)
	req.SetPathValue("orchestration_id", "orch-1")
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "nelson@example.test"))
	rec := httptest.NewRecorder()

	app.handleGetOrchestration(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

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

func TestHandleOrchestrationPhaseMergedMergesMainForward(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "main-phase", status: pgstore.PhaseMerged, target: pgstore.PhaseTargetMain, spoke: "spoke-main"},
	)
	store.orch.IntegrationBranch = "tank/orchestration/orch-1/integration"
	gh := &fakeMCPGitHub{mergeCommit: "merge-forward-sha", createPRNumber: 77}
	app := &appServer{orchestrationRuns: store, mcpGitHub: gh}

	if err := app.handleOrchestrationPhaseMerged(context.Background(), store.snapshot(phaseID("main-phase"))); err != nil {
		t.Fatalf("phase merged hook: %v", err)
	}
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("create PR calls = %#v, want one", gh.createPRCalls)
	}
	if !strings.Contains(gh.createPRCalls[0], ":main:"+store.orch.IntegrationBranch) {
		t.Fatalf("create PR call = %q, want main -> integration", gh.createPRCalls[0])
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", gh.mergeCalls)
	}
}

func TestHandleApproveOrchestrationReviewMergesIntegrationToMainAndMarksDone(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationAwaitingReview,
		phaseSpec{key: "a", status: pgstore.PhaseMerged, target: pgstore.PhaseTargetIntegration, spoke: "spoke-a"},
	)
	store.orch.IntegrationBranch = "tank/orchestration/orch-1/integration"
	gh := &fakeMCPGitHub{mergeCommit: "final-merge-sha", createPRNumber: 88}
	app := &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		sessionScope:      "default",
		orchestrationRuns: store,
		mcpGitHub:         gh,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/orchestrations/orch-1/review/approve", nil)
	req.SetPathValue("orchestration_id", "orch-1")
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleApproveOrchestrationReview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.orch.State != pgstore.OrchestrationDone {
		t.Fatalf("run state = %q, want done", store.orch.State)
	}
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("create PR calls = %#v, want one", gh.createPRCalls)
	}
	if !strings.Contains(gh.createPRCalls[0], ":"+store.orch.IntegrationBranch+":main") {
		t.Fatalf("create PR call = %q, want integration -> main", gh.createPRCalls[0])
	}
}

func TestCheckoutAndDeployOrchestrationReviewUsesIntegrationBranch(t *testing.T) {
	slotURL := "https://tank-operator-slot-1.tank.dev.romaine.life/"
	slotIndex := 1
	slotName := "tank-operator-slot-1"
	glim := &fakeGlimmungClient{checkoutResult: glimmung.CheckoutTestSlotResult{
		State: "active", Project: "tank-operator", SlotIndex: &slotIndex, SlotName: &slotName,
		URL: &slotURL, Lease: "lease-1", Usable: true,
	}}
	// The orchestration-review path is now gated by the shared deterministic
	// provisioning helper: a green/mergeable live verdict is required before
	// checkout+deploy run. Provide a ready PR state so the gate greenlights and
	// the integration-branch deploy assertions below still hold.
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	app := &appServer{glimmung: glim, mcpGitHub: gh}
	orch := pgstore.Orchestration{
		OrchestrationID:   "orch-1",
		OwnerEmail:        "owner@example.test",
		RepoOwner:         "romaine-life",
		RepoName:          "tank-operator",
		IntegrationBranch: "tank/orchestration/orch-1/integration",
	}

	checkout, deploy, err := app.checkoutAndDeployOrchestrationReview(context.Background(), orch, "spoke-a")
	if err != nil {
		t.Fatalf("checkout/deploy: %v", err)
	}
	if checkout.Lease != "lease-1" || deploy.Job != "deploy-1" {
		t.Fatalf("checkout/deploy results = %#v %#v", checkout, deploy)
	}
	if glim.checkoutReq.TankSessionID == nil || *glim.checkoutReq.TankSessionID != "spoke-a" {
		t.Fatalf("checkout tank_session_id = %#v, want spoke-a", glim.checkoutReq.TankSessionID)
	}
	if glim.deployReq.GitRef != orch.IntegrationBranch {
		t.Fatalf("deploy git_ref = %q, want %q", glim.deployReq.GitRef, orch.IntegrationBranch)
	}
}
