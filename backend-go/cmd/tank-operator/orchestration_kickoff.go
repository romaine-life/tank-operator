package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// The orchestration on-switch and human-gate HTTP surface. The advance engine
// (orchestration.go) drives a run forward once it is running; this file is the
// kickoff the engine never triggers on its own — freeze an approved plan into a
// durable run and dispatch its initial (no-dependency) phases — plus the human
// gates the autonomous run hands back to: promote the integration branch to
// main on "go", and unblock / fail a run a spoke escalated. Everything here is
// the service-principal internal surface (no UI); a session pod or an ops tool
// presents an auth.romaine.life service JWT.

// appOrchestrationStore is the durable-store slice the kickoff + gate handlers
// need (the engine owns the advance-time slice). Satisfied by
// *pgstore.OrchestrationStore; an interface so the handlers are testable.
type appOrchestrationStore interface {
	Create(ctx context.Context, req pgstore.CreateOrchestrationRequest) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	Get(ctx context.Context, orchestrationID string) (pgstore.Orchestration, error)
	GetWithPhases(ctx context.Context, orchestrationID string) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
}

// orchestrationPlanDoc is the approved plan document the kickoff endpoint
// accepts: the target repo, an optional shared integration branch, and the DAG
// of phases. It is the human-authored/approved input; the store freezes a
// canonical, content-hashed snapshot of it.
type orchestrationPlanDoc struct {
	RepoOwner         string                     `json:"repo_owner"`
	RepoName          string                     `json:"repo_name"`
	IntegrationBranch string                     `json:"integration_branch"`
	ApproverEmail     string                     `json:"approver_email"`
	Phases            []orchestrationPlanPhaseDoc `json:"phases"`
}

type orchestrationPlanPhaseDoc struct {
	PhaseKey  string   `json:"phase_key"`
	Brief     string   `json:"brief"`
	DependsOn []string `json:"depends_on"`
	Target    string   `json:"target"`
}

func (d orchestrationPlanDoc) toPlanPhases() []pgstore.PlanPhase {
	out := make([]pgstore.PlanPhase, 0, len(d.Phases))
	for _, p := range d.Phases {
		out = append(out, pgstore.PlanPhase{
			Key:       strings.TrimSpace(p.PhaseKey),
			Brief:     p.Brief,
			DependsOn: p.DependsOn,
			Target:    pgstore.PhaseTarget(strings.TrimSpace(p.Target)),
		})
	}
	return out
}

func (d orchestrationPlanDoc) hasIntegrationTarget() bool {
	for _, p := range d.Phases {
		if pgstore.PhaseTarget(strings.TrimSpace(p.Target)) == pgstore.PhaseTargetIntegration {
			return true
		}
	}
	return false
}

// createAndStartOrchestration freezes an approved plan into a durable run and
// kicks it off: it validates the plan, creates the run's integration branch off
// the repo's default branch when the plan has integration-target phases, inserts
// the run in 'approved', then drives it once so its root phases dispatch
// immediately (the engine drives everything after the first merge). Returns the
// run and its phases after the kickoff pass.
func (s *appServer) createAndStartOrchestration(ctx context.Context, ownerEmail string, doc orchestrationPlanDoc) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error) {
	if s.orchStore == nil || s.orchestrations == nil {
		return pgstore.Orchestration{}, nil, errors.New("orchestration subsystem unavailable")
	}
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	if ownerEmail == "" {
		return pgstore.Orchestration{}, nil, errors.New("orchestration requires an owner email")
	}
	approver := strings.ToLower(strings.TrimSpace(doc.ApproverEmail))
	if approver == "" {
		approver = ownerEmail
	}

	phases := doc.toPlanPhases()
	integ := strings.TrimSpace(doc.IntegrationBranch)
	if doc.hasIntegrationTarget() && integ == "" {
		return pgstore.Orchestration{}, nil, errors.New("integration-target phases require integration_branch in the plan")
	}

	// Validate the plan up front (cycles, dangling deps, bad targets) before any
	// side effect, so an invalid plan fails cleanly without creating a branch.
	if _, _, err := pgstore.OrchestrationPlanHash(doc.RepoOwner, doc.RepoName, integ, phases); err != nil {
		return pgstore.Orchestration{}, nil, err
	}

	// Stand up the integration branch before freezing the run, so an
	// integration phase never dispatches against a base that doesn't exist yet.
	if integ != "" {
		if s.mcpGitHub == nil {
			return pgstore.Orchestration{}, nil, errors.New("integration-target runs require the github client (auth.romaine.life token unmounted)")
		}
		base, err := s.mcpGitHub.DefaultBranch(ctx, ownerEmail, doc.RepoOwner, doc.RepoName)
		if err != nil {
			return pgstore.Orchestration{}, nil, fmt.Errorf("resolve default branch: %w", err)
		}
		if err := s.mcpGitHub.CreateBranch(ctx, ownerEmail, doc.RepoOwner, doc.RepoName, integ, base); err != nil {
			return pgstore.Orchestration{}, nil, fmt.Errorf("create integration branch %q: %w", integ, err)
		}
	}

	orch, phaseRows, err := s.orchStore.Create(ctx, pgstore.CreateOrchestrationRequest{
		OwnerEmail:        ownerEmail,
		ApproverEmail:     approver,
		RepoOwner:         doc.RepoOwner,
		RepoName:          doc.RepoName,
		IntegrationBranch: integ,
		State:             pgstore.OrchestrationApproved,
		Phases:            phases,
	})
	if err != nil {
		return pgstore.Orchestration{}, nil, err
	}

	// The kickoff: dispatch the initial ready (no-dependency) phases now rather
	// than waiting up to a reconcile interval. Best-effort — if it errs the run
	// is still durably approved and the reconcile backstop will bootstrap it.
	if err := s.orchestrations.Start(ctx, orch.OrchestrationID); err != nil {
		slog.Warn("orchestration kickoff: initial dispatch failed (reconcile will retry)",
			"orchestration_id", orch.OrchestrationID, "error", err)
	}
	if o, p, gerr := s.orchStore.GetWithPhases(ctx, orch.OrchestrationID); gerr == nil {
		orch, phaseRows = o, p
	}
	recordOrchestrationKickoff("started")
	slog.Info("orchestration started",
		"orchestration_id", orch.OrchestrationID, "owner", ownerEmail,
		"repo", orch.RepoOwner+"/"+orch.RepoName, "phases", len(phaseRows),
		"integration_branch", integ)
	return orch, phaseRows, nil
}

// handleInternalCreateOrchestration is the on-switch: POST an approved plan doc
// and Tank freezes it into a durable run and dispatches its initial phases. The
// run is owned by the caller's actor_email; the plan may name a separate
// approver.
func (s *appServer) handleInternalCreateOrchestration(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/orchestrations")
	if user == nil {
		return
	}
	if s.orchStore == nil || s.orchestrations == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	var doc orchestrationPlanDoc
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid plan document")
		return
	}
	orch, phases, err := s.createAndStartOrchestration(r.Context(), user.ActorEmail, doc)
	if err != nil {
		recordOrchestrationKickoff("rejected")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, orchestrationViewOf(orch, phases))
}

// handleInternalGetOrchestration returns a run's current state + phase DAG.
func (s *appServer) handleInternalGetOrchestration(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/orchestrations/{orchestration_id}")
	if user == nil {
		return
	}
	if s.orchStore == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("orchestration_id"))
	orch, phases, err := s.orchStore.GetWithPhases(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationNotFound) {
			writeError(w, http.StatusNotFound, "orchestration not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orchestrationViewOf(orch, phases))
}

// handleInternalApproveMergeOrchestration is the human's "go" at the terminal
// review gate: it promotes the run's integration branch to the repo's default
// branch and marks the run done. Guarded — the run must be awaiting_review with
// an integration branch and every phase a terminal success (a run parked on the
// gate because of a blocked phase is refused here; the human unblocks/fails it
// instead).
func (s *appServer) handleInternalApproveMergeOrchestration(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/orchestrations/{orchestration_id}/approve-merge")
	if user == nil {
		return
	}
	if s.orchStore == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("orchestration_id"))
	orch, phases, err := s.orchStore.GetWithPhases(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationNotFound) {
			writeError(w, http.StatusNotFound, "orchestration not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if orch.State != pgstore.OrchestrationAwaitingReview {
		writeError(w, http.StatusConflict, "run is not awaiting review (state="+string(orch.State)+")")
		return
	}
	if strings.TrimSpace(orch.IntegrationBranch) == "" {
		writeError(w, http.StatusConflict, "run has no integration branch to promote")
		return
	}
	if !allPhasesTerminalSuccess(phases) {
		writeError(w, http.StatusConflict, "run has phases that are not merged; resolve blocked phases before promoting")
		return
	}
	mergeSHA, err := s.promoteIntegrationToMain(r.Context(), orch)
	if err != nil {
		recordOrchestrationPromote("error")
		writeError(w, http.StatusConflict, "promote integration to main failed: "+err.Error())
		return
	}
	if _, uerr := s.updateOrchestrationState(r.Context(), id, pgstore.OrchestrationDone); uerr != nil {
		writeError(w, http.StatusInternalServerError, "integration merged but marking run done failed: "+uerr.Error())
		return
	}
	recordOrchestrationPromote("merged")
	recordOrchestrationRunDone()
	slog.Info("orchestration promoted to main",
		"orchestration_id", id, "merge_sha", mergeSHA, "by", user.ActorEmail)
	orch, phases, _ = s.orchStore.GetWithPhases(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"merged":       true,
		"merge_commit": mergeSHA,
		"orchestration": orchestrationViewOf(orch, phases),
	})
}

// handleInternalUnblockOrchestrationPhase is the human's "I resolved the
// blocker, resume that branch" lever for an escalated phase.
func (s *appServer) handleInternalUnblockOrchestrationPhase(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/orchestrations/phases/{phase_id}/unblock")
	if user == nil {
		return
	}
	if s.orchestrations == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	phaseID := strings.TrimSpace(r.PathValue("phase_id"))
	unblocked, err := s.orchestrations.signalUnblock(r.Context(), phaseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unblocked": unblocked})
}

// handleInternalFailOrchestration is the human's terminal "abandon this run"
// action — a non-hanging exit when a run cannot or should not finish.
func (s *appServer) handleInternalFailOrchestration(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/orchestrations/{orchestration_id}/fail")
	if user == nil {
		return
	}
	if s.orchStore == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("orchestration_id"))
	orch, err := s.updateOrchestrationState(r.Context(), id, pgstore.OrchestrationFailed)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationNotFound) {
			writeError(w, http.StatusNotFound, "orchestration not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("orchestration failed by human", "orchestration_id", id, "by", user.ActorEmail)
	writeJSON(w, http.StatusOK, map[string]any{"orchestration_id": orch.OrchestrationID, "state": string(orch.State)})
}

// handleInternalSignalOrchestrationBlocked is the spoke's escalation: a phase's
// spoke session reports it is genuinely stuck. Authenticated as the session pod
// itself (a spoke can only block its own phase), it moves the phase to blocked,
// pauses that DAG subtree, and notifies the human. A non-orchestration session
// is a benign no-op.
func (s *appServer) handleInternalSignalOrchestrationBlocked(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" || sessionID != caller.SessionID {
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.orchestrations == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration subsystem unavailable")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "the spoke session reported it is blocked"
	}
	blocked, err := s.orchestrations.signalBlocked(r.Context(), sessionID, reason)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": blocked})
}

// updateOrchestrationState reaches the run-level state setter on whichever
// concrete store is wired, without widening appOrchestrationStore for the two
// handlers that need it.
func (s *appServer) updateOrchestrationState(ctx context.Context, id string, state pgstore.OrchestrationState) (pgstore.Orchestration, error) {
	setter, ok := s.orchStore.(interface {
		UpdateState(context.Context, string, pgstore.OrchestrationState) (pgstore.Orchestration, error)
	})
	if !ok {
		return pgstore.Orchestration{}, errors.New("orchestration store cannot set state")
	}
	return setter.UpdateState(ctx, id, state)
}

func allPhasesTerminalSuccess(phases []pgstore.OrchestrationPhase) bool {
	if len(phases) == 0 {
		return false
	}
	for _, p := range phases {
		if p.Status != pgstore.PhaseMerged && p.Status != pgstore.PhaseSkipped {
			return false
		}
	}
	return true
}

// --- response projection -------------------------------------------------

type orchestrationPhaseView struct {
	PhaseID        string   `json:"phase_id"`
	Key            string   `json:"phase_key"`
	DependsOn      []string `json:"depends_on"`
	Target         string   `json:"target"`
	Status         string   `json:"status"`
	SpokeSessionID string   `json:"spoke_session_id,omitempty"`
	PRNumber       int      `json:"pr_number,omitempty"`
	PRURL          string   `json:"pr_url,omitempty"`
	MergeSHA       string   `json:"merge_sha,omitempty"`
	Detail         string   `json:"detail,omitempty"`
}

type orchestrationView struct {
	OrchestrationID   string                   `json:"orchestration_id"`
	State             string                   `json:"state"`
	Repo              string                   `json:"repo"`
	IntegrationBranch string                   `json:"integration_branch,omitempty"`
	OwnerEmail        string                   `json:"owner_email"`
	ApproverEmail     string                   `json:"approver_email,omitempty"`
	PhaseCount        int                      `json:"phase_count"`
	Phases            []orchestrationPhaseView `json:"phases"`
}

func orchestrationViewOf(orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase) orchestrationView {
	views := make([]orchestrationPhaseView, 0, len(phases))
	for _, p := range phases {
		deps := p.DependsOn
		if deps == nil {
			deps = []string{}
		}
		views = append(views, orchestrationPhaseView{
			PhaseID:        p.PhaseID,
			Key:            p.Key,
			DependsOn:      deps,
			Target:         string(p.Target),
			Status:         string(p.Status),
			SpokeSessionID: p.SpokeSessionID,
			PRNumber:       p.PRNumber,
			PRURL:          p.PRURL,
			MergeSHA:       p.MergeSHA,
			Detail:         p.Detail,
		})
	}
	return orchestrationView{
		OrchestrationID:   orch.OrchestrationID,
		State:             string(orch.State),
		Repo:              orch.RepoOwner + "/" + orch.RepoName,
		IntegrationBranch: orch.IntegrationBranch,
		OwnerEmail:        orch.OwnerEmail,
		ApproverEmail:     orch.ApproverEmail,
		PhaseCount:        orch.PhaseCount,
		Phases:            views,
	}
}
