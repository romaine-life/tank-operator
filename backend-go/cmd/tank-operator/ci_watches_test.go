package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type ciWatchStatusCall struct {
	watchID string
	status  pgstore.CIWatchStatus
}

type ciWatchObservationCall struct {
	req pgstore.UpdateCIWatchObservationRequest
}

type fakeCIWatchStore struct {
	registerCalls []pgstore.RegisterCIWatchRequest
	registerErr   error

	getByPRResult          pgstore.CIWatch
	getByPRErr             error
	updateStatusCalls      []ciWatchStatusCall
	updateObservationCalls []ciWatchObservationCall
	markMergedCalls        []string
}

func (s *fakeCIWatchStore) Register(_ context.Context, req pgstore.RegisterCIWatchRequest) (pgstore.CIWatch, error) {
	if s.registerErr != nil {
		return pgstore.CIWatch{}, s.registerErr
	}
	s.registerCalls = append(s.registerCalls, req)
	return pgstore.CIWatch{
		WatchID:    "ciwatch_test",
		Status:     pgstore.CIWatchWatching,
		SessionID:  req.SessionID,
		OwnerEmail: req.OwnerEmail,
		PROwner:    req.PROwner,
		PRName:     req.PRName,
		PRNumber:   req.PRNumber,
		HeadSHA:    req.HeadSHA,
		PRURL:      req.PRURL,
	}, nil
}

func (s *fakeCIWatchStore) UpdateStatus(_ context.Context, watchID string, status pgstore.CIWatchStatus, _ string) (pgstore.CIWatch, error) {
	s.updateStatusCalls = append(s.updateStatusCalls, ciWatchStatusCall{watchID: watchID, status: status})
	w := s.getByPRResult
	w.WatchID = watchID
	w.Status = status
	return w, nil
}

func (s *fakeCIWatchStore) UpdateObservation(_ context.Context, req pgstore.UpdateCIWatchObservationRequest) (pgstore.CIWatch, error) {
	s.updateObservationCalls = append(s.updateObservationCalls, ciWatchObservationCall{req: req})
	w := s.getByPRResult
	w.WatchID = req.WatchID
	if w.WatchID == "" {
		w.WatchID = "ciwatch_test"
	}
	w.Status = req.Status
	w.HeadSHA = req.HeadSHA
	w.MergeableState = req.MergeableState
	w.CheckState = req.CheckState
	w.Detail = req.Detail
	w.PRURL = req.PRURL
	s.getByPRResult = w
	return w, nil
}

func (s *fakeCIWatchStore) Get(_ context.Context, _ string) (pgstore.CIWatch, error) {
	return s.getByPRResult, s.getByPRErr
}

func (s *fakeCIWatchStore) GetByPR(_ context.Context, _, _ string, _ int) (pgstore.CIWatch, error) {
	return s.getByPRResult, s.getByPRErr
}

func (s *fakeCIWatchStore) GetLatestForSession(_ context.Context, _, _ string) (pgstore.CIWatch, error) {
	return s.getByPRResult, s.getByPRErr
}

func (s *fakeCIWatchStore) MarkMerged(_ context.Context, watchID, _ string) (pgstore.CIWatch, error) {
	s.markMergedCalls = append(s.markMergedCalls, watchID)
	w := s.getByPRResult
	w.WatchID = watchID
	w.Status = pgstore.CIWatchMerged
	return w, nil
}

func (s *fakeCIWatchStore) HasActiveForSession(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func ciWatchTestServer(t *testing.T, store ciWatchStore) *appServer {
	t.Helper()
	app := testTurnsApp(
		t,
		&recordingSessionBus{},
		sdkSessionPod("session-47", "47", "owner@example.test", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	app.verifier = auth.NewVerifier(testJWT(t))
	app.sessionScope = "tank-operator-slot-3"
	app.ciWatches = store
	return app
}

func TestHandleInternalRegisterCIWatchRegistersWithServiceActor(t *testing.T) {
	store := &fakeCIWatchStore{}
	app := ciWatchTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/ci-watches", strings.NewReader(`{
		"pr_owner": "romaine-life",
		"pr_name": "tank-operator",
		"pr_number": 1234,
		"head_sha": "abc123",
		"mergeable_state": "clean",
		"check_state": "pending",
		"detail": "CI in progress",
		"pr_url": "https://github.com/romaine-life/tank-operator/pull/1234"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterCIWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.registerCalls) != 1 {
		t.Fatalf("register calls = %d, want 1", len(store.registerCalls))
	}
	got := store.registerCalls[0]
	// The owner is taken from the service token's actor_email, the session from
	// the path - never the request body.
	if got.OwnerEmail != "owner@example.test" || got.SessionID != "47" {
		t.Fatalf("owner/session = (%q,%q), want (owner@example.test,47)", got.OwnerEmail, got.SessionID)
	}
	if got.PROwner != "romaine-life" || got.PRName != "tank-operator" || got.PRNumber != 1234 || got.HeadSHA != "abc123" {
		t.Fatalf("pr fields = %#v", got)
	}
}

// TestHandleInternalRegisterCIWatchLinksPhasePR proves the phase->PR join: when
// the registering session is an orchestration phase's spoke, the PR coordinates
// it registers are copied onto the phase (so the merged-PR reverse lookup will
// resolve later) and the phase moves to pr_open.
func TestHandleInternalRegisterCIWatchLinksPhasePR(t *testing.T) {
	store := &fakeCIWatchStore{}
	app := ciWatchTestServer(t, store)
	orch := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, spoke: "47"},
	)
	app.orchestrations = newOrchestrationEngine(orch, newRecordingSpawner().spawn)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/ci-watches", strings.NewReader(`{
		"pr_owner": "romaine-life",
		"pr_name": "tank-operator",
		"pr_number": 1234,
		"head_sha": "abc123",
		"pr_url": "https://github.com/romaine-life/tank-operator/pull/1234"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterCIWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := orch.snapshot(phaseID("a"))
	if got.Status != pgstore.PhasePROpen {
		t.Fatalf("phase a status = %q, want pr_open", got.Status)
	}
	if got.PRNumber != 1234 || got.PROwner != "romaine-life" || got.PRName != "tank-operator" {
		t.Fatalf("phase a PR coordinates not stamped: %#v", got)
	}
}

func TestHandleInternalRegisterCIWatchReadyAutoMergesOrchestrationPhase(t *testing.T) {
	mergeable := true
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "ciwatch_test", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 1234,
		HeadSHA: "abc123", PRURL: "https://github.com/romaine-life/tank-operator/pull/1234",
	}}
	app := ciWatchTestServer(t, store)
	orch := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, spoke: "47"},
	)
	app.orchestrations = newOrchestrationEngine(orch, newRecordingSpawner().spawn)
	gh := &fakeMCPGitHub{
		mergeCommit: "merge-sha",
		prState: mcpgithub.PullRequestState{
			Mergeable: &mergeable, MergeableState: "clean", HeadSHA: "abc123",
			CheckState: "success", CIStatus: "succeeded",
		},
	}
	app.mcpGitHub = gh

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/ci-watches", strings.NewReader(`{
		"pr_owner": "romaine-life",
		"pr_name": "tank-operator",
		"pr_number": 1234,
		"head_sha": "abc123",
		"mergeable_state": "clean",
		"check_state": "success",
		"status": "ready",
		"pr_url": "https://github.com/romaine-life/tank-operator/pull/1234"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterCIWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gh.mergeCalls != 1 || gh.mergeWithHeadSHA != "abc123" {
		t.Fatalf("merge calls=%d head=%q, want one guarded merge on abc123", gh.mergeCalls, gh.mergeWithHeadSHA)
	}
	if len(store.markMergedCalls) != 1 {
		t.Fatalf("mark merged calls = %#v, want one", store.markMergedCalls)
	}
	if got := orch.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase status = %q, want merged", got)
	}
}

func TestHandleInternalRegisterCIWatchRejectsNonService(t *testing.T) {
	store := &fakeCIWatchStore{}
	app := ciWatchTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/ci-watches", strings.NewReader(`{"pr_owner":"o","pr_name":"r","pr_number":1}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "user@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterCIWatch(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for role=user", rec.Code)
	}
	if len(store.registerCalls) != 0 {
		t.Fatalf("store was called for a rejected caller")
	}
}

func TestHandleInternalRegisterCIWatchRejectsBadBody(t *testing.T) {
	store := &fakeCIWatchStore{}
	app := ciWatchTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/ci-watches", strings.NewReader(`not json`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterCIWatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for invalid JSON", rec.Code)
	}
	if len(store.registerCalls) != 0 {
		t.Fatalf("store was called on an invalid body")
	}
}
