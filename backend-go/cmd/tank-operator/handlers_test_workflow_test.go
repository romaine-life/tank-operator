package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// testWorkflowApp wires an appServer for the interactive test-workflow endpoint:
// a real Manager over a fake clientset (so SetTestState exercises the
// patch+registry path), a verifier for owner-scoped auth, an in-memory session
// event store (so test_provision progress records persist), and the supplied
// GitHub + glimmung doubles. The interactive launcher is replaced with a synchronous
// capture that records each launched request, so a handler test asserts the
// resolved coordinates without goroutine races; the gate's behavior is exercised
// directly via runInteractiveTestWorkflow.
func testWorkflowApp(t *testing.T, record sessionmodel.SessionRecord, gh *provisionFakeGitHub, glim *fakeGlimmungClient) (*appServer, *testSessionRegistry, *recordingSessionEventStore, *[]provisionTestSlotRequest) {
	t.Helper()
	reg := newTestSessionRegistry(record)
	events := &recordingSessionEventStore{}
	var launched []provisionTestSlotRequest
	app := &appServer{
		verifier:      auth.NewVerifier(testJWT(t)),
		sessionScope:  record.Scope,
		mcpGitHub:     gh,
		glimmung:      glim,
		sessionEvents: events,
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(activitySessionPod(record.ID, record.Email)),
			nil,
			sessionmodel.SessionsNamespace,
			reg,
			nil,
			sessions.ManagerOptions{},
		),
		provisionSleepFunc: func(context.Context, time.Duration) error { return nil },
		interactiveTestWorkflowLaunch: func(req provisionTestSlotRequest) {
			launched = append(launched, req)
		},
	}
	return app, reg, events, &launched
}

func testWorkflowSessionRecord(repos ...string) sessionmodel.SessionRecord {
	return sessionmodel.SessionRecord{
		ID:      "77",
		Email:   provisionTestOwner,
		Mode:    sessionmodel.ClaudeGUIMode,
		Scope:   "default",
		PodName: "session-77",
		Visible: true,
		Status:  "Active",
		Repos:   repos,
	}
}

func TestStartTestWorkflow_AcceptedAndLaunchesByBranch(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	if len(*launched) != 1 {
		t.Fatalf("expected exactly one launched gate run, got %d", len(*launched))
	}
	got := (*launched)[0]
	if got.RepoOwner != "romaine-life" || got.RepoName != "tank-operator" {
		t.Fatalf("repo coords = %s/%s, want romaine-life/tank-operator", got.RepoOwner, got.RepoName)
	}
	if got.Branch != "tank/session/77/tank-operator" {
		t.Fatalf("branch = %q, want tank/session/77/tank-operator", got.Branch)
	}
	if got.PRNumber != 0 || got.ExpectedSHA != "" {
		t.Fatalf("expected by-branch (no PR pin / no SHA pin); got PR=%d sha=%q", got.PRNumber, got.ExpectedSHA)
	}
	if got.Project != "tank-operator" {
		t.Fatalf("glimmung project = %q, want tank-operator", got.Project)
	}
	if got.Workflow != interactiveTestWorkflowLabel {
		t.Fatalf("workflow label = %q, want %q", got.Workflow, interactiveTestWorkflowLabel)
	}
}

func TestStartTestWorkflow_RequiresAuth(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	// No Authorization header.
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 without auth", rec.Code)
	}
	if len(*launched) != 0 {
		t.Fatalf("unauthenticated request must not launch a gate run; launched=%d", len(*launched))
	}
}

func TestStartTestWorkflow_RejectsOtherOwner(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for a session owned by another user", rec.Code)
	}
	if len(*launched) != 0 {
		t.Fatalf("cross-owner request must not launch a gate run; launched=%d", len(*launched))
	}
}

func TestStartTestWorkflow_MultiRepoAmbiguousRefuses(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t,
		testWorkflowSessionRecord("romaine-life/tank-operator", "romaine-life/glimmung"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409 for ambiguous multi-repo", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "specify") {
		t.Fatalf("ambiguous refusal should ask to specify the repo, got %s", rec.Body.String())
	}
	if len(*launched) != 0 {
		t.Fatalf("ambiguous request must not launch a gate run; launched=%d", len(*launched))
	}
}

func TestStartTestWorkflow_MultiRepoWithOverride(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t,
		testWorkflowSessionRecord("romaine-life/tank-operator", "romaine-life/glimmung"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start",
		strings.NewReader(`{"repo":"romaine-life/glimmung"}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202 with explicit repo override", rec.Code, rec.Body.String())
	}
	if len(*launched) != 1 {
		t.Fatalf("expected one launched gate run, got %d", len(*launched))
	}
	got := (*launched)[0]
	if got.RepoName != "glimmung" || got.Branch != "tank/session/77/glimmung" {
		t.Fatalf("override coords = %s branch %q, want glimmung", got.RepoName, got.Branch)
	}
}

func TestStartTestWorkflow_NoRepoRefuses(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord(), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for a repo-less session", rec.Code)
	}
	if len(*launched) != 0 {
		t.Fatalf("repo-less request must not launch a gate run; launched=%d", len(*launched))
	}
}

// TestRunInteractiveTestWorkflow_ReadyProvisionsAndAnnounces drives the
// background runner directly on a ready verdict: glimmung is checked out +
// deployed, the session test-state is marked active, and the provision thread
// opens ("creating") and closes with a terminal "ready" record carrying the
// test-environment URL.
func TestRunInteractiveTestWorkflow_ReadyProvisionsAndAnnounces(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, reg, events, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	app.runInteractiveTestWorkflow(provisionTestSlotRequest{
		OwnerEmail: provisionTestOwner,
		SessionID:  "77",
		Project:    "tank-operator",
		Workflow:   interactiveTestWorkflowLabel,
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Branch:     "tank/session/77/tank-operator",
	})

	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("glimmung calls checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	if glim.deployReq.GitRef != "tank/session/77/tank-operator" {
		t.Fatalf("deploy git_ref=%q, want the governed branch", glim.deployReq.GitRef)
	}
	rec, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatalf("session record missing")
	}
	if active, _ := rec.TestState["active"].(bool); !active {
		t.Fatalf("ready verdict should mark test-state active: %#v", rec.TestState)
	}
	phases := testProvisionPhases(events)
	if len(phases) == 0 || phases[0] != "creating" {
		t.Fatalf("first provision record should be the 'creating' opener, got %v", phases)
	}
	terminal := latestTestProvision(events)
	if got, _ := terminal["phase"].(string); got != "ready" {
		t.Fatalf("terminal provision phase=%q, want ready (all phases: %v)", got, phases)
	}
	if got, _ := terminal["url"].(string); got == "" {
		t.Fatalf("ready record should carry the test-environment URL, got payload %#v", terminal)
	}
	if got, _ := terminal["text"].(string); !strings.Contains(got, "ready") {
		t.Fatalf("ready text=%q, want it to announce the environment is ready", got)
	}
	// All records share the run id so they thread under one system avatar.
	if !testProvisionSingleRun(events) {
		t.Fatalf("all provision records of one run must share a run_id: %v", testProvisionEvents(events))
	}
}

// TestRunInteractiveTestWorkflow_RefusalSurfacesReason drives a failed verdict:
// glimmung is never touched, the refusal reason is the terminal error record's
// text, and the session test-state stays inactive.
func TestRunInteractiveTestWorkflow_RefusalSurfacesReason(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{{
		CheckState:       "failure",
		AllChecksSettled: true,
		FailingChecks:    []string{"build", "lint"},
		Mergeable:        provBoolPtr(true),
		MergeableState:   "blocked",
		HeadSHA:          "sha-red",
	}}}
	glim := &fakeGlimmungClient{}
	app, reg, events, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	app.runInteractiveTestWorkflow(provisionTestSlotRequest{
		OwnerEmail: provisionTestOwner,
		SessionID:  "77",
		Project:    "tank-operator",
		Workflow:   interactiveTestWorkflowLabel,
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Branch:     "tank/session/77/tank-operator",
	})

	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("refusal must not touch glimmung; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	phases := testProvisionPhases(events)
	if len(phases) == 0 || phases[0] != "creating" {
		t.Fatalf("first provision record should be the 'creating' opener, got %v", phases)
	}
	terminal := latestTestProvision(events)
	if got, _ := terminal["phase"].(string); got != "error" {
		t.Fatalf("refusal terminal phase=%q, want error", got)
	}
	if got, _ := terminal["severity"].(string); got != "error" {
		t.Fatalf("refusal terminal severity=%q, want error", got)
	}
	detail, _ := terminal["text"].(string)
	if !strings.Contains(detail, "build") || !strings.Contains(detail, "lint") {
		t.Fatalf("refusal text should list failing checks, got %q", detail)
	}
	rec, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatalf("session record missing")
	}
	if active, _ := rec.TestState["active"].(bool); active {
		t.Fatalf("refusal must leave test-state inactive, got %#v", rec.TestState)
	}
}

// driveWakeCapture records one backend-owned wake submission so a test can
// assert whether (and with what URL) the drive variant woke the agent, without
// standing up the full sessionBus/pod machinery enqueueSDKTurn requires.
type driveWakeCapture struct {
	req provisionTestSlotRequest
	url string
}

// installDriveWakeCapture replaces the real wake submission with a capture and
// returns a pointer to the accumulating slice.
func installDriveWakeCapture(app *appServer) *[]driveWakeCapture {
	captured := &[]driveWakeCapture{}
	app.testDriveWakeSubmit = func(_ context.Context, req provisionTestSlotRequest, url string) (map[string]any, int, string) {
		*captured = append(*captured, driveWakeCapture{req: req, url: url})
		return map[string]any{"status": "accepted", "turn_id": "turn_testdrive_fake"}, 0, ""
	}
	return captured
}

func driveRequest(drive bool) provisionTestSlotRequest {
	return provisionTestSlotRequest{
		OwnerEmail: provisionTestOwner,
		SessionID:  "77",
		Project:    "tank-operator",
		Workflow:   interactiveTestWorkflowLabel,
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Branch:     "tank/session/77/tank-operator",
		drive:      drive,
	}
}

// TestRunInteractiveTestWorkflow_DriveReadyWakesAgent: the drive variant, on a
// ready provision, submits exactly one backend-owned wake turn carrying the
// running slot's URL. This is the only place the agent re-enters; provisioning
// itself stayed zero-LLM.
func TestRunInteractiveTestWorkflow_DriveReadyWakesAgent(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, events, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	wakes := installDriveWakeCapture(app)

	app.runInteractiveTestWorkflow(driveRequest(true))

	if len(*wakes) != 1 {
		t.Fatalf("drive+ready should wake the agent exactly once, got %d wakes", len(*wakes))
	}
	wake := (*wakes)[0]
	terminalURL, _ := latestTestProvision(events)["url"].(string)
	if wake.url == "" || wake.url != terminalURL {
		t.Fatalf("wake url=%q, want the ready slot URL %q", wake.url, terminalURL)
	}
	if wake.req.Branch != "tank/session/77/tank-operator" {
		t.Fatalf("wake req branch=%q, want the governed branch", wake.req.Branch)
	}
	// The visible provision thread is identical to the plain button: ready terminal.
	if got, _ := latestTestProvision(events)["phase"].(string); got != "ready" {
		t.Fatalf("terminal phase=%q, want ready", got)
	}
}

// TestRunInteractiveTestWorkflow_DriveRefusalDoesNotWake: a refusal (no slot
// came up) never wakes the agent — only the thread's refusal record, identical
// to the plain button.
func TestRunInteractiveTestWorkflow_DriveRefusalDoesNotWake(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{{
		CheckState:       "failure",
		AllChecksSettled: true,
		FailingChecks:    []string{"build"},
		Mergeable:        provBoolPtr(true),
		MergeableState:   "blocked",
		HeadSHA:          "sha-red",
	}}}
	glim := &fakeGlimmungClient{}
	app, _, events, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	wakes := installDriveWakeCapture(app)

	app.runInteractiveTestWorkflow(driveRequest(true))

	if len(*wakes) != 0 {
		t.Fatalf("drive+refusal must NOT wake the agent, got %d wakes", len(*wakes))
	}
	if got, _ := latestTestProvision(events)["phase"].(string); got != "error" {
		t.Fatalf("refusal terminal phase=%q, want error", got)
	}
}

// TestRunInteractiveTestWorkflow_PlainReadyDoesNotWake: the plain "Create test
// slot" button (drive=false) provisions but never wakes — Slice 8 behavior is
// unchanged.
func TestRunInteractiveTestWorkflow_PlainReadyDoesNotWake(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, events, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	wakes := installDriveWakeCapture(app)

	app.runInteractiveTestWorkflow(driveRequest(false))

	if len(*wakes) != 0 {
		t.Fatalf("plain (drive=false) ready must NOT wake the agent, got %d wakes", len(*wakes))
	}
	if got, _ := latestTestProvision(events)["phase"].(string); got != "ready" {
		t.Fatalf("terminal phase=%q, want ready", got)
	}
}

// TestStartTestWorkflow_DriveFlagThreadsToLaunch: the endpoint parses
// {"drive": true} and threads it onto the launched gate request.
func TestStartTestWorkflow_DriveFlagThreadsToLaunch(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start",
		strings.NewReader(`{"drive":true}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	if len(*launched) != 1 {
		t.Fatalf("expected one launched gate run, got %d", len(*launched))
	}
	if !(*launched)[0].drive {
		t.Fatalf("drive flag should thread onto the launched request")
	}
}

// TestStartTestWorkflow_DefaultsDriveFalse: omitting drive (the plain button)
// leaves the launched request non-driving.
func TestStartTestWorkflow_DefaultsDriveFalse(t *testing.T) {
	gh := &provisionFakeGitHub{}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-workflow/start", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "77")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, provisionTestOwner, auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleStartTestWorkflow(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	if len(*launched) != 1 || (*launched)[0].drive {
		t.Fatalf("plain request must launch a non-driving gate run")
	}
}

func TestTestDriveWakeHelpers(t *testing.T) {
	if got := sdkTurnSource("test-slot-drive"); got != "test-slot-drive" {
		t.Fatalf("sdkTurnSource(test-slot-drive) = %q, want test-slot-drive", got)
	}
	// Deterministic + turn-id-shaped so a re-fire collapses under command dedupe.
	seed := "77:tank/session/77/tank-operator:https://slot-1.example"
	n1 := testDriveWakeTurnNonce(seed)
	n2 := testDriveWakeTurnNonce(seed)
	if n1 != n2 {
		t.Fatalf("nonce not deterministic: %q vs %q", n1, n2)
	}
	if !turnIDPattern.MatchString(n1) {
		t.Fatalf("nonce %q does not match turn id pattern", n1)
	}
	// The prompt assumes the slot exists and tells the agent NOT to reserve one.
	prompt := testDriveWakePrompt(driveRequest(true), "https://slot-1.example")
	for _, want := range []string{"https://slot-1.example", "already live", "do NOT reserve", "/test-drive"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("drive prompt missing %q:\n%s", want, prompt)
		}
	}
	if got := testDriveWakeDisplayText("https://slot-1.example"); !strings.Contains(got, "https://slot-1.example") {
		t.Fatalf("display text should mention the slot URL, got %q", got)
	}
}

func TestSessionGlimmungProjectMapping(t *testing.T) {
	if got := sessionGlimmungProject("romaine-life", "glimmung"); got != "glimmung" {
		t.Fatalf("romaine-life repo project=%q, want glimmung", got)
	}
	if got := sessionGlimmungProject("someone-else", "thing"); got != defaultGlimmungProject {
		t.Fatalf("non-romaine project=%q, want %q", got, defaultGlimmungProject)
	}
}

// testProvisionEvents returns the payloads of every persisted
// test_provision.updated event, in emission order.
func testProvisionEvents(events *recordingSessionEventStore) []map[string]any {
	var out []map[string]any
	for _, ev := range events.upserts {
		if t, _ := ev["type"].(string); t != "test_provision.updated" {
			continue
		}
		if payload, ok := ev["payload"].(map[string]any); ok {
			out = append(out, payload)
		}
	}
	return out
}

// testProvisionPhases returns the ordered phase names of the provision thread.
func testProvisionPhases(events *recordingSessionEventStore) []string {
	payloads := testProvisionEvents(events)
	phases := make([]string, 0, len(payloads))
	for _, p := range payloads {
		phase, _ := p["phase"].(string)
		phases = append(phases, phase)
	}
	return phases
}

// latestTestProvision returns the payload of the most-recent provision record.
func latestTestProvision(events *recordingSessionEventStore) map[string]any {
	payloads := testProvisionEvents(events)
	if len(payloads) == 0 {
		return nil
	}
	return payloads[len(payloads)-1]
}

// testProvisionSingleRun reports whether every provision record shares one run_id.
func testProvisionSingleRun(events *recordingSessionEventStore) bool {
	runID := ""
	for _, p := range testProvisionEvents(events) {
		id, _ := p["run_id"].(string)
		if runID == "" {
			runID = id
			continue
		}
		if id != runID {
			return false
		}
	}
	return runID != ""
}
