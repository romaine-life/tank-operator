package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type fakeMCPGitHub struct {
	mergeCommit       string
	mergeErr          error
	markReadyErr      error
	createBranchErr   error
	createPRErr       error
	prState           mcpgithub.PullRequestState
	prStateErr        error
	resolvePRCalls    int
	mergeCalls        int
	mergeWithHeadSHA  string
	createBranchCalls []string
	createPRCalls     []string
	createPRNumber    int
	createPRURL       string
}

func (f *fakeMCPGitHub) ListRepos(_ context.Context, _ string) ([]mcpgithub.Repo, error) {
	return nil, nil
}

func (f *fakeMCPGitHub) MarkPRReady(_ context.Context, _, _, _ string, _ int) error {
	return f.markReadyErr
}

func (f *fakeMCPGitHub) MergePR(_ context.Context, _, _, _ string, _ int, _ string) (string, error) {
	f.mergeCalls++
	return f.mergeCommit, f.mergeErr
}

func (f *fakeMCPGitHub) MergePRWithHead(_ context.Context, _, _, _ string, _ int, _ string, expectedHeadSHA string) (string, error) {
	f.mergeCalls++
	f.mergeWithHeadSHA = expectedHeadSHA
	return f.mergeCommit, f.mergeErr
}

func (f *fakeMCPGitHub) CreateBranch(_ context.Context, _, owner, name, branch, base string) error {
	f.createBranchCalls = append(f.createBranchCalls, owner+"/"+name+":"+branch+":"+base)
	return f.createBranchErr
}

func (f *fakeMCPGitHub) CreatePullRequest(_ context.Context, _, owner, name, title, head, base, _ string, _ bool) (mcpgithub.PullRequest, error) {
	f.createPRCalls = append(f.createPRCalls, owner+"/"+name+":"+title+":"+head+":"+base)
	if f.createPRErr != nil {
		return mcpgithub.PullRequest{}, f.createPRErr
	}
	number := f.createPRNumber
	if number == 0 {
		number = 99
	}
	url := f.createPRURL
	if url == "" {
		url = "https://github.com/" + owner + "/" + name + "/pull/" + strconv.Itoa(number)
	}
	return mcpgithub.PullRequest{Number: number, HTMLURL: url, State: "open"}, nil
}

func (f *fakeMCPGitHub) ResolvePullRequestState(_ context.Context, _ string, owner, name string, number int) (mcpgithub.PullRequestState, error) {
	f.resolvePRCalls++
	if f.prStateErr != nil {
		return mcpgithub.PullRequestState{}, f.prStateErr
	}
	state := f.prState
	if state.PR.Number == 0 {
		state.PR.Number = number
	}
	if state.HTMLURL == "" {
		state.HTMLURL = "https://github.com/" + owner + "/" + name + "/pull/" + strconv.Itoa(number)
	}
	if state.HeadSHA == "" {
		state.HeadSHA = state.PR.Head.SHA
	}
	if state.CheckState == "" {
		state.CheckState = "pending"
	}
	if state.CIStatus == "" {
		state.CIStatus = "started"
	}
	return state, nil
}

func mergeTestApp(t *testing.T, watches *fakeCIWatchStore, gh *fakeMCPGitHub) *appServer {
	t.Helper()
	app := testTurnsApp(
		t,
		&recordingSessionBus{},
		sdkSessionPod("session-47", "47", "owner@example.test", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	app.sessionScope = "default"
	app.ciWatches = watches
	app.mcpGitHub = gh
	return app
}

func mergeRequest(t *testing.T, auth bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/merge-pr", nil)
	req.SetPathValue("session_id", "47")
	if auth {
		req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "", "nelson@example.test"))
	}
	return req
}

func TestHandleMergeSessionPRMerges(t *testing.T) {
	watches := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", PROwner: "romaine-life", PRName: "tank-operator",
		PRNumber: 1234, Status: pgstore.CIWatchReady,
	}}
	gh := &fakeMCPGitHub{mergeCommit: "deadbeef"}
	app := mergeTestApp(t, watches, gh)
	rec := httptest.NewRecorder()

	app.handleMergeSessionPR(rec, mergeRequest(t, true))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", gh.mergeCalls)
	}
	if len(watches.markMergedCalls) != 1 {
		t.Fatalf("watch not marked merged: %+v", watches.markMergedCalls)
	}
}

func TestHandleMergeSessionPRSurfacesMergeRejection(t *testing.T) {
	watches := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", PROwner: "o", PRName: "r", PRNumber: 1,
		Status: pgstore.CIWatchWatching,
	}}
	gh := &fakeMCPGitHub{mergeErr: errors.New("Pull Request is not mergeable")}
	app := mergeTestApp(t, watches, gh)
	rec := httptest.NewRecorder()

	app.handleMergeSessionPR(rec, mergeRequest(t, true))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (GitHub is the merge gate)", rec.Code)
	}
	if len(watches.markMergedCalls) != 0 {
		t.Fatalf("watch marked merged despite a rejected merge")
	}
}

func TestHandleMergeSessionPRRequiresAuth(t *testing.T) {
	app := mergeTestApp(t, &fakeCIWatchStore{}, &fakeMCPGitHub{})
	rec := httptest.NewRecorder()

	app.handleMergeSessionPR(rec, mergeRequest(t, false))

	if rec.Code == http.StatusOK {
		t.Fatalf("unauthenticated merge succeeded (status=%d)", rec.Code)
	}
}
