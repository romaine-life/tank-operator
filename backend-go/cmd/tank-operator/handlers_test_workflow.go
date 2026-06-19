package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
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
	// optional: a single-repo session needs none. `drive` selects the
	// "Create test slot and test" variant: provision deterministically (same
	// zero-LLM gate) and, only on the ready terminal, wake the agent to validate
	// the running slot.
	var body struct {
		Repo  string `json:"repo"`
		Drive bool   `json:"drive"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	req, herr := s.resolveTestWorkflowTarget(r.Context(), owner, sessionID, info, strings.TrimSpace(body.Repo))
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}
	req.drive = body.Drive

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
// settle) and records the outcome durably. The user-visible surface is the
// dedicated test-slot page (see handlers_test_slot_status.go), NOT the
// transcript: provisioning no longer emits any transcript record. On a ready
// verdict the gate's SetTestState already marked the slot active + URL (the page
// and the pill light from the session-row SSE); on any refusal the gate left
// glimmung and test-state untouched and the reason lands on the durable
// pending_test_provisions row the page reads. It uses a fresh background context
// budgeted for the settle cap plus deploy grace, not a possibly-canceled request
// ctx.
func (s *appServer) runInteractiveTestWorkflow(req provisionTestSlotRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), s.provisionBackgroundTimeout())
	defer cancel()

	repo := req.RepoOwner + "/" + req.RepoName

	outcome, err := s.provisionTestSlotForSession(ctx, req)
	if err != nil {
		recordTestSlotInteractive("error")
		slog.Warn("interactive test workflow gate failed",
			"session_id", req.SessionID, "repo", repo, "branch", req.Branch, "error", err)
		// Infra error: terminalize 'failed' so the backstop only recovers a
		// restart-stranded record, not a deterministic infra failure loop. The
		// reason surfaces on the test-slot page via the durable pending row.
		s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionFailed, "gate error: "+err.Error())
		return
	}
	if outcome.Provisioned {
		recordTestSlotInteractive(provisionStepProvisioned)
		s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionDone, strings.TrimSpace(outcome.Detail))
		// The "drive" variant (and only on a ready terminal) hands back to the
		// agent: provisioning stayed zero-LLM, and now the agent re-enters to do
		// the inherently-agent part — exercise the running app. A refusal never
		// reaches here, so a no-slot outcome never wakes.
		if req.drive {
			s.driveTestSlot(ctx, req, testProvisionOutcomeURL(outcome))
		}
		return
	}

	// Refusal: a reached verdict, not a strand. Terminalize 'done' with the
	// reason so the backstop does not re-drive it and the page can show why no
	// environment came up.
	recordTestSlotInteractive(string(outcome.Verdict))
	reason := strings.TrimSpace(outcome.Detail)
	if reason == "" {
		reason = "no test environment for " + req.Branch + " (" + string(outcome.Verdict) + ")"
	}
	s.markInteractiveProvisionTerminal(ctx, req, outcome.HeadSHA, pgstore.PendingTestProvisionDone, reason)
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

// driveTestSlot wakes the session's agent after a successful interactive
// "drive" provision so it validates its changes against the now-running slot.
// The LLM boundary is the whole point: provisioning ran zero-LLM through the
// gate, and the agent re-enters only here — never on a refusal — to exercise
// the running app. The wake reuses the same backend-owned-turn machinery
// ScheduleWakeup fires (enqueueSDKTurn → durable user_message.created +
// turn.submitted + a submit_turn command, tagged source=test-slot-drive); it is
// not a hand-rolled turn. A wake failure is logged + countered, not fatal: the
// slot is up and the ready thread already announced it.
func (s *appServer) driveTestSlot(ctx context.Context, req provisionTestSlotRequest, url string) {
	resp, status, detail := s.enqueueTestDriveWakeTurn(ctx, req, url)
	if status != 0 {
		recordTestSlotInteractive("drive_wake_error")
		slog.Warn("interactive test workflow drive wake failed",
			"session_id", req.SessionID, "branch", req.Branch, "status", status, "detail", detail)
		return
	}
	recordTestSlotInteractive("drive_wake")
	slog.Info("interactive test workflow drive wake submitted",
		"session_id", req.SessionID, "turn_id", turnIDFromEnqueueResponse(resp), "url", url)
}

// enqueueTestDriveWakeTurn submits the backend-owned wake turn for the drive
// variant. Production reuses enqueueSDKTurn (the ScheduleWakeup path); tests
// inject testDriveWakeSubmit to capture the submission without standing up the
// full sessionBus/pod machinery enqueueSDKTurn requires.
func (s *appServer) enqueueTestDriveWakeTurn(ctx context.Context, req provisionTestSlotRequest, url string) (map[string]any, int, string) {
	if s.testDriveWakeSubmit != nil {
		return s.testDriveWakeSubmit(ctx, req, url)
	}
	seed := req.SessionID + ":" + req.Branch + ":" + url
	return s.enqueueSDKTurn(ctx, req.OwnerEmail, req.SessionID, sdkTurnRequest{
		ClientNonce:  testDriveWakeTurnNonce(seed),
		RequireNonce: true,
		Prompt:       testDriveWakePrompt(req, url),
		DisplayText:  testDriveWakeDisplayText(url),
		Source:       string(conversation.TurnSubmittedSourceTestSlotDrive),
		CreatedAt:    s.provisionNowTime().UTC(),
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
}

// testDriveWakeTurnNonce derives a deterministic, turn-id-shaped client nonce
// for a drive wake so a re-fire on the same (session, branch, url) collapses to
// one turn under JetStream's command dedupe rather than waking twice.
func testDriveWakeTurnNonce(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = randomHex(12)
	}
	sum := sha256.Sum256([]byte(seed))
	return "turn_testdrive_" + hex.EncodeToString(sum[:12])
}

// testDriveWakeDisplayText is the system-authored line the transcript renders
// for the wake turn (the agent's prompt itself is the larger instruction).
func testDriveWakeDisplayText(url string) string {
	if strings.TrimSpace(url) != "" {
		return "Test slot ready at " + url + " — validating your changes against it."
	}
	return "Test slot ready — validating your changes against it."
}

// testDriveWakePrompt is the instruction the woken agent runs: the slot already
// exists at url (provisioned deterministically), so it only has to validate —
// browse it, exercise the changed feature, and report findings. It does NOT ask
// the agent to reserve or check out a slot; that already happened, zero-LLM.
func testDriveWakePrompt(req provisionTestSlotRequest, url string) string {
	repo := strings.Trim(req.RepoOwner+"/"+req.RepoName, "/")
	var b strings.Builder
	b.WriteString("A test environment for your changes is already live")
	if url != "" {
		b.WriteString(" at ")
		b.WriteString(url)
	}
	b.WriteString(". It was provisioned for you — do NOT reserve, check out, or hot-swap a slot; one already exists and is running your branch")
	if strings.TrimSpace(req.Branch) != "" {
		b.WriteString(" (")
		b.WriteString(req.Branch)
		b.WriteString(")")
	}
	if repo != "" {
		b.WriteString(" of ")
		b.WriteString(repo)
	}
	b.WriteString(".\n\nValidate your changes against it now: invoke the /test-drive skill (it assumes the slot already exists and only validates), or do it directly — browse the running app")
	if url != "" {
		b.WriteString(" at ")
		b.WriteString(url)
	}
	b.WriteString(", exercise the feature you changed end to end, and report concrete findings (what you checked, what worked, what didn't) with evidence. If something is broken, say so plainly.")
	return b.String()
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
