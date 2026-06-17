package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type orchestrationRunStore interface {
	Create(context.Context, pgstore.CreateOrchestrationRequest) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	Approve(context.Context, string, string) (pgstore.Orchestration, error)
	GetWithPhases(ctx context.Context, orchestrationID string) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	UpdateState(ctx context.Context, orchestrationID string, state pgstore.OrchestrationState) (pgstore.Orchestration, error)
}

type orchestrationPlanPhaseRequest struct {
	PhaseKey string   `json:"phase_key"`
	Key      string   `json:"key"`
	Brief    string   `json:"brief"`
	Depends  []string `json:"depends_on"`
	Target   string   `json:"target"`
}

type createOrchestrationRequest struct {
	Repo              string                          `json:"repo"`
	RepoOwner         string                          `json:"repo_owner"`
	RepoName          string                          `json:"repo_name"`
	IntegrationBranch string                          `json:"integration_branch"`
	Phases            []orchestrationPlanPhaseRequest `json:"phases"`
}

type orchestrationResponse struct {
	Orchestration pgstore.Orchestration        `json:"orchestration"`
	Phases        []pgstore.OrchestrationPhase `json:"phases"`
}

func (s *appServer) handleCreateOrchestration(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.orchestrationRuns == nil || s.orchestrations == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration store unavailable")
		return
	}
	var body createOrchestrationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Repo != "" && (body.RepoOwner == "" || body.RepoName == "") {
		parts := strings.Split(strings.TrimSpace(body.Repo), "/")
		if len(parts) == 2 {
			body.RepoOwner, body.RepoName = parts[0], parts[1]
		}
	}
	phases := make([]pgstore.PlanPhase, 0, len(body.Phases))
	wantsIntegration := strings.TrimSpace(body.IntegrationBranch) != ""
	for i, p := range body.Phases {
		key := strings.TrimSpace(p.PhaseKey)
		if key == "" {
			key = strings.TrimSpace(p.Key)
		}
		target := pgstore.PhaseTarget(strings.TrimSpace(p.Target))
		if target == pgstore.PhaseTargetIntegration {
			wantsIntegration = true
		}
		phases = append(phases, pgstore.PlanPhase{
			Key:       key,
			Brief:     p.Brief,
			DependsOn: p.Depends,
			Target:    target,
			Ordinal:   i,
		})
	}

	orchestrationID, err := pgstore.NewOrchestrationID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mint orchestration id: "+err.Error())
		return
	}
	integrationBranch := strings.TrimSpace(body.IntegrationBranch)
	if wantsIntegration && integrationBranch == "" {
		integrationBranch = "tank/orchestration/" + orchestrationID + "/integration"
	}
	if strings.TrimSpace(integrationBranch) != "" && s.mcpGitHub == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp-github client not configured")
		return
	}

	ownerEmail := orchestrationActorEmail(user)
	orch, _, err := s.orchestrationRuns.Create(r.Context(), pgstore.CreateOrchestrationRequest{
		OrchestrationID:   orchestrationID,
		OwnerEmail:        ownerEmail,
		RepoOwner:         body.RepoOwner,
		RepoName:          body.RepoName,
		IntegrationBranch: integrationBranch,
		State:             pgstore.OrchestrationDraft,
		Phases:            phases,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "create orchestration: "+err.Error())
		return
	}
	if strings.TrimSpace(orch.IntegrationBranch) != "" {
		if err := s.mcpGitHub.CreateBranch(r.Context(), ownerEmail, orch.RepoOwner, orch.RepoName, orch.IntegrationBranch, "main"); err != nil && !isBranchAlreadyExistsError(err) {
			writeError(w, http.StatusBadGateway, "create integration branch: "+err.Error())
			return
		}
	}
	if _, err := s.orchestrationRuns.Approve(r.Context(), orchestrationID, user.Email); err != nil {
		writeError(w, http.StatusInternalServerError, "approve orchestration: "+err.Error())
		return
	}
	if err := s.orchestrations.reconcileRun(r.Context(), orchestrationID); err != nil {
		writeError(w, http.StatusInternalServerError, "start orchestration: "+err.Error())
		return
	}
	orch, outPhases, err := s.orchestrationRuns.GetWithPhases(r.Context(), orchestrationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read orchestration: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, orchestrationResponse{Orchestration: orch, Phases: outPhases})
}

func isBranchAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "reference already exists")
}

func (s *appServer) handleInternalOrchestrationBlocked(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/orchestration/blocked")
	if user == nil {
		return
	}
	if s.orchestrations == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration engine unavailable")
		return
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	orch, phase, err := s.orchestrations.blockSpokePhase(r.Context(), r.PathValue("session_id"))
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			writeError(w, http.StatusNotFound, "session is not an orchestration phase")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.emitOrchestrationPhaseStatusRecord(r.Context(), orch, phase, user.ActorEmail, "failed", strings.TrimSpace(body.Detail))
	writeJSON(w, http.StatusOK, map[string]any{"phase": phase})
}

func (s *appServer) emitOrchestrationPhaseStatusRecord(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase, ownerEmail, state, detail string) {
	sessionID := strings.TrimSpace(phase.SpokeSessionID)
	if sessionID == "" {
		return
	}
	repo := orch.RepoOwner + "/" + orch.RepoName
	prURL := strings.TrimSpace(phase.PRURL)
	if prURL == "" {
		prURL = "https://github.com/" + repo
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	event := conversation.CIStatusUpdatedEventMap(conversation.CIStatusUpdatedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             ownerEmail,
		Repo:              repo,
		PRNumber:          phase.PRNumber,
		PRURL:             prURL,
		State:             state,
		Detail:            detail,
	})
	if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
		slog.Warn("orchestration phase status record persist failed", "session", sessionID, "phase_id", phase.PhaseID, "error", err)
	}
}

func orchestrationActorEmail(user auth.User) string {
	if owner := repoLookupOwnerEmail(user); owner != "" {
		return owner
	}
	return user.Email
}
