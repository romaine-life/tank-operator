package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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

	// All the branches/PRs the agent has worked on this session (newest first).
	// These are what the page lists so the user can pick which to provision.
	var watches []pgstore.CIWatch
	if s.ciWatches != nil {
		var err error
		watches, err = s.ciWatches.ListForSession(r.Context(), s.sessionScope, sessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			recordTestSlotStatus("error")
			writeError(w, http.StatusInternalServerError, "read PR readiness: "+err.Error())
			return
		}
		for i := range watches {
			if i == 0 {
				// Newest watch is the default selection (the prior single-PR behavior).
				resp.Watch = testSlotWatchViewFrom(watches[i])
			}
			resp.PRs = append(resp.PRs, testSlotPRViewFrom(watches[i]))
		}
	}

	// Deployable options the page offers: every open PR the session worked on
	// plus `main` (always present), with the intelligent default marked — so the
	// page is never a dead-end even when there is no open PR to grab.
	resp.Options = buildTestSlotOptions(watches)

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
		// Resolve the preflight against a watch's PR BY NUMBER, not the open PR by
		// branch: a merged PR has no *open* PR for the branch, so by-branch reports
		// "no_pr" and hides the merge; reading it by number sees `merged=true` (→ a
		// purple "Merged"). Honor an explicit `?pr=<n>` so the user can preflight
		// any of the session's branches/PRs; default to the newest watch.
		preflightReq := req
		if target := pickWatch(watches, selectedPRNumber(r)); target != nil && target.PRNumber > 0 {
			preflightReq.PRNumber = target.PRNumber
			if o := strings.TrimSpace(target.PROwner); o != "" {
				preflightReq.RepoOwner = o
			}
			if n := strings.TrimSpace(target.PRName); n != "" {
				preflightReq.RepoName = n
			}
		}
		preflight := s.testSlotPreflight(r.Context(), preflightReq)
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

// selectedPRNumber parses the optional `?pr=<n>` selection (the branch/PR the
// user picked on the page). 0 means "no explicit selection — use the default".
func selectedPRNumber(r *http.Request) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pr")))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// pickWatch chooses which watch to preflight: the explicitly-selected PR when
// given (and present in the session's set), else the newest. nil when the
// session has no watches yet.
func pickWatch(watches []pgstore.CIWatch, selectedPR int) *pgstore.CIWatch {
	if selectedPR > 0 {
		for i := range watches {
			if watches[i].PRNumber == selectedPR {
				return &watches[i]
			}
		}
	}
	if len(watches) > 0 {
		return &watches[0]
	}
	return nil
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
		PendingChecks:  state.PendingChecks,
		PRNumber:       state.PR.Number,
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
	// PRs is every branch/PR the agent has worked on this session (newest first),
	// so the page can list them and let the user pick which to provision.
	PRs       []testSlotPRView       `json:"prs"`
	// Options is the deployable target set the page offers (open PRs + main),
	// with the intelligent default marked, so provisioning is never a dead-end.
	Options   []testSlotOption       `json:"options"`
	Provision *testSlotProvisionView `json:"provision"`
	TestState map[string]any         `json:"test_state"`
	Preflight *testSlotPreflightView `json:"preflight"`
}

// testSlotPRView is one branch/PR the session has worked on, for the page's
// picker. Derived from the durable session_ci_watches row.
type testSlotPRView struct {
	PRNumber       int     `json:"pr_number"`
	PRURL          string  `json:"pr_url"`
	Status         string  `json:"status"`
	MergeableState string  `json:"mergeable_state"`
	CheckState     string  `json:"check_state"`
	Detail         string  `json:"detail"`
	HeadSHA        string  `json:"head_sha"`
	LastEventAt    *string `json:"last_event_at"`
	HasOpenPR      bool    `json:"has_open_pr"`
}

func testSlotPRViewFrom(watch pgstore.CIWatch) testSlotPRView {
	return testSlotPRView{
		PRNumber:       watch.PRNumber,
		PRURL:          strings.TrimSpace(watch.PRURL),
		Status:         string(watch.Status),
		MergeableState: strings.TrimSpace(watch.MergeableState),
		CheckState:     strings.TrimSpace(watch.CheckState),
		Detail:         strings.TrimSpace(watch.Detail),
		HeadSHA:        strings.TrimSpace(watch.HeadSHA),
		LastEventAt:    rfc3339Ptr(watch.LastEventAt),
		HasOpenPR:      ciWatchStatusImpliesOpenPR(watch.Status),
	}
}

// testSlotDefaultRef is the always-available baseline deploy target. Stage 1
// assumes the governed repos' default branch is `main` (true for every repo
// today); resolving the repo's actual default branch is a later refinement.
const testSlotDefaultRef = "main"

// testSlotOption is one deployable target the page offers. The page renders
// these as the choices for "what to put in the slot," preselecting the one
// marked Default — so provisioning is never a dead-end: even with no open PR,
// `main` is always offered.
type testSlotOption struct {
	Kind     string `json:"kind"`                // "pr" | "ref"
	Label    string `json:"label"`               // e.g. "PR #1364" / "main (default branch)"
	PRNumber int    `json:"pr_number,omitempty"` // kind=pr
	PRURL    string `json:"pr_url,omitempty"`     // kind=pr
	Status   string `json:"status,omitempty"`    // kind=pr: durable watch status
	Ref      string `json:"ref,omitempty"`       // kind=ref: git ref to deploy (e.g. "main")
	Default  bool   `json:"default"`             // the intelligent preselection
}

// buildTestSlotOptions assembles the deployable targets the page offers: every
// open PR the session has worked on (newest first), plus `main` as an
// always-available baseline so there is never a dead-end. The Default marks the
// intelligent preselection: a ready open PR, else the newest open PR, else main.
func buildTestSlotOptions(watches []pgstore.CIWatch) []testSlotOption {
	options := make([]testSlotOption, 0, len(watches)+1)
	for i := range watches {
		w := watches[i]
		if !ciWatchStatusImpliesOpenPR(w.Status) {
			continue
		}
		options = append(options, testSlotOption{
			Kind:     "pr",
			Label:    "PR #" + strconv.Itoa(w.PRNumber),
			PRNumber: w.PRNumber,
			PRURL:    strings.TrimSpace(w.PRURL),
			Status:   string(w.Status),
		})
	}
	options = append(options, testSlotOption{
		Kind:  "ref",
		Label: "main (default branch)",
		Ref:   testSlotDefaultRef,
	})
	// Intelligent default: a ready open PR, else the newest open PR, else main.
	def := -1
	for i := range options {
		if options[i].Kind == "pr" && options[i].Status == string(pgstore.CIWatchReady) {
			def = i
			break
		}
	}
	if def < 0 {
		for i := range options {
			if options[i].Kind == "pr" {
				def = i
				break
			}
		}
	}
	if def < 0 {
		def = len(options) - 1
	}
	options[def].Default = true
	return options
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
	PendingChecks  []string `json:"pending_checks"`
	PRNumber       int      `json:"pr_number"`
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
