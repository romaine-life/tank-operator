package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

func provBoolPtr(b bool) *bool { return &b }

// provisionFakeGitHub is a focused AppServerMCPGitHub double for the
// deterministic test-slot provisioning gate. It serves a queue of live PR
// states (the last entry sticks once exhausted) so a test can model a PR that
// starts 'watching' and later settles to a verdict, and counts how many times
// the gate resolved live state.
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
func (f *provisionFakeGitHub) MarkPRReady(context.Context, string, string, string, int) error {
	return nil
}
func (f *provisionFakeGitHub) MergePR(context.Context, string, string, string, int, string) (string, error) {
	return "", nil
}
func (f *provisionFakeGitHub) MergePRWithHead(context.Context, string, string, string, int, string, string) (string, error) {
	return "", nil
}
func (f *provisionFakeGitHub) CreateBranch(context.Context, string, string, string, string, string) error {
	return nil
}
func (f *provisionFakeGitHub) CreatePullRequest(context.Context, string, string, string, string, string, string, string, bool) (mcpgithub.PullRequest, error) {
	return mcpgithub.PullRequest{}, nil
}

const provisionTestOwner = "owner@example.test"

// provisionTestApp wires an appServer with the supplied GitHub + glimmung
// doubles, a real Manager over a fake clientset (so SetTestState exercises the
// patch+registry path), and an instant injected sleep so settle-waits never
// block real time.
func provisionTestApp(t *testing.T, gh *provisionFakeGitHub, glim *fakeGlimmungClient) (*appServer, *testSessionRegistry) {
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
	app := &appServer{
		mcpGitHub: gh,
		glimmung:  glim,
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
	return app, reg
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
	app, reg := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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

func TestProvisionTestSlot_HeadMovedRefusesWithoutGlimmung(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-new")}}
	glim := &fakeGlimmungClient{}
	app, _ := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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
	app, _ := provisionTestApp(t, gh, glim)

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
