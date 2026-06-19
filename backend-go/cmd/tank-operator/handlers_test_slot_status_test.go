package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// getTestSlotStatus drives handleGetTestSlotStatus through the shared
// test-workflow app harness (ciWatches / pendingTestProvisions are left nil, so
// the durable snapshot is empty and the handler exercises its nil guards) and
// returns the decoded response.
func getTestSlotStatus(t *testing.T, app *appServer, owner, query string) (*httptest.ResponseRecorder, testSlotStatusResponse) {
	t.Helper()
	url := "/api/sessions/77/test-slot" + query
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("session_id", "77")
	if owner != "" {
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, owner, auth.RoleUser))
	}
	rec := httptest.NewRecorder()
	app.handleGetTestSlotStatus(rec, req)
	var resp testSlotStatusResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, resp
}

func TestGetTestSlotStatus_RequiresAuth(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	rec, _ := getTestSlotStatus(t, app, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 without auth", rec.Code)
	}
}

func TestGetTestSlotStatus_RejectsOtherOwner(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	rec, _ := getTestSlotStatus(t, app, otherUser, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for a session owned by another user", rec.Code)
	}
}

func TestGetTestSlotStatus_DurableSnapshotResolvesCoordinates(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	rec, resp := getTestSlotStatus(t, app, provisionTestOwner, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if resp.Repo == nil {
		t.Fatalf("expected resolved repo, got nil (repo_error=%q)", resp.RepoError)
	}
	if resp.Repo.Slug != "romaine-life/tank-operator" {
		t.Fatalf("repo slug = %q, want romaine-life/tank-operator", resp.Repo.Slug)
	}
	if resp.Repo.Branch != "tank/session/77/tank-operator" {
		t.Fatalf("branch = %q, want tank/session/77/tank-operator", resp.Repo.Branch)
	}
	// Without ?refresh=1 the handler does no live read, so no preflight is
	// attached — the page renders the (here empty) durable snapshot only.
	if resp.Preflight != nil {
		t.Fatalf("expected no preflight without refresh, got %+v", resp.Preflight)
	}
}

func TestGetTestSlotStatus_RefreshReadyPreflight(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, &fakeGlimmungClient{})
	rec, resp := getTestSlotStatus(t, app, provisionTestOwner, "?refresh=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if resp.Preflight == nil {
		t.Fatalf("expected a live preflight on ?refresh=1, got nil")
	}
	if resp.Preflight.Verdict != "ready" {
		t.Fatalf("preflight verdict = %q, want ready", resp.Preflight.Verdict)
	}
	if !resp.Preflight.HasOpenPR {
		t.Fatalf("ready verdict must report has_open_pr=true")
	}
}

// TestGetTestSlotStatus_RefreshDetectsMergedFromWatchPR proves the refresh reads
// the durable watch's PR BY NUMBER, so a PR that has actually merged is reported
// `merged` even when its watch row is stuck at `ready` (the merge webhook missed
// it). Resolving the open PR by branch would instead report `no_pr` and hide the
// merge — the page would never show purple "Merged".
func TestGetTestSlotStatus_RefreshDetectsMergedFromWatchPR(t *testing.T) {
	merged := mcpgithub.PullRequestState{
		PR: mcpgithub.PullRequestDetail{
			Merged: true, Number: 100,
			HTMLURL: "https://github.com/romaine-life/tank-operator/pull/100",
		},
		HeadSHA: "h1",
	}
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{merged}}
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, &fakeGlimmungClient{})
	// Stuck-'ready' watch whose PR is in fact merged.
	app.ciWatches = &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "77", PROwner: "romaine-life", PRName: "tank-operator",
		PRNumber: 100, Status: pgstore.CIWatchReady,
	}}

	rec, resp := getTestSlotStatus(t, app, provisionTestOwner, "?refresh=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if resp.Preflight == nil {
		t.Fatalf("expected a live preflight on ?refresh=1")
	}
	if resp.Preflight.Verdict != "merged" {
		t.Fatalf("preflight verdict = %q, want merged (read the watch's PR #100 by number)", resp.Preflight.Verdict)
	}
	if resp.Preflight.HasOpenPR {
		t.Fatalf("merged verdict must report has_open_pr=false")
	}
}

func TestGetTestSlotStatus_RefreshNoOpenPR(t *testing.T) {
	// A branch with no open PR is a first-class no_pr verdict, not an error, so
	// the page can say "publish a PR to test" and grey out Create.
	gh := &provisionFakeGitHub{resolveErr: mcpgithub.ErrNoOpenPR}
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, &fakeGlimmungClient{})
	rec, resp := getTestSlotStatus(t, app, provisionTestOwner, "?refresh=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if resp.Preflight == nil {
		t.Fatalf("expected a preflight, got nil")
	}
	if resp.Preflight.Verdict != "no_pr" {
		t.Fatalf("preflight verdict = %q, want no_pr", resp.Preflight.Verdict)
	}
	if resp.Preflight.HasOpenPR {
		t.Fatalf("no_pr verdict must report has_open_pr=false")
	}
}

func TestGetTestSlotStatus_MultiRepoAmbiguousSoftError(t *testing.T) {
	// A multi-repo session with no override can't resolve a single target; the
	// read surface returns 200 with a repo_error (the page renders the message /
	// a picker) rather than failing the whole page.
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator", "romaine-life/glimmung"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	rec, resp := getTestSlotStatus(t, app, provisionTestOwner, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 with soft repo_error", rec.Code, rec.Body.String())
	}
	if resp.Repo != nil {
		t.Fatalf("expected nil repo for an ambiguous multi-repo session, got %+v", resp.Repo)
	}
	if resp.RepoError == "" {
		t.Fatalf("expected a repo_error explaining the ambiguity")
	}
	if len(resp.Repos) != 2 {
		t.Fatalf("expected both repos echoed for a picker, got %v", resp.Repos)
	}
}
