package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	staleWatching          []pgstore.CIWatch
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
	current := s.getByPRResult
	// Emulate the store's conditional write: only a row still in 'watching' (on
	// the head the observation was based on) can be updated.
	if current.Status != "" && current.Status != pgstore.CIWatchWatching {
		return pgstore.CIWatch{}, pgstore.ErrCIWatchObservationStale
	}
	if req.ExpectedHeadSHA != "" && strings.TrimSpace(current.HeadSHA) != "" &&
		strings.TrimSpace(current.HeadSHA) != strings.TrimSpace(req.ExpectedHeadSHA) {
		return pgstore.CIWatch{}, pgstore.ErrCIWatchObservationStale
	}
	w := current
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

func (s *fakeCIWatchStore) ListStaleWatching(_ context.Context, _ time.Duration, _ int) ([]pgstore.CIWatch, error) {
	return s.staleWatching, nil
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
			CheckState: "success", CIStatus: "succeeded", AllChecksSettled: true,
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

func TestHandleInternalRegisterPRReadinessResolvesBranchInBackend(t *testing.T) {
	mergeable := true
	store := &fakeCIWatchStore{}
	app := ciWatchTestServer(t, store)
	gh := &fakeMCPGitHub{prState: mcpgithub.PullRequestState{
		PR:             mcpgithub.PullRequestDetail{Number: 1234},
		Mergeable:      &mergeable,
		MergeableState: "clean",
		HeadSHA:        "abc123",
		CheckState:     "success",
		CIStatus:       "succeeded",
		HTMLURL:        "https://github.com/romaine-life/tank-operator/pull/1234",
	}}
	app.mcpGitHub = gh

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/pr-readiness", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"branch": "tank/session/47/tank-operator",
		"expected_head_sha": "abc123"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalRegisterPRReadiness(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gh.resolveOpenPRCalls != 1 || gh.resolvePRCalls != 0 {
		t.Fatalf("resolve calls = open %d by-number %d, want open 1 by-number 0", gh.resolveOpenPRCalls, gh.resolvePRCalls)
	}
	if len(store.registerCalls) != 1 {
		t.Fatalf("register calls = %d, want 1", len(store.registerCalls))
	}
	got := store.registerCalls[0]
	if got.PROwner != "romaine-life" || got.PRName != "tank-operator" || got.PRNumber != 1234 || got.HeadSHA != "abc123" {
		t.Fatalf("registered readiness = %#v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "ready" || body["pr_number"] != float64(1234) {
		t.Fatalf("body = %#v", body)
	}
}

func TestApplyResolvedCIWatchStateSkipsSideEffectsOnLostRace(t *testing.T) {
	// We read a 'watching' row and computed a terminal result, but a concurrent
	// reconcile already moved the row out of 'watching'. The conditional write
	// matches no row, so we must not fire side effects (no second wake / no
	// re-ready) and must not surface an error.
	mergeable := true
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 1234,
		HeadSHA: "abc", Status: pgstore.CIWatchFailed,
	}}
	app := ciWatchTestServer(t, store)

	read := pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 1234,
		HeadSHA: "abc", Status: pgstore.CIWatchWatching,
	}
	state := mcpgithub.PullRequestState{
		Mergeable: &mergeable, MergeableState: "clean", HeadSHA: "abc",
		CheckState: "success", CIStatus: "succeeded",
	}
	result, err := app.applyResolvedCIWatchState(context.Background(), read, state, ciWatchReconcileWebhook, 0)
	if err != nil {
		t.Fatalf("lost-race observation should not error, got %v", err)
	}
	if result.Status != pgstore.CIWatchReady {
		t.Fatalf("classify result = %q, want ready", result.Status)
	}
	if len(store.updateStatusCalls) != 0 {
		t.Fatalf("side effects fired on a lost race: updateStatus=%+v", store.updateStatusCalls)
	}
	if store.getByPRResult.Status != pgstore.CIWatchFailed {
		t.Fatalf("losing observation clobbered the row to %q, want it left failed", store.getByPRResult.Status)
	}
}

func TestReconcileStaleCIWatchesReDrivesStuckWatch(t *testing.T) {
	// A 'watching' watch went stale (its deciding webhook was dropped). The
	// durable backstop must re-drive it with a fresh live read instead of
	// leaving the session stranded asleep.
	mergeable := true
	stale := pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 1234,
		HeadSHA: "abc", Status: pgstore.CIWatchWatching,
	}
	store := &fakeCIWatchStore{staleWatching: []pgstore.CIWatch{stale}, getByPRResult: stale}
	app := ciWatchTestServer(t, store)
	app.mcpGitHub = &fakeMCPGitHub{prState: mcpgithub.PullRequestState{
		Mergeable: &mergeable, MergeableState: "clean", HeadSHA: "abc",
		CheckState: "success", CIStatus: "succeeded", AllChecksSettled: true,
	}}
	if err := app.reconcileStaleCIWatches(context.Background(), time.Minute); err != nil {
		t.Fatalf("backstop pass: %v", err)
	}
	if len(store.updateObservationCalls) != 1 {
		t.Fatalf("backstop did not re-drive the stale watch: %+v", store.updateObservationCalls)
	}
	if store.updateObservationCalls[0].req.Status != pgstore.CIWatchReady {
		t.Fatalf("re-drive resolved to %q, want ready", store.updateObservationCalls[0].req.Status)
	}
}

func TestApplyResolvedCIWatchStateAlertsOnOutOfBandHead(t *testing.T) {
	// The watch is pinned to h1, but GitHub's live head is a foreign h2 with no
	// governed publish in the ledger: supersede + user-facing alert, never follow
	// or wake the agent.
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", SessionScope: "tank-operator-slot-3",
		OwnerEmail: "owner@example.test", PROwner: fakePROwner, PRName: fakePRName,
		PRNumber: 1234, HeadSHA: "h1", Status: pgstore.CIWatchWatching,
	}}
	app := ciWatchTestServer(t, store)
	app.controlActions = &fakeControlActionStore{listRows: []pgstore.ControlActionEvent{
		{Action: "github.commit.push", Status: "succeeded", ResultSHA: "h1"},
	}}
	read := store.getByPRResult
	state := mcpgithub.PullRequestState{
		MergeableState: "clean", HeadSHA: "h2",
		CheckState: "success", CIStatus: "succeeded", AllChecksSettled: true,
	}
	if _, err := app.applyResolvedCIWatchState(context.Background(), read, state, ciWatchReconcileWebhook, 0); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(store.updateObservationCalls) != 1 || store.updateObservationCalls[0].req.Status != pgstore.CIWatchSuperseded {
		t.Fatalf("out-of-band head should supersede, got %+v", store.updateObservationCalls)
	}
	if len(store.updateStatusCalls) != 0 {
		t.Fatalf("out-of-band head must not wake/auto-merge: %+v", store.updateStatusCalls)
	}
}

func TestAutoMergeWithheldUntilChecksConfirmedSettled(t *testing.T) {
	// GitHub reports clean and Tank classifies Ready, but the confirm read shows
	// the checks are not all settled yet (the partial/transient-clean window).
	// Auto-merge must withhold and un-latch to 'watching' for retry, not merge.
	mergeable := true
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "ciwatch_test", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 1234, HeadSHA: "abc123",
	}}
	app := ciWatchTestServer(t, store)
	orch := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, spoke: "47"},
	)
	app.orchestrations = newOrchestrationEngine(orch, newRecordingSpawner().spawn)
	gh := &fakeMCPGitHub{mergeCommit: "merge-sha", prState: mcpgithub.PullRequestState{
		Mergeable: &mergeable, MergeableState: "clean", HeadSHA: "abc123",
		CheckState: "success", CIStatus: "succeeded", AllChecksSettled: false,
	}}
	app.mcpGitHub = gh

	watch := store.getByPRResult
	watch.Status = pgstore.CIWatchWatching
	app.handleGreenCIWatch(context.Background(), watch, "ready")

	if gh.mergeCalls != 0 {
		t.Fatalf("auto-merge should withhold on unconfirmed settle, mergeCalls=%d", gh.mergeCalls)
	}
	if len(store.updateStatusCalls) != 1 || store.updateStatusCalls[0].status != pgstore.CIWatchWatching {
		t.Fatalf("withheld auto-merge should un-latch to watching, got %+v", store.updateStatusCalls)
	}
}

// prReadyUpserts filters a recording event store for the durable
// pr_ready.notified user pings.
func prReadyUpserts(t *testing.T, app *appServer) []map[string]any {
	t.Helper()
	es, ok := app.sessionEvents.(*recordingSessionEventStore)
	if !ok {
		t.Fatalf("sessionEvents is %T, want *recordingSessionEventStore", app.sessionEvents)
	}
	var out []map[string]any
	for _, e := range es.upserts {
		if typ, _ := e["type"].(string); typ == "pr_ready.notified" {
			out = append(out, e)
		}
	}
	return out
}

// TestHandleGreenCIWatchPingsUserOnReadyTransition pins the core slice
// behavior: on the non-orchestration watching -> ready edge, the user is pinged
// with a durable pr_ready.notified system notice, and the AGENT is never woken
// (no submit_turn command on the bus).
func TestHandleGreenCIWatchPingsUserOnReadyTransition(t *testing.T) {
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", SessionScope: "tank-operator-slot-3",
		OwnerEmail: "owner@example.test", PROwner: fakePROwner, PRName: fakePRName,
		PRNumber: 1234, HeadSHA: "h1", PRURL: "https://github.com/o/r/pull/1234",
		Status: pgstore.CIWatchWatching,
	}}
	app := ciWatchTestServer(t, store)

	watch := store.getByPRResult
	app.handleGreenCIWatch(context.Background(), watch, "All required checks passed.")

	pings := prReadyUpserts(t, app)
	if len(pings) != 1 {
		t.Fatalf("pr_ready.notified pings = %d, want 1", len(pings))
	}
	payload, _ := pings[0]["payload"].(map[string]any)
	if got, _ := payload["pr_url"].(string); got != "https://github.com/o/r/pull/1234" {
		t.Fatalf("ping pr_url = %q, want the watch PR URL", got)
	}
	if got, _ := payload["head_sha"].(string); got != "h1" {
		t.Fatalf("ping head_sha = %q, want h1", got)
	}
	// The agent must NEVER be woken on ready: no submit_turn command published.
	bus, ok := app.sessionBus.(*recordingSessionBus)
	if !ok {
		t.Fatalf("sessionBus is %T, want *recordingSessionBus", app.sessionBus)
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0 (agent must never be woken on ready): %+v", len(bus.commands), bus.commands)
	}
}

// TestHandleGreenCIWatchDoesNotRepingOnReentry pins idempotency: a re-entry on
// an ALREADY-ready watch (webhook + reconcile double-drive) does not re-ping the
// user.
func TestHandleGreenCIWatchDoesNotRepingOnReentry(t *testing.T) {
	store := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", SessionScope: "tank-operator-slot-3",
		OwnerEmail: "owner@example.test", PROwner: fakePROwner, PRName: fakePRName,
		PRNumber: 1234, HeadSHA: "h1", PRURL: "https://github.com/o/r/pull/1234",
		Status: pgstore.CIWatchReady,
	}}
	app := ciWatchTestServer(t, store)

	watch := store.getByPRResult // already ready
	app.handleGreenCIWatch(context.Background(), watch, "All required checks passed.")

	if pings := prReadyUpserts(t, app); len(pings) != 0 {
		t.Fatalf("pr_ready.notified pings on re-entry = %d, want 0 (idempotent on transition)", len(pings))
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
