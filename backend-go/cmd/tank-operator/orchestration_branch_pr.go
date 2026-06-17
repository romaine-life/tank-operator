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
	detail := "Orchestration " + orch.OrchestrationID + " is awaiting review on integration branch " + orch.IntegrationBranch + "."
	if checkout, deploy, err := s.checkoutAndDeployOrchestrationReview(ctx, orch, target.SpokeSessionID); err != nil {
		detail += " Test environment setup failed: " + err.Error()
		slog.Warn("orchestration review gate setup failed", "orchestration_id", orch.OrchestrationID, "error", err)
	} else {
		if checkout.URL != nil && strings.TrimSpace(*checkout.URL) != "" {
			detail += " Test environment: " + strings.TrimSpace(*checkout.URL) + "."
		}
		if deploy.Job != "" {
			detail += " Deploy job: " + deploy.Job + "."
		}
	}
	target.PRURL = "https://github.com/" + orch.RepoOwner + "/" + orch.RepoName + "/tree/" + orch.IntegrationBranch
	s.emitOrchestrationPhaseStatusRecord(ctx, orch, target, orch.OwnerEmail, "ready", detail)
}

func latestPhaseWithSpoke(phases []pgstore.OrchestrationPhase) (pgstore.OrchestrationPhase, bool) {
	for i := len(phases) - 1; i >= 0; i-- {
		if strings.TrimSpace(phases[i].SpokeSessionID) != "" {
			return phases[i], true
		}
	}
	return pgstore.OrchestrationPhase{}, false
}

func (s *appServer) checkoutAndDeployOrchestrationReview(ctx context.Context, orch pgstore.Orchestration, sessionID string) (glimmung.CheckoutTestSlotResult, glimmung.DeployImageToTestSlotResult, error) {
	if s.glimmung == nil {
		return glimmung.CheckoutTestSlotResult{}, glimmung.DeployImageToTestSlotResult{}, errors.New("glimmung client not configured")
	}
	project := orchestrationGlimmungProject(orch)
	workflow := "orchestration-review"
	checkout, err := s.glimmung.CheckoutTestSlot(ctx, orch.OwnerEmail, glimmung.CheckoutTestSlotRequest{
		Project:       project,
		Workflow:      &workflow,
		TankSessionID: &sessionID,
	})
	if err != nil {
		return checkout, glimmung.DeployImageToTestSlotResult{}, err
	}
	if checkout.SlotIndex == nil && checkout.SlotName == nil {
		return checkout, glimmung.DeployImageToTestSlotResult{}, errors.New("glimmung checkout returned no slot identity")
	}
	deploy, err := s.glimmung.DeployImageToTestSlot(ctx, orch.OwnerEmail, glimmung.DeployImageToTestSlotRequest{
		Project:   project,
		SlotIndex: checkout.SlotIndex,
		SlotName:  checkout.SlotName,
		GitRef:    orch.IntegrationBranch,
	})
	if err != nil {
		return checkout, deploy, err
	}
	if s.mgr != nil && checkout.URL != nil {
		if _, err := s.mgr.SetTestState(ctx, orch.OwnerEmail, sessionID, true, checkout.SlotIndex, checkout.URL, nil); err != nil {
			slog.Warn("orchestration review gate set test state failed", "session_id", sessionID, "error", err)
		}
	}
	return checkout, deploy, nil
}

func orchestrationGlimmungProject(orch pgstore.Orchestration) string {
	if strings.EqualFold(orch.RepoOwner, "romaine-life") && strings.TrimSpace(orch.RepoName) != "" {
		return strings.TrimSpace(orch.RepoName)
	}
	return defaultGlimmungProject
}
