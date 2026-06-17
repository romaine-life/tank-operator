package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type prReadinessRegistration struct {
	SessionID       string
	OwnerEmail      string
	PROwner         string
	PRName          string
	PRNumber        int
	Branch          string
	ExpectedHeadSHA string
	MergeableState  string
	CheckState      string
	Detail          string
	PRURL           string
	Status          string
}

type prReadinessRegistrationResult struct {
	Watch  pgstore.CIWatch
	Result ciWatchReconcileResult
}

type prReadinessRequestBody struct {
	Repo            string `json:"repo"`
	PROwner         string `json:"pr_owner"`
	PRName          string `json:"pr_name"`
	PRNumber        int    `json:"pr_number"`
	Branch          string `json:"branch"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	HeadSHA         string `json:"head_sha"`
	MergeableState  string `json:"mergeable_state"`
	CheckState      string `json:"check_state"`
	Detail          string `json:"detail"`
	PRURL           string `json:"pr_url"`
	Status          string `json:"status"`
}

// handleInternalRegisterPRReadiness is Tank's neutral PR/head readiness entry
// point. Older routes such as /ci-watches and /hot-swap/verify are compatibility
// facades over the same registration + reconcile process.
func (s *appServer) handleInternalRegisterPRReadiness(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/pr-readiness")
	if user == nil {
		return
	}
	var body prReadinessRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req, err := prReadinessRegistrationFromBody(r.PathValue("session_id"), user.ActorEmail, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	registered, err := s.registerAndReconcilePRReadiness(r.Context(), req, ciWatchReconcileHandoff)
	if err != nil {
		writeError(w, http.StatusBadGateway, "reconcile PR readiness: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, prReadinessResponseBody(registered.Watch, registered.Result))
}

func (s *appServer) registerAndReconcilePRReadiness(ctx context.Context, req prReadinessRegistration, source ciWatchReconcileSource) (prReadinessRegistrationResult, error) {
	if s.ciWatches == nil {
		return prReadinessRegistrationResult{}, errCIWatchReconcileUnavailable("ci watch store unavailable")
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	req.PROwner = strings.ToLower(strings.TrimSpace(req.PROwner))
	req.PRName = strings.ToLower(strings.TrimSpace(req.PRName))
	req.Branch = strings.TrimSpace(req.Branch)
	req.ExpectedHeadSHA = strings.TrimSpace(req.ExpectedHeadSHA)
	req.PRURL = strings.TrimSpace(req.PRURL)
	if req.SessionID == "" || req.OwnerEmail == "" || req.PROwner == "" || req.PRName == "" {
		return prReadinessRegistrationResult{}, errors.New("missing session, owner, or repo")
	}

	var resolved *mcpgithub.PullRequestState
	if req.PRNumber <= 0 {
		if req.Branch == "" {
			return prReadinessRegistrationResult{}, errors.New("pr_number or branch is required")
		}
		if s.mcpGitHub == nil {
			return prReadinessRegistrationResult{}, errCIWatchReconcileUnavailable("mcp-github client not configured")
		}
		state, err := s.mcpGitHub.ResolveOpenPullRequestState(ctx, req.OwnerEmail, req.PROwner, req.PRName, req.PROwner, req.Branch)
		if err != nil {
			return prReadinessRegistrationResult{}, err
		}
		resolved = &state
		req.PRNumber = state.PR.Number
		if req.ExpectedHeadSHA == "" {
			req.ExpectedHeadSHA = state.HeadSHA
		}
		if req.PRURL == "" {
			req.PRURL = state.HTMLURL
		}
	}
	if req.PRNumber <= 0 {
		return prReadinessRegistrationResult{}, errors.New("missing pr_number")
	}
	watch, err := s.ciWatches.Register(ctx, pgstore.RegisterCIWatchRequest{
		SessionID:      req.SessionID,
		OwnerEmail:     req.OwnerEmail,
		PROwner:        req.PROwner,
		PRName:         req.PRName,
		PRNumber:       req.PRNumber,
		HeadSHA:        req.ExpectedHeadSHA,
		MergeableState: req.MergeableState,
		CheckState:     req.CheckState,
		Detail:         req.Detail,
		PRURL:          req.PRURL,
	})
	if err != nil {
		return prReadinessRegistrationResult{}, err
	}
	s.linkReadinessPRToOrchestrationPhase(ctx, watch)

	if s.mcpGitHub == nil {
		if ciWatchRegistrationReady(req.Status, req.CheckState, req.MergeableState) {
			s.handleGreenCIWatch(ctx, watch, req.Detail)
		}
		return prReadinessRegistrationResult{Watch: watch, Result: ciWatchReconcileResult{
			Status:         watch.Status,
			HeadSHA:        watch.HeadSHA,
			MergeableState: watch.MergeableState,
			CheckState:     watch.CheckState,
			Detail:         watch.Detail,
			PRURL:          watch.PRURL,
		}}, nil
	}

	var result ciWatchReconcileResult
	if resolved != nil {
		result, err = s.applyResolvedCIWatchState(ctx, watch, *resolved, source, 0)
	} else {
		result, err = s.reconcileAndApplyCIWatch(ctx, watch, source)
	}
	if err != nil {
		return prReadinessRegistrationResult{}, err
	}
	return prReadinessRegistrationResult{Watch: watch, Result: result}, nil
}

func (s *appServer) linkReadinessPRToOrchestrationPhase(ctx context.Context, watch pgstore.CIWatch) {
	if s.orchestrations == nil {
		return
	}
	s.orchestrations.linkPhasePR(ctx, watch.SessionID, pgstore.SetPhasePRRequest{
		PROwner:  watch.PROwner,
		PRName:   watch.PRName,
		PRNumber: watch.PRNumber,
		PRURL:    watch.PRURL,
	})
}

func prReadinessRegistrationFromBody(sessionID, ownerEmail string, body prReadinessRequestBody) (prReadinessRegistration, error) {
	owner := strings.TrimSpace(body.PROwner)
	name := strings.TrimSpace(body.PRName)
	if repo := strings.TrimSpace(body.Repo); repo != "" {
		repoOwner, repoName, ok := strings.Cut(repo, "/")
		if !ok || strings.TrimSpace(repoOwner) == "" || strings.TrimSpace(repoName) == "" {
			return prReadinessRegistration{}, errors.New("repo must be a GitHub slug like owner/name")
		}
		if owner != "" && !strings.EqualFold(owner, repoOwner) {
			return prReadinessRegistration{}, errors.New("repo and pr_owner disagree")
		}
		if name != "" && !strings.EqualFold(name, repoName) {
			return prReadinessRegistration{}, errors.New("repo and pr_name disagree")
		}
		owner, name = repoOwner, repoName
	}
	headSHA := strings.TrimSpace(body.ExpectedHeadSHA)
	if headSHA == "" {
		headSHA = strings.TrimSpace(body.HeadSHA)
	}
	return prReadinessRegistration{
		SessionID:       sessionID,
		OwnerEmail:      ownerEmail,
		PROwner:         owner,
		PRName:          name,
		PRNumber:        body.PRNumber,
		Branch:          body.Branch,
		ExpectedHeadSHA: headSHA,
		MergeableState:  body.MergeableState,
		CheckState:      body.CheckState,
		Detail:          body.Detail,
		PRURL:           body.PRURL,
		Status:          body.Status,
	}, nil
}

func prReadinessResponseBody(watch pgstore.CIWatch, result ciWatchReconcileResult) map[string]any {
	return map[string]any{
		"watch":           watch,
		"repo":            watch.PROwner + "/" + watch.PRName,
		"pr_number":       watch.PRNumber,
		"state":           ciWatchToolState(result.Status),
		"detail":          result.Detail,
		"head_sha":        result.HeadSHA,
		"mergeable_state": result.MergeableState,
		"check_state":     result.CheckState,
		"failing_checks":  result.FailingChecks,
		"pr_url":          result.PRURL,
	}
}
