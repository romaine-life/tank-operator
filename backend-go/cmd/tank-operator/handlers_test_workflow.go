package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// interactiveTestWorkflowLabel labels the glimmung checkout lease for the
// interactive (UI-button-triggered) test workflow so it is distinguishable from
// the "orchestration-review" lease the autonomous path leases.
const interactiveTestWorkflowLabel = "interactive-test"

// testWorkflowResolveError carries an HTTP status + message out of the
// coordinate-resolution helper without writing the response itself.
type testWorkflowResolveError struct {
	status int
	msg    string
}

// handleStartTestWorkflow is the deterministic, zero-LLM replacement for the
// retired interactive "/test" skill. The UI "test" button POSTs here instead of
// sending the skill prompt to the agent. The backend resolves the session's
// governed-PR coordinates from durable state (owner, repo, the governed session
// branch tank/session/<id>/<repo>, glimmung project) and runs the shared
// validate→wait→provision gate (provisionTestSlotForSession) in a background
// goroutine with a fresh budgeted context, mirroring
// provisionOrchestrationReviewSlot. It returns 202 immediately because the
// gate's settle-wait can take minutes. The branch's current head is validated
// ("latest pushed" semantics): no PR number pin, no ExpectedSHA pin.
//
// Owner-scoped like the other human-facing /api/sessions/{id}/... routes: the
// caller can only trigger their own session's test workflow.
func (s *appServer) handleStartTestWorkflow(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.glimmung == nil || s.mcpGitHub == nil {
		writeError(w, http.StatusServiceUnavailable, "test workflow is unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	owner := user.OwnerEmail()
	info, err := s.mgr.GetRegisteredByOwner(r.Context(), owner, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusNotFound, "session not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Optional `repo` override disambiguates a multi-repo session. The body is
	// optional: a single-repo session needs none.
	var body struct {
		Repo string `json:"repo"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	req, herr := s.resolveTestWorkflowTarget(r.Context(), owner, sessionID, info, strings.TrimSpace(body.Repo))
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	// Double-trigger guard (Slice-5). A rapid double-click must not start two
	// gate runs / two glimmung checkouts. Refuse when a test environment is
	// already active for this session, and -- atomically -- when a provision for
	// this exact target is already in flight (a non-terminal pending record).
	if active, _ := info.TestState["active"].(bool); active {
		recordTestSlotProvisionGuard("test_state_active")
		writeError(w, http.StatusConflict, "a test environment is already active for this session")
		return
	}
	if s.pendingTestProvisions != nil {
		_, created, err := s.pendingTestProvisions.Register(r.Context(), pgstore.RegisterPendingTestProvisionRequest{
			SessionScope: s.sessionScope,
			SessionID:    req.SessionID,
			OwnerEmail:   req.OwnerEmail,
			RepoOwner:    req.RepoOwner,
			RepoName:     req.RepoName,
			Branch:       req.Branch,
			Project:      req.Project,
			Workflow:     req.Workflow,
			Kind:         pgstore.PendingTestProvisionInteractive,
			PRNumber:     req.PRNumber,
			ExpectedSHA:  req.ExpectedSHA,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not record pending test provision: "+err.Error())
			return
		}
		if !created {
			recordTestSlotProvisionGuard("in_flight")
			writeError(w, http.StatusConflict, "a test workflow is already in progress for "+req.Branch)
			return
		}
		recordTestSlotProvisionGuard("launched")
	}

	s.launchInteractiveTestWorkflow(req)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "started",
		"repo":   req.RepoOwner + "/" + req.RepoName,
		"branch": req.Branch,
	})
}

// launchInteractiveTestWorkflow starts the background gate run. Production wiring
// is a fresh budgeted goroutine; tests inject a synchronous capture via
// interactiveTestWorkflowLaunch.
func (s *appServer) launchInteractiveTestWorkflow(req provisionTestSlotRequest) {
	if s.interactiveTestWorkflowLaunch != nil {
		s.interactiveTestWorkflowLaunch(req)
		return
	}
	go s.runInteractiveTestWorkflow(req)
}

// resolveTestWorkflowTarget derives the gate request from durable session state.
// Repo comes from sessions.repos (the create-time owner/name selection); the
// branch is the governed session branch the publish flow pushes to; the project
// is the glimmung project mapping for the repo. PRNumber/ExpectedSHA are left
// zero so the gate validates the branch's current head.
func (s *appServer) resolveTestWorkflowTarget(ctx context.Context, owner, sessionID string, info sessions.Info, repoOverride string) (provisionTestSlotRequest, *testWorkflowResolveError) {
	repos := make([]string, 0, len(info.Repos))
	for _, repo := range info.Repos {
		if trimmed := strings.TrimSpace(repo); trimmed != "" {
			repos = append(repos, trimmed)
		}
	}
	if len(repos) == 0 {
		return provisionTestSlotRequest{}, &testWorkflowResolveError{
			status: http.StatusBadRequest,
			msg:    "session has no repository; nothing to provision a test environment for",
		}
	}

	slug, herr := s.pickTestWorkflowRepo(ctx, sessionID, repos, repoOverride)
	if herr != nil {
		return provisionTestSlotRequest{}, herr
	}
	repoOwner, repoName, ok := splitRepoSlug(slug)
	if !ok {
		return provisionTestSlotRequest{}, &testWorkflowResolveError{
			status: http.StatusBadRequest,
			msg:    "session repository is not a valid owner/name slug: " + slug,
		}
	}

	return provisionTestSlotRequest{
		OwnerEmail: owner,
		SessionID:  sessionID,
		Project:    sessionGlimmungProject(repoOwner, repoName),
		Workflow:   interactiveTestWorkflowLabel,
		RepoOwner:  repoOwner,
		RepoName:   repoName,
		Branch:     sessionGovernedBranch(sessionID, repoName),
		// PRNumber=0, ExpectedSHA="": validate the branch's current head
		// ("latest pushed"), not a pinned PR number or commit.
	}, nil
}

// pickTestWorkflowRepo selects the repo to test. An explicit override wins (and
// must be one of the session's repos). A single-repo session is unambiguous.
// For a multi-repo session with no override, the repo carrying the open governed
// PR — the durable CI-watch record the publish flow registers — disambiguates;
// otherwise the request is refused as ambiguous.
func (s *appServer) pickTestWorkflowRepo(ctx context.Context, sessionID string, repos []string, override string) (string, *testWorkflowResolveError) {
	if override != "" {
		for _, repo := range repos {
			if strings.EqualFold(repo, override) || strings.EqualFold(repoNameOnly(repo), override) {
				return repo, nil
			}
		}
		return "", &testWorkflowResolveError{
			status: http.StatusBadRequest,
			msg:    "repo " + override + " is not one of this session's repositories",
		}
	}
	if len(repos) == 1 {
		return repos[0], nil
	}
	if s.ciWatches != nil {
		if watch, err := s.ciWatches.GetLatestForSession(ctx, s.sessionScope, sessionID); err == nil &&
			strings.TrimSpace(watch.PROwner) != "" && strings.TrimSpace(watch.PRName) != "" {
			watched := watch.PROwner + "/" + watch.PRName
			for _, repo := range repos {
				if strings.EqualFold(repo, watched) {
					return repo, nil
				}
			}
		}
	}
	return "", &testWorkflowResolveError{
		status: http.StatusConflict,
		msg:    "session has multiple repositories; specify which to test (repo=owner/name)",
	}
}

// runInteractiveTestWorkflow runs the gated validate→wait→provision sequence for
// the interactive trigger off the request path (it can wait minutes for CI to
// settle) and surfaces the outcome durably as a grouped role:system thread of
// test_provision.updated records: an opener on kickoff, intermediate
// validating/waiting updates as the gate advances, and a terminal ready/refusal
// record. On a ready verdict the gate's SetTestState already marked the slot
// active + URL; on any refusal the gate left glimmung and test-state untouched,
// so the refusal reason is the terminal record's text. It uses a fresh
// background context budgeted for the settle cap plus deploy grace, not a
// possibly-canceled request ctx.
func (s *appServer) runInteractiveTestWorkflow(req provisionTestSlotRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), s.provisionBackgroundTimeout())
	defer cancel()

	runID := s.newTestProvisionRunID()
	repo := req.RepoOwner + "/" + req.RepoName

	// Opener: the user sees the workflow start immediately, before the gate's
	// (potentially minutes-long) validate+wait runs.
	s.emitTestProvisionRecord(ctx, req, runID, "creating", "info", "Creating test slot.", "")

	// Intermediate progress: the gate calls back as it advances. Each phase is
	// emitted at most once per run (the closure dedupes).
	emitted := map[string]bool{}
	req.progress = func(phase string) {
		if emitted[phase] {
			return
		}
		emitted[phase] = true
		switch phase {
		case "validating":
			s.emitTestProvisionRecord(ctx, req, runID, "validating", "info", "Validating PR readiness…", "")
		case "waiting":
			s.emitTestProvisionRecord(ctx, req, runID, "waiting", "info", "Waiting for CI to settle…", "")
		}
	}

	outcome, err := s.provisionTestSlotForSession(ctx, req)
	if err != nil {
		recordTestSlotInteractive("error")
		slog.Warn("interactive test workflow gate failed",
			"session_id", req.SessionID, "repo", repo, "branch", req.Branch, "error", err)
		s.emitTestProvisionRecord(ctx, req, runID, "error", "error",
			"Couldn't create test slot: "+err.Error(), "")
		// Infra error: terminalize 'failed' so the backstop only recovers a
		// restart-stranded record, not a deterministic infra failure loop.
		s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionFailed, "gate error: "+err.Error())
		return
	}
	if outcome.Provisioned {
		recordTestSlotInteractive(provisionStepProvisioned)
		url := testProvisionOutcomeURL(outcome)
		text := "Test environment ready."
		if url != "" {
			text = "Test environment ready at " + url
		}
		s.emitTestProvisionRecord(ctx, req, runID, "ready", "info", text, url)
		s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionDone, strings.TrimSpace(outcome.Detail))
		return
	}

	// Refusal: surface the reason so the user sees why no environment came up.
	recordTestSlotInteractive(string(outcome.Verdict))
	reason := strings.TrimSpace(outcome.Detail)
	if reason == "" {
		reason = "no test environment for " + req.Branch + " (" + string(outcome.Verdict) + ")"
	}
	s.emitTestProvisionRecord(ctx, req, runID, "error", "error", "Couldn't create test slot: "+reason, "")
	// A gate refusal is a reached verdict, not a strand: terminalize 'done' so
	// the backstop does not re-drive a legitimately-refused provision.
	s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionDone, reason)
}

// newTestProvisionRunID derives a per-run identifier so the phases of one
// interactive provision run thread together (and a later re-run renders as a
// fresh thread). Uses the gate's clock so a fake clock keeps tests stable.
func (s *appServer) newTestProvisionRunID() string {
	return strconv.FormatInt(s.provisionNowTime().UTC().UnixNano(), 36)
}

// testProvisionOutcomeURL returns the provisioned test-environment URL from a
// ready outcome, if the glimmung checkout reported one.
func testProvisionOutcomeURL(outcome provisionOutcome) string {
	if outcome.Checkout.URL != nil {
		return strings.TrimSpace(*outcome.Checkout.URL)
	}
	return ""
}

// markInteractiveProvisionTerminal closes the durable pending record for an
// interactive provision at the end of its run, by its target coordinates.
func (s *appServer) markInteractiveProvisionTerminal(ctx context.Context, req provisionTestSlotRequest, headSHA string, status pgstore.PendingTestProvisionStatus, detail string) {
	s.markPendingTestProvisionTerminal(ctx, req.SessionID, req.RepoOwner, req.RepoName, req.Branch,
		pgstore.PendingTestProvisionInteractive, status, detail, headSHA)
}

// emitTestProvisionRecord writes a display-only test_provision.updated event to
// the session ledger so one phase of the interactive test-workflow run is
// visible inline as a grouped role:system thread — not only in logs. Each phase
// (creating → validating → waiting → ready/error) carries its own timeline_id
// keyed by runID, so the records append in order and group under one system
// avatar. The ci_status.updated emission this replaced never rendered inline
// (no projection case existed for it), so the prior outcome records were
// invisible.
func (s *appServer) emitTestProvisionRecord(ctx context.Context, req provisionTestSlotRequest, runID, phase, severity, text, url string) {
	if s.sessionEvents == nil {
		return
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, req.SessionID)
	event := conversation.TestProvisionUpdatedEventMap(conversation.TestProvisionUpdatedArgs{
		SessionID:         req.SessionID,
		SessionStorageKey: storageKey,
		Email:             req.OwnerEmail,
		RunID:             runID,
		Phase:             phase,
		Severity:          severity,
		Text:              text,
		Repo:              req.RepoOwner + "/" + req.RepoName,
		Branch:            req.Branch,
		URL:               url,
	})
	if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
		slog.Warn("interactive test workflow provision record persist failed",
			"session", req.SessionID, "phase", phase, "error", err)
	}
}

// sessionGovernedBranch is the Tank-owned session branch the governed Git flow
// publishes to: tank/session/<session_id>/<repoName>.
func sessionGovernedBranch(sessionID, repoName string) string {
	return "tank/session/" + strings.TrimSpace(sessionID) + "/" + strings.TrimSpace(repoName)
}

// sessionGlimmungProject maps a repo to its glimmung project, mirroring
// orchestrationGlimmungProject: romaine-life repos map to a project named after
// the repo; everything else falls back to the default project.
func sessionGlimmungProject(repoOwner, repoName string) string {
	if strings.EqualFold(strings.TrimSpace(repoOwner), "romaine-life") && strings.TrimSpace(repoName) != "" {
		return strings.TrimSpace(repoName)
	}
	return defaultGlimmungProject
}

func repoNameOnly(slug string) string {
	_, name, ok := splitRepoSlug(slug)
	if !ok {
		return ""
	}
	return name
}
