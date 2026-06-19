package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// handleGetTestSlotStatus backs the dedicated test-slot page. It is a read-only,
// owner-scoped snapshot of everything that page needs to render the controls and
// the PR-readiness block without contradicting the durable system:
//
//   - the resolved governed-PR coordinates (repo + session branch), or a soft
//     repo_error when a multi-repo session needs disambiguation (the page renders
//     a picker instead of failing);
//   - the durable last-known PR readiness from the session_ci_watches row
//     (status + mergeable_state + check_state, "as of" last_event_at) — cheap, no
//     GitHub call, event-driven-fresh;
//   - the durable last/in-flight interactive provision from pending_test_provisions;
//   - the session's test_state (active slot + URL).
//
// With ?refresh=1 it additionally runs the SAME one-shot live read + classifier
// the provision gate uses (resolveProvisionState → classifyCIWatchState) with no
// durable row and no side effects, so the page can show an authoritative current
// verdict on demand. The "Create test slot" click re-runs the full gate against
// live GitHub regardless, so the cheap durable display can never cause a wrong
// provision — it just sets expectations.
func (s *appServer) handleGetTestSlotStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	owner := user.OwnerEmail()
	info, err := s.mgr.GetRegisteredByOwner(r.Context(), owner, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
			recordTestSlotStatus("not_found")
			writeError(w, http.StatusNotFound, "session not found")
		default:
			recordTestSlotStatus("error")
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := testSlotStatusResponse{
		Repos:     trimmedRepoSlugs(info.Repos),
		TestState: info.TestState,
	}

	repoOverride := strings.TrimSpace(r.URL.Query().Get("repo"))
	req, herr := s.resolveTestWorkflowTarget(r.Context(), owner, sessionID, info, repoOverride)
	if herr != nil {
		// Not a hard failure for this read surface: the page renders the
		// repo_error (e.g. "specify which repo to test") and, for a multi-repo
		// session, lets the user pick. A definite verdict is unavailable until a
		// repo is chosen.
		resp.RepoError = herr.msg
		if herr.status == http.StatusConflict {
			recordTestSlotStatus("ambiguous_repo")
		} else {
			recordTestSlotStatus("no_repo")
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Repo = &testSlotRepoView{
		Owner:  req.RepoOwner,
		Name:   req.RepoName,
		Slug:   req.RepoOwner + "/" + req.RepoName,
		Branch: req.Branch,
	}

	if s.ciWatches != nil {
		if watch, err := s.ciWatches.GetLatestForSession(r.Context(), s.sessionScope, sessionID); err == nil {
			resp.Watch = testSlotWatchViewFrom(watch)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			recordTestSlotStatus("error")
			writeError(w, http.StatusInternalServerError, "read PR readiness: "+err.Error())
			return
		}
	}

	if s.pendingTestProvisions != nil {
		pid := pgstore.PendingTestProvisionID(sessionID, req.RepoOwner, req.RepoName, req.Branch, pgstore.PendingTestProvisionInteractive)
		if prov, err := s.pendingTestProvisions.Get(r.Context(), pid); err == nil {
			resp.Provision = testSlotProvisionViewFrom(prov)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			recordTestSlotStatus("error")
			writeError(w, http.StatusInternalServerError, "read provision status: "+err.Error())
			return
		}
	}

	result := "durable"
	if refreshRequested(r) && s.mcpGitHub != nil {
		preflight := s.testSlotPreflight(r.Context(), req)
		resp.Preflight = &preflight
		result = "live"
	}

	recordTestSlotStatus(result)
	writeJSON(w, http.StatusOK, resp)
}

func refreshRequested(r *http.Request) bool {
	q := r.URL.Query()
	switch {
	case q.Get("refresh") == "1", q.Get("refresh") == "true":
		return true
	case q.Get("live") == "1", q.Get("live") == "true":
		return true
	default:
		return false
	}
}

// testSlotPreflight runs the gate's validate step read-only: the same one-shot
// live read + classifyCIWatchState, with no settle-wait, no durable row, and no
// provisioning. It maps the result to the page's bounded verdict. A branch with
// no open PR is a first-class "no_pr" verdict (not an error), so the page can say
// "publish a PR to test" rather than showing a failure.
func (s *appServer) testSlotPreflight(ctx context.Context, req provisionTestSlotRequest) testSlotPreflightView {
	state, err := s.resolveProvisionState(ctx, req)
	if err != nil {
		if errors.Is(err, mcpgithub.ErrNoOpenPR) {
			return testSlotPreflightView{
				Verdict: "no_pr",
				Detail:  "No open PR for " + req.Branch + " yet.",
			}
		}
		return testSlotPreflightView{Verdict: "error", Detail: err.Error()}
	}
	// Transient identity for the classifier, mirroring the gate's in-memory watch.
	watch := pgstore.CIWatch{
		OwnerEmail: req.OwnerEmail,
		PROwner:    req.RepoOwner,
		PRName:     req.RepoName,
		PRNumber:   req.PRNumber,
	}
	res := classifyCIWatchState(watch, state)
	view := testSlotPreflightView{
		MergeableState: res.MergeableState,
		CheckState:     res.CheckState,
		FailingChecks:  res.FailingChecks,
		PRURL:          res.PRURL,
		HeadSHA:        res.HeadSHA,
		HasOpenPR:      true,
	}
	switch res.Status {
	case pgstore.CIWatchReady:
		view.Verdict = "ready"
		view.Detail = "PR is green and mergeable."
	case pgstore.CIWatchFailed:
		view.Verdict = "failed"
		view.Detail = "CI is failing on this PR."
	case pgstore.CIWatchConflict:
		view.Verdict = "conflict"
		view.Detail = strings.TrimSpace(res.Detail)
		if view.Detail == "" {
			view.Detail = "PR needs a rebase onto main."
		}
	case pgstore.CIWatchMerged:
		view.Verdict = "merged"
		view.HasOpenPR = false
		view.Detail = "PR is already merged."
	default:
		view.Verdict = "watching"
		view.Detail = "Checks are still running."
	}
	return view
}

// testSlotStatusResponse is the read-only page snapshot. Every field is derived
// from durable state (or, for Preflight, a side-effect-free live read).
type testSlotStatusResponse struct {
	Repo      *testSlotRepoView      `json:"repo"`
	RepoError string                 `json:"repo_error"`
	Repos     []string               `json:"repos"`
	Watch     *testSlotWatchView     `json:"watch"`
	Provision *testSlotProvisionView `json:"provision"`
	TestState map[string]any         `json:"test_state"`
	Preflight *testSlotPreflightView `json:"preflight"`
}

type testSlotRepoView struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Branch string `json:"branch"`
}

type testSlotWatchView struct {
	Status         string  `json:"status"`
	MergeableState string  `json:"mergeable_state"`
	CheckState     string  `json:"check_state"`
	Detail         string  `json:"detail"`
	PRURL          string  `json:"pr_url"`
	PRNumber       int     `json:"pr_number"`
	HeadSHA        string  `json:"head_sha"`
	LastEventAt    *string `json:"last_event_at"`
	// HasOpenPR is derived from Status: a watch in a live state (watching/ready/
	// failed/conflict) implies an open PR; merged/superseded/cancelled does not.
	HasOpenPR bool `json:"has_open_pr"`
}

type testSlotProvisionView struct {
	Status      string  `json:"status"`
	Detail      string  `json:"detail"`
	HeadSHA     string  `json:"head_sha"`
	StartedAt   string  `json:"started_at"`
	LastEventAt *string `json:"last_event_at"`
}

type testSlotPreflightView struct {
	Verdict        string   `json:"verdict"`
	MergeableState string   `json:"mergeable_state"`
	CheckState     string   `json:"check_state"`
	FailingChecks  []string `json:"failing_checks"`
	PRURL          string   `json:"pr_url"`
	HeadSHA        string   `json:"head_sha"`
	Detail         string   `json:"detail"`
	HasOpenPR      bool     `json:"has_open_pr"`
}

func testSlotWatchViewFrom(watch pgstore.CIWatch) *testSlotWatchView {
	status := string(watch.Status)
	return &testSlotWatchView{
		Status:         status,
		MergeableState: strings.TrimSpace(watch.MergeableState),
		CheckState:     strings.TrimSpace(watch.CheckState),
		Detail:         strings.TrimSpace(watch.Detail),
		PRURL:          strings.TrimSpace(watch.PRURL),
		PRNumber:       watch.PRNumber,
		HeadSHA:        strings.TrimSpace(watch.HeadSHA),
		LastEventAt:    rfc3339Ptr(watch.LastEventAt),
		HasOpenPR:      ciWatchStatusImpliesOpenPR(watch.Status),
	}
}

func testSlotProvisionViewFrom(prov pgstore.PendingTestProvision) *testSlotProvisionView {
	startedAt := ""
	if !prov.StartedAt.IsZero() {
		startedAt = prov.StartedAt.UTC().Format(time.RFC3339)
	}
	return &testSlotProvisionView{
		Status:      string(prov.Status),
		Detail:      strings.TrimSpace(prov.Detail),
		HeadSHA:     strings.TrimSpace(prov.HeadSHA),
		StartedAt:   startedAt,
		LastEventAt: rfc3339Ptr(prov.LastEventAt),
	}
}

// ciWatchStatusImpliesOpenPR reports whether a durable watch status means there
// is still an open PR to provision against. merged/superseded/cancelled (and any
// unknown) do not.
func ciWatchStatusImpliesOpenPR(status pgstore.CIWatchStatus) bool {
	switch status {
	case pgstore.CIWatchWatching, pgstore.CIWatchReady, pgstore.CIWatchFailed, pgstore.CIWatchConflict:
		return true
	default:
		return false
	}
}

func trimmedRepoSlugs(repos []string) []string {
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		if trimmed := strings.TrimSpace(repo); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
