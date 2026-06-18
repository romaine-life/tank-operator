package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/glimmung"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

func (s *appServer) handleOrchestrationPhaseMerged(ctx context.Context, phase pgstore.OrchestrationPhase) error {
	if phase.Target != pgstore.PhaseTargetMain {
		return nil
	}
	if s.orchestrationRuns == nil {
		return nil
	}
	orch, _, err := s.orchestrationRuns.GetWithPhases(ctx, phase.OrchestrationID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(orch.IntegrationBranch) == "" {
		return nil
	}
	if _, _, _, err := s.createAndMergeBranchPR(ctx, orch.OwnerEmail, orch, "main", orch.IntegrationBranch, "merge-forward"); err != nil {
		if isNoBranchDiffError(err) {
			return nil
		}
		_, _ = s.orchestrationRuns.UpdateState(ctx, orch.OrchestrationID, pgstore.OrchestrationFailed)
		s.emitOrchestrationPhaseStatusRecord(ctx, orch, phase, orch.OwnerEmail, "failed", "Integration branch merge-forward failed: "+err.Error())
		return err
	}
	return nil
}

func (s *appServer) handleApproveOrchestrationReview(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.orchestrationRuns == nil || s.mcpGitHub == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration review approval unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("orchestration_id"))
	orch, phases, err := s.orchestrationRuns.GetWithPhases(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationNotFound) {
			writeError(w, http.StatusNotFound, "orchestration not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if orch.OwnerEmail != "" && !strings.EqualFold(orch.OwnerEmail, orchestrationActorEmail(user)) {
		writeError(w, http.StatusForbidden, "orchestration belongs to a different owner")
		return
	}
	if orch.State != pgstore.OrchestrationAwaitingReview {
		writeError(w, http.StatusConflict, "orchestration is not awaiting review")
		return
	}
	if strings.TrimSpace(orch.IntegrationBranch) == "" {
		updated, err := s.orchestrationRuns.UpdateState(r.Context(), orch.OrchestrationID, pgstore.OrchestrationDone)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orchestration": updated, "phases": phases})
		return
	}
	pr, mergeCommit, _, err := s.createAndMergeBranchPR(r.Context(), orchestrationActorEmail(user), orch, orch.IntegrationBranch, "main", "final")
	if err != nil {
		writeError(w, http.StatusConflict, "merge integration to main failed: "+err.Error())
		return
	}
	updated, err := s.orchestrationRuns.UpdateState(r.Context(), orch.OrchestrationID, pgstore.OrchestrationDone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mark orchestration done: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orchestration": updated,
		"phases":        phases,
		"pull_request":  pr,
		"merge_commit":  mergeCommit,
	})
}

func (s *appServer) createAndMergeBranchPR(ctx context.Context, actorEmail string, orch pgstore.Orchestration, head, base, purpose string) (mcpgithub.PullRequest, string, bool, error) {
	if s.mcpGitHub == nil {
		return mcpgithub.PullRequest{}, "", false, errors.New("mcp-github client not configured")
	}
	head = strings.TrimSpace(head)
	base = strings.TrimSpace(base)
	if head == "" || base == "" {
		return mcpgithub.PullRequest{}, "", false, errors.New("branch PR requires head and base")
	}
	title := orchestrationBranchPRTitle(orch, head, base, purpose)
	body := "Automated orchestration branch merge for " + orch.OrchestrationID + ".\n\n" +
		"Head: `" + head + "`\n\nBase: `" + base + "`\n"
	pr, err := s.mcpGitHub.CreatePullRequest(ctx, actorEmail, orch.RepoOwner, orch.RepoName, title, head, base, body, false)
	if err != nil {
		if isNoBranchDiffError(err) {
			return mcpgithub.PullRequest{}, "", true, nil
		}
		return mcpgithub.PullRequest{}, "", false, err
	}
	if pr.Number <= 0 {
		return pr, "", false, errors.New("created branch PR without number")
	}
	mergeCommit, err := s.mcpGitHub.MergePR(ctx, actorEmail, orch.RepoOwner, orch.RepoName, pr.Number, "merge")
	if err != nil {
		return pr, "", false, err
	}
	return pr, mergeCommit, false, nil
}

func orchestrationBranchPRTitle(orch pgstore.Orchestration, head, base, purpose string) string {
	prefix := "Orchestration " + orch.OrchestrationID
	if purpose == "merge-forward" {
		return prefix + ": merge main into integration"
	}
	return prefix + ": merge integration into main"
}

func isNoBranchDiffError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no commits between") ||
		strings.Contains(msg, "no commits") ||
		strings.Contains(msg, "already up-to-date") ||
		strings.Contains(msg, "already up to date")
}

func (s *appServer) emitOrchestrationReviewReadyRecord(ctx context.Context, orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase) {
	target, ok := latestPhaseWithSpoke(phases)
	if !ok {
		return
	}
	target.PRURL = "https://github.com/" + orch.RepoOwner + "/" + orch.RepoName + "/tree/" + orch.IntegrationBranch
	detail := "Orchestration " + orch.OrchestrationID + " is awaiting review on integration branch " + orch.IntegrationBranch + "."
	// Emit the awaiting-review signal immediately. The gated test-slot
	// provisioning now validates PR-readiness and may wait minutes for CI to
	// settle, so it runs off this reconcile path (below) and reports the test
	// environment outcome in a follow-up status record.
	s.emitOrchestrationPhaseStatusRecord(ctx, orch, target, orch.OwnerEmail, "ready", detail)
	go s.provisionOrchestrationReviewSlot(orch, target)
}

// provisionOrchestrationReviewSlot runs the gated checkout+deploy for the
// orchestration review environment in the background (it can wait minutes for
// CI to settle) and emits a follow-up phase status record with the outcome. It
// uses a fresh background context budgeted for the settle cap plus deploy
// grace, deliberately not the reconcile ctx which may already be canceled.
func (s *appServer) provisionOrchestrationReviewSlot(orch pgstore.Orchestration, target pgstore.OrchestrationPhase) {
	ctx, cancel := context.WithTimeout(context.Background(), s.provisionBackgroundTimeout())
	defer cancel()
	// Register the durable pending record at kickoff so an orchestrator restart
	// mid-settle-wait leaves a 'pending' row the reconcile backstop re-drives.
	// A re-drive re-enters here on an already-'pending' (claimed) row; Register's
	// conditional ON CONFLICT leaves it untouched, so this is a safe no-op then.
	if s.pendingTestProvisions != nil {
		if _, _, err := s.pendingTestProvisions.Register(ctx, pgstore.RegisterPendingTestProvisionRequest{
			SessionScope:    s.sessionScope,
			SessionID:       target.SpokeSessionID,
			OwnerEmail:      orch.OwnerEmail,
			RepoOwner:       orch.RepoOwner,
			RepoName:        orch.RepoName,
			Branch:          orch.IntegrationBranch,
			Project:         orchestrationGlimmungProject(orch),
			Workflow:        "orchestration-review",
			Kind:            pgstore.PendingTestProvisionOrchestrationReview,
			OrchestrationID: orch.OrchestrationID,
		}); err != nil {
			slog.Warn("orchestration review pending provision register failed",
				"orchestration_id", orch.OrchestrationID, "error", err)
		}
	}
	detail := "Test environment for orchestration " + orch.OrchestrationID + ": "
	checkout, deploy, err := s.checkoutAndDeployOrchestrationReview(ctx, orch, target.SpokeSessionID)
	if err != nil {
		detail += "setup failed: " + err.Error()
		slog.Warn("orchestration review gate setup failed", "orchestration_id", orch.OrchestrationID, "error", err)
		s.emitOrchestrationPhaseStatusRecord(ctx, orch, target, orch.OwnerEmail, "ready", detail)
		// checkoutAndDeployOrchestrationReview collapses gate refusals and infra
		// errors into one error; either way a verdict was reached and the record
		// is terminal (the backstop recovers only restart-stranded 'pending').
		s.markOrchestrationProvisionTerminal(ctx, orch, target, pgstore.PendingTestProvisionFailed, detail)
		return
	}
	if checkout.URL != nil && strings.TrimSpace(*checkout.URL) != "" {
		detail += strings.TrimSpace(*checkout.URL)
	} else {
		detail += "provisioned"
	}
	if deploy.Job != "" {
		detail += " (deploy job: " + deploy.Job + ")"
	}
	detail += "."
	s.emitOrchestrationPhaseStatusRecord(ctx, orch, target, orch.OwnerEmail, "ready", detail)
	s.markOrchestrationProvisionTerminal(ctx, orch, target, pgstore.PendingTestProvisionDone, detail)
}

// markOrchestrationProvisionTerminal closes the durable pending record for an
// orchestration-review provision at the end of its run, by its target
// coordinates (the spoke session + integration branch).
func (s *appServer) markOrchestrationProvisionTerminal(ctx context.Context, orch pgstore.Orchestration, target pgstore.OrchestrationPhase, status pgstore.PendingTestProvisionStatus, detail string) {
	s.markPendingTestProvisionTerminal(ctx, target.SpokeSessionID, orch.RepoOwner, orch.RepoName, orch.IntegrationBranch,
		pgstore.PendingTestProvisionOrchestrationReview, status, detail, "")
}

func latestPhaseWithSpoke(phases []pgstore.OrchestrationPhase) (pgstore.OrchestrationPhase, bool) {
	for i := len(phases) - 1; i >= 0; i-- {
		if strings.TrimSpace(phases[i].SpokeSessionID) != "" {
			return phases[i], true
		}
	}
	return pgstore.OrchestrationPhase{}, false
}

// checkoutAndDeployOrchestrationReview provisions the orchestration-review test
// environment, now gated by the shared deterministic provisioning helper: it
// validates the integration branch's live PR-readiness with the same
// classifyCIWatchState reducer the CI-watch path uses and only checks out +
// deploys on a green/mergeable verdict. A refusal verdict (failed CI, conflict,
// merged, settle timeout, head moved) is surfaced as an error so the caller's
// status record reflects that no environment was provisioned.
//
// The integration branch is validated by branch (no PR number) with the SHA
// pin disabled: the merge-forward path can re-head the branch between awaiting
// review and this run, and refusing on that movement would be a false negative
// for a branch we are deploying wholesale, not pinning to one commit.
func (s *appServer) checkoutAndDeployOrchestrationReview(ctx context.Context, orch pgstore.Orchestration, sessionID string) (glimmung.CheckoutTestSlotResult, glimmung.DeployImageToTestSlotResult, error) {
	if s.glimmung == nil {
		return glimmung.CheckoutTestSlotResult{}, glimmung.DeployImageToTestSlotResult{}, errors.New("glimmung client not configured")
	}
	outcome, err := s.provisionTestSlotForSession(ctx, provisionTestSlotRequest{
		OwnerEmail: orch.OwnerEmail,
		SessionID:  sessionID,
		Project:    orchestrationGlimmungProject(orch),
		Workflow:   "orchestration-review",
		RepoOwner:  orch.RepoOwner,
		RepoName:   orch.RepoName,
		Branch:     orch.IntegrationBranch,
	})
	if err != nil {
		return outcome.Checkout, outcome.Deploy, err
	}
	if !outcome.Provisioned {
		return outcome.Checkout, outcome.Deploy, provisionRefusalError(outcome)
	}
	return outcome.Checkout, outcome.Deploy, nil
}

func orchestrationGlimmungProject(orch pgstore.Orchestration) string {
	if strings.EqualFold(orch.RepoOwner, "romaine-life") && strings.TrimSpace(orch.RepoName) != "" {
		return strings.TrimSpace(orch.RepoName)
	}
	return defaultGlimmungProject
}
