package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

func provBoolPtr(b bool) *bool { return &b }

// provisionFakeGitHub is a focused AppServerMCPGitHub double for the
// deterministic test-slot provisioning gate. It serves a queue of live PR
// states (the last entry sticks once exhausted) so a test can model a PR that
// starts 'watching' and later settles to a verdict, and counts how many times
// the gate resolved live state. Image readiness is no longer a GitHub poll — it
// is the durable ci_image_available row (fakeCIImageAvailable below) — so this
// double carries no image-build surface.
type provisionFakeGitHub struct {
	states       []mcpgithub.PullRequestState
	resolveCalls int
	resolveErr   error
}

func (f *provisionFakeGitHub) nextState() (mcpgithub.PullRequestState, error) {
	if f.resolveErr != nil {
		return mcpgithub.PullRequestState{}, f.resolveErr
	}
	idx := f.resolveCalls
	f.resolveCalls++
	if idx >= len(f.states) {
		if len(f.states) == 0 {
			return mcpgithub.PullRequestState{}, nil
		}
		return f.states[len(f.states)-1], nil
	}
	return f.states[idx], nil
}

func (f *provisionFakeGitHub) ResolvePullRequestState(_ context.Context, _, _, _ string, _ int) (mcpgithub.PullRequestState, error) {
	return f.nextState()
}

func (f *provisionFakeGitHub) ResolveOpenPullRequestState(_ context.Context, _, _, _, _, _ string) (mcpgithub.PullRequestState, error) {
	return f.nextState()
}

func (f *provisionFakeGitHub) ListRepos(context.Context, string) ([]mcpgithub.Repo, error) {
	return nil, nil
}
func (f *provisionFakeGitHub) MarkPRReady(context.Context, string, string, string, int, string) error {
	return nil
}
func (f *provisionFakeGitHub) MergePR(context.Context, string, string, string, int, string, string) (string, error) {
	return "", nil
}
func (f *provisionFakeGitHub) MergePRWithHead(context.Context, string, string, string, int, string, string, string) (string, error) {
	return "", nil
}
func (f *provisionFakeGitHub) CreateBranch(context.Context, string, string, string, string, string) error {
	return nil
}
func (f *provisionFakeGitHub) CreatePullRequest(context.Context, string, string, string, string, string, string, string, bool) (mcpgithub.PullRequest, error) {
	return mcpgithub.PullRequest{}, nil
}

// fakeCIImageAvailable is the controllable ciImageAvailableStore double for the
// gate's durable image-readiness read. It is the event-driven replacement for the
// retired GitHub image-build poll: the gate reads ImageAvailableForCommit, which
// reports whether the ACR `push` webhook has written the ci_image_available row
// for this commit. `available` (nil ⇒ true, the common case) sets the default
// answer; `availableSeq` is consumed one entry per call before falling back to it,
// so a test can model "not yet, then yes" (the row lands while the gate watches).
type fakeCIImageAvailable struct {
	available    *bool
	availableSeq []bool
	calls        int
	err          error
}

func (f *fakeCIImageAvailable) UpsertCIImageAvailable(context.Context, pgstore.CIImageAvailable) error {
	return nil
}

func (f *fakeCIImageAvailable) ImageAvailableForCommit(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	if len(f.availableSeq) > 0 {
		v := f.availableSeq[0]
		f.availableSeq = f.availableSeq[1:]
		return v, nil
	}
	if f.available == nil {
		return true, nil
	}
	return *f.available, nil
}

const provisionTestOwner = "owner@example.test"

// provisionTestApp wires an appServer with the supplied GitHub + glimmung
// doubles, a real Manager over a fake clientset (so SetTestState exercises the
// patch+registry path), and an instant injected sleep so settle-waits never
// block real time. It also installs a default-available ciImageAvailable fake
// (image present) and returns it so a test can model a still-propagating image.
func provisionTestApp(t *testing.T, gh *provisionFakeGitHub, glim *fakeGlimmungClient) (*appServer, *testSessionRegistry, *fakeCIImageAvailable) {
	t.Helper()
	reg := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "77",
		Email:   provisionTestOwner,
		Mode:    sessionmodel.ClaudeGUIMode,
		Scope:   "default",
		PodName: "session-77",
		Visible: true,
		Status:  "Active",
	})
	imageAvailable := &fakeCIImageAvailable{}
	app := &appServer{
		mcpGitHub:        gh,
		glimmung:         glim,
		ciImageAvailable: imageAvailable,
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(activitySessionPod("77", provisionTestOwner)),
			nil,
			sessionmodel.SessionsNamespace,
			reg,
			nil,
			sessions.ManagerOptions{},
		),
		// Inject an instant sleep so the settle-wait loop advances without
		// burning wall-clock; provisionNow stays real (the loop polls the
		// queued states regardless of elapsed time).
		provisionSleepFunc: func(context.Context, time.Duration) error { return nil },
	}
	return app, reg, imageAvailable
}

func provisionByNumberReq() provisionTestSlotRequest {
	return provisionTestSlotRequest{
		OwnerEmail: provisionTestOwner,
		SessionID:  "77",
		Project:    "tank-operator",
		Workflow:   "orchestration-review",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Branch:     "feature-branch",
		PRNumber:   1234,
	}
}

func readyState(headSHA string) mcpgithub.PullRequestState {
	return mcpgithub.PullRequestState{
		CheckState:       "success",
		AllChecksSettled: true,
		Mergeable:        provBoolPtr(true),
		MergeableState:   "clean",
		HeadSHA:          headSHA,
		HTMLURL:          "https://github.com/romaine-life/tank-operator/pull/1234",
	}
}

func TestProvisionTestSlot_ReadyProvisionsAndSetsTestState(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, reg, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictReady || !out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want ready+provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("glimmung calls checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	if glim.deployReq.GitRef != "feature-branch" {
		t.Fatalf("deploy git_ref=%q, want feature-branch", glim.deployReq.GitRef)
	}
	rec, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatalf("session record missing")
	}
	if active, _ := rec.TestState["active"].(bool); !active {
		t.Fatalf("SetTestState did not mark the slot active: %#v", rec.TestState)
	}
	// Fidelity wall: landing a real gated image must clear any live-preview
	// override so unverified scratch can never masquerade as the gated image.
	// The deploy path disables live preview (the in-pod daemon then DELETEs the
	// slot override), and the disable must preserve the freshly-set active flag.
	live, ok := rec.TestState["live_preview"].(map[string]any)
	if !ok {
		t.Fatalf("deploy path did not write live_preview state: %#v", rec.TestState)
	}
	if enabled, _ := live["enabled"].(bool); enabled {
		t.Fatalf("deploy path left live preview enabled: %#v", live)
	}
}

// TestProvisionTestSlot_DeployRefProvisionsWithoutPRGate proves the deploy-by-ref
// escape hatch: a request with DeployRef set provisions the ref straight away
// (no open PR required), never reading PR/CI state. This is what keeps the flow
// from being a dead-end when a session's work has merged / has no obvious branch.
func TestProvisionTestSlot_DeployRefProvisionsWithoutPRGate(t *testing.T) {
	// A GitHub double that errors on any read, so a stray PR-readiness read fails
	// the test loudly instead of silently passing.
	gh := &provisionFakeGitHub{resolveErr: errors.New("deploy-by-ref must not read PR state")}
	glim := &fakeGlimmungClient{}
	app, reg, imageAvailable := provisionTestApp(t, gh, glim)

	req := provisionByNumberReq()
	req.PRNumber = 0
	req.Branch = ""
	req.DeployRef = "main"

	out, err := app.provisionTestSlotForSession(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictRef || !out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want ref+provisioned", out.Verdict, out.Provisioned)
	}
	if gh.resolveCalls != 0 {
		t.Fatalf("deploy-by-ref read PR state %d times; it must not validate a PR", gh.resolveCalls)
	}
	// Deploy-by-ref has no PR/commit to validate, so it must not consult the
	// durable image-availability signal either.
	if imageAvailable.calls != 0 {
		t.Fatalf("deploy-by-ref checked image availability %d times; it must not", imageAvailable.calls)
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("glimmung checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	if glim.deployReq.GitRef != "main" {
		t.Fatalf("deploy git_ref=%q, want main", glim.deployReq.GitRef)
	}
	if glim.deployReq.ImageSource != "chart" {
		t.Fatalf("deploy image_source=%q, want chart (deploy-by-ref uses the chart's pinned image)", glim.deployReq.ImageSource)
	}
	rec, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatalf("session record missing")
	}
	if active, _ := rec.TestState["active"].(bool); !active {
		t.Fatalf("SetTestState did not mark the slot active: %#v", rec.TestState)
	}
}

// TestProvisionTestSlot_DeployFailureReleasesSlot proves a deploy failure does
// not leak the checked-out slot: the gate returns it so the finite pool is not
// silently drained by failed provisions (the exact leak the live smoke test hit).
func TestProvisionTestSlot_DeployFailureReleasesSlot(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{deployErr: errors.New("glimmung deploy-image returned 422")}
	app, _, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err == nil {
		t.Fatalf("expected a deploy error, got nil (out=%+v)", out)
	}
	if out.Provisioned {
		t.Fatalf("a failed deploy must not report provisioned")
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	// The checked-out slot must be returned, not leaked.
	if glim.returnReq.SlotIndex == nil && glim.returnReq.SlotName == nil {
		t.Fatalf("deploy failure must release the checked-out slot (ReturnTestSlot not called)")
	}
	if glim.returnReq.SlotName != nil && *glim.returnReq.SlotName != "tank-operator-slot-1" {
		t.Fatalf("released the wrong slot: %q", *glim.returnReq.SlotName)
	}
}

func TestProvisionTestSlot_FailedRefusesWithoutGlimmung(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{{
		CheckState:       "failure",
		AllChecksSettled: true,
		FailingChecks:    []string{"build", "lint"},
		Mergeable:        provBoolPtr(true),
		MergeableState:   "blocked",
		HeadSHA:          "sha-red",
	}}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictFailed || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want failed+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung should not be called on failed verdict; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if !strings.Contains(out.Detail, "build") || !strings.Contains(out.Detail, "lint") {
		t.Fatalf("failure detail should list failing checks, got %q", out.Detail)
	}
	if err := provisionRefusalError(out); err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("refusal error should surface failing checks, got %v", err)
	}
}

func TestProvisionTestSlot_ConflictRefusesWithoutGlimmung(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{{
		CheckState:       "success",
		AllChecksSettled: true,
		Mergeable:        provBoolPtr(false),
		MergeableState:   "dirty",
		HeadSHA:          "sha-conflict",
	}}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictConflict || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want conflict+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung should not be called on conflict; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if !strings.Contains(strings.ToLower(out.Detail), "rebase") {
		t.Fatalf("conflict detail should mention rebase, got %q", out.Detail)
	}
}

func TestProvisionTestSlot_WatchingThenReadyWaitsThenProvisions(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{
		// Two 'watching' reads (checks pending), then a settled ready read.
		{CheckState: "pending", MergeableState: "unknown", HeadSHA: "sha1"},
		{CheckState: "pending", MergeableState: "clean", Mergeable: provBoolPtr(true), HeadSHA: "sha1"},
		readyState("sha1"),
	}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	slept := 0
	app.provisionSleepFunc = func(context.Context, time.Duration) error {
		slept++
		return nil
	}

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictReady || !out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want ready+provisioned", out.Verdict, out.Provisioned)
	}
	if gh.resolveCalls != 3 {
		t.Fatalf("expected 3 live resolves (2 watching + 1 ready), got %d", gh.resolveCalls)
	}
	if slept != 2 {
		t.Fatalf("expected 2 settle sleeps between the watching reads, got %d", slept)
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("glimmung calls checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
}

func TestProvisionTestSlot_WatchingTimeoutRefuses(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{
		{CheckState: "pending", MergeableState: "unknown", HeadSHA: "sha1"},
	}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	// Drive a deterministic clock that jumps past the settle cap on the second
	// read so the loop times out instead of polling forever.
	app.provisionSettleInterval = 25 * time.Second
	app.provisionSettleTimeout = 1 * time.Minute
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	step := 0
	app.provisionNow = func() time.Time {
		// 0: loop start (deadline = base+1m); subsequent calls advance well past
		// the cap so the next watching check trips the timeout.
		now := base.Add(time.Duration(step) * 2 * time.Minute)
		step++
		return now
	}

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictWatchingTimeout || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want watching_timeout+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung should not be called on timeout; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if !strings.Contains(strings.ToLower(out.Detail), "settle") {
		t.Fatalf("timeout detail should mention settle, got %q", out.Detail)
	}
}

// TestProvisionTestSlot_ReadyButImageNotAvailableWaitsThenProvisions proves the
// durable image-readiness gate: the CI-watch reducer reports `ready` (mergeable +
// checks green) on every poll, but the registry has not yet reported the
// deployable image for the head SHA on the first poll (no ci_image_available row).
// The gate must demote that first `ready` to `watching`, sleep one settle
// interval, and only provision once the ACR push webhook has written the row —
// closing the window where a fast unrelated green check let the gate deploy a
// commit whose CI image did not exist yet.
func TestProvisionTestSlot_ReadyButImageNotAvailableWaitsThenProvisions(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, reg, imageAvailable := provisionTestApp(t, gh, glim)
	// First poll: reducer is green but the durable image row is not present yet.
	// Second poll: the ACR push webhook has recorded the image.
	imageAvailable.availableSeq = []bool{false, true}

	slept := 0
	app.provisionSleepFunc = func(context.Context, time.Duration) error {
		slept++
		return nil
	}

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictReady || !out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want ready+provisioned once the image row lands", out.Verdict, out.Provisioned)
	}
	if slept != 1 {
		t.Fatalf("slept=%d, want 1 settle wait while the image row had not yet landed", slept)
	}
	if imageAvailable.calls != 2 {
		t.Fatalf("image availability checked %d times, want 2 (not-available then available)", imageAvailable.calls)
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("glimmung calls checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	rec, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatalf("session record missing")
	}
	if active, _ := rec.TestState["active"].(bool); !active {
		t.Fatalf("SetTestState did not mark the slot active: %#v", rec.TestState)
	}
}

// TestProvisionTestSlot_ReadyButImageNeverAvailableTimesOut proves the durable
// image-readiness gate is bounded: when the reducer stays `ready` but the
// registry never reports the deployable image for the head SHA (no
// ci_image_available row), the gate keeps watching until the settle cap and
// refuses with watching_timeout — it never provisions a commit whose CI image was
// not produced, and never touches glimmung.
func TestProvisionTestSlot_ReadyButImageNeverAvailableTimesOut(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, imageAvailable := provisionTestApp(t, gh, glim)
	imageAvailable.available = provBoolPtr(false) // the registry never reports the image for this head

	// Deterministic clock that jumps past the settle cap on the first watching
	// check so the demoted-ready loop trips the timeout instead of polling forever.
	app.provisionSettleInterval = 25 * time.Second
	app.provisionSettleTimeout = 1 * time.Minute
	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	step := 0
	app.provisionNow = func() time.Time {
		now := base.Add(time.Duration(step) * 2 * time.Minute)
		step++
		return now
	}

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictWatchingTimeout || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want watching_timeout+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung must not be called when the image never lands; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if imageAvailable.calls == 0 {
		t.Fatalf("expected the gate to query the durable image availability before refusing")
	}
}

// TestProvisionTestSlot_ImageAvailabilityReadErrorFailsClosed proves the gate
// fails closed (verdict=error, no provision) when the durable image-availability
// read errors — a store/DB outage must not be treated as "image present".
func TestProvisionTestSlot_ImageAvailabilityReadErrorFailsClosed(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, imageAvailable := provisionTestApp(t, gh, glim)
	imageAvailable.err = errors.New("postgres unavailable")

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err == nil {
		t.Fatalf("expected an error when the image-availability read fails (verdict=%q)", out.Verdict)
	}
	if out.Verdict != provisionVerdictError || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want error+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung must not be called when the image read fails; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
}

func TestProvisionTestSlot_HeadMovedRefusesWithoutGlimmung(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-new")}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	req := provisionByNumberReq()
	req.ExpectedSHA = "sha-old"

	out, err := app.provisionTestSlotForSession(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictHeadMoved || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want head_moved+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung should not be called when head moved; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if !strings.Contains(strings.ToLower(out.Detail), "redeploy latest") {
		t.Fatalf("head-moved detail should ask to redeploy latest, got %q", out.Detail)
	}
}

func TestProvisionTestSlot_MergedRefusesWithoutGlimmung(t *testing.T) {
	merged := mcpgithub.PullRequestState{HeadSHA: "sha-merged"}
	merged.PR.Merged = true
	merged.PR.MergeCommitSHA = "merge-sha"
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{merged}}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Verdict != provisionVerdictMerged || out.Provisioned {
		t.Fatalf("verdict=%q provisioned=%v, want merged+not-provisioned", out.Verdict, out.Provisioned)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("glimmung should not be called on merged verdict; checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
}

func TestProvisionTestSlot_GitHubReadErrorReturnsError(t *testing.T) {
	gh := &provisionFakeGitHub{resolveErr: errors.New("boom")}
	glim := &fakeGlimmungClient{}
	app, _, _ := provisionTestApp(t, gh, glim)

	out, err := app.provisionTestSlotForSession(context.Background(), provisionByNumberReq())
	if err == nil {
		t.Fatalf("expected error on GitHub read failure")
	}
	if out.Verdict != provisionVerdictError {
		t.Fatalf("verdict=%q, want error", out.Verdict)
	}
	if glim.checkoutCalls != 0 {
		t.Fatalf("glimmung should not be called when validation cannot read state")
	}
}
