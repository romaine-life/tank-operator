package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

const orchestrationEventStreamPageLimit = 100

type orchestrationRunStore interface {
	Create(context.Context, pgstore.CreateOrchestrationRequest) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	Approve(context.Context, string, string) (pgstore.Orchestration, error)
	GetWithPhases(ctx context.Context, orchestrationID string) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	ListByOwner(ctx context.Context, ownerEmail string) ([]pgstore.Orchestration, error)
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

type orchestrationRunJSON struct {
	ID                string                     `json:"id"`
	OrchestrationID   string                     `json:"orchestration_id"`
	OwnerEmail        string                     `json:"owner_email"`
	ApproverEmail     string                     `json:"approver_email"`
	Repo              string                     `json:"repo"`
	RepoOwner         string                     `json:"repo_owner"`
	RepoName          string                     `json:"repo_name"`
	IntegrationBranch string                     `json:"integration_branch"`
	State             pgstore.OrchestrationState `json:"state"`
	PlanHash          string                     `json:"plan_hash"`
	PhaseCount        int                        `json:"phase_count"`
	CreatedAt         string                     `json:"created_at"`
	UpdatedAt         string                     `json:"updated_at"`
	ApprovedAt        *string                    `json:"approved_at,omitempty"`
}

type orchestrationPhaseJSON struct {
	PhaseID         string              `json:"phase_id"`
	OrchestrationID string              `json:"orchestration_id"`
	Ordinal         int                 `json:"ordinal"`
	Key             string              `json:"key"`
	Brief           string              `json:"brief"`
	DependsOn       []string            `json:"depends_on"`
	Target          pgstore.PhaseTarget `json:"target"`
	Status          pgstore.PhaseStatus `json:"status"`
	SpokeSessionID  string              `json:"spoke_session_id"`
	PROwner         string              `json:"pr_owner"`
	PRName          string              `json:"pr_name"`
	PRNumber        int                 `json:"pr_number"`
	PRURL           string              `json:"pr_url"`
	MergeSHA        string              `json:"merge_sha"`
	CreatedAt       string              `json:"created_at"`
	UpdatedAt       string              `json:"updated_at"`
}

type orchestrationReadResponse struct {
	Orchestration orchestrationRunJSON     `json:"orchestration"`
	Phases        []orchestrationPhaseJSON `json:"phases"`
}

type orchestrationListResponse struct {
	Orchestrations []orchestrationRunJSON `json:"orchestrations"`
}

func orchestrationRunDTO(orch pgstore.Orchestration) orchestrationRunJSON {
	var approvedAt *string
	if orch.ApprovedAt != nil {
		value := orch.ApprovedAt.UTC().Format(time.RFC3339Nano)
		approvedAt = &value
	}
	return orchestrationRunJSON{
		ID:                orch.OrchestrationID,
		OrchestrationID:   orch.OrchestrationID,
		OwnerEmail:        orch.OwnerEmail,
		ApproverEmail:     orch.ApproverEmail,
		Repo:              orch.RepoOwner + "/" + orch.RepoName,
		RepoOwner:         orch.RepoOwner,
		RepoName:          orch.RepoName,
		IntegrationBranch: orch.IntegrationBranch,
		State:             orch.State,
		PlanHash:          orch.PlanHash,
		PhaseCount:        orch.PhaseCount,
		CreatedAt:         orch.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:         orch.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ApprovedAt:        approvedAt,
	}
}

func orchestrationPhaseDTO(phase pgstore.OrchestrationPhase) orchestrationPhaseJSON {
	return orchestrationPhaseJSON{
		PhaseID:         phase.PhaseID,
		OrchestrationID: phase.OrchestrationID,
		Ordinal:         phase.Ordinal,
		Key:             phase.Key,
		Brief:           phase.Brief,
		DependsOn:       phase.DependsOn,
		Target:          phase.Target,
		Status:          phase.Status,
		SpokeSessionID:  phase.SpokeSessionID,
		PROwner:         phase.PROwner,
		PRName:          phase.PRName,
		PRNumber:        phase.PRNumber,
		PRURL:           phase.PRURL,
		MergeSHA:        phase.MergeSHA,
		CreatedAt:       phase.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       phase.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *appServer) handleListOrchestrations(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.orchestrationRuns == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration store unavailable")
		return
	}
	orchs, err := s.orchestrationRuns.ListByOwner(r.Context(), orchestrationActorEmail(user))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list orchestrations: "+err.Error())
		return
	}
	out := make([]orchestrationRunJSON, 0, len(orchs))
	for _, orch := range orchs {
		out = append(out, orchestrationRunDTO(orch))
	}
	writeJSON(w, http.StatusOK, orchestrationListResponse{Orchestrations: out})
}

func (s *appServer) handleGetOrchestration(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.orchestrationRuns == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration store unavailable")
		return
	}
	orch, phases, err := s.orchestrationRuns.GetWithPhases(r.Context(), strings.TrimSpace(r.PathValue("orchestration_id")))
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationNotFound) {
			writeError(w, http.StatusNotFound, "orchestration not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "read orchestration: "+err.Error())
		return
	}
	if orch.OwnerEmail != orchestrationActorEmail(user) {
		writeError(w, http.StatusNotFound, "orchestration not found")
		return
	}
	outPhases := make([]orchestrationPhaseJSON, 0, len(phases))
	for _, phase := range phases {
		outPhases = append(outPhases, orchestrationPhaseDTO(phase))
	}
	writeJSON(w, http.StatusOK, orchestrationReadResponse{
		Orchestration: orchestrationRunDTO(orch),
		Phases:        outPhases,
	})
}

func (s *appServer) handleOrchestrationEventStream(w http.ResponseWriter, r *http.Request) {
	orchestrationID := strings.TrimSpace(r.PathValue("orchestration_id"))
	user, _, ok := s.requireBrowserStreamAuth(w, r, streamKindOrchestration, orchestrationID)
	if !ok {
		return
	}
	if s.orchestrationRuns == nil {
		writeError(w, http.StatusServiceUnavailable, "orchestration store unavailable")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	deadlineW := newSSEDeadlineWriter(w, flusher)
	w = deadlineW
	flusher = deadlineW

	eventStore := s.sessionEventStoreForScope(s.sessionScope)
	orch, phases, err := s.orchestrationRuns.GetWithPhases(r.Context(), orchestrationID)
	if err != nil {
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{"reason": "read_orchestration_failed", "detail": err.Error()})
		flusher.Flush()
		return
	}
	if orch.OwnerEmail != orchestrationActorEmail(user) {
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{"reason": "orchestration_not_found"})
		flusher.Flush()
		return
	}
	writeSSEJSONEvent(w, "ready", "", map[string]any{"orchestration_id": orchestrationID})
	writeOrchestrationSnapshotSSE(w, orch, phases)
	flusher.Flush()

	wakes := make(chan string, 32)
	unsubscribes := map[string]func(){}
	cursors := map[string]string{}
	defer func() {
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
	}()
	subscribePhases := func(phases []pgstore.OrchestrationPhase) {
		for _, phase := range phases {
			spoke := strings.TrimSpace(phase.SpokeSessionID)
			if spoke == "" {
				continue
			}
			if _, exists := unsubscribes[spoke]; exists {
				continue
			}
			storageKey := sessionmodel.SessionStorageKey(s.sessionScope, spoke)
			ch, unsubscribe, err := s.sessionBus.SubscribeWakesForStorageKey(r.Context(), storageKey, nil)
			if err != nil {
				slog.Warn("orchestration event stream spoke subscribe failed", "orchestration_id", orchestrationID, "spoke_session_id", spoke, "error", err)
				continue
			}
			unsubscribes[spoke] = unsubscribe
			go func(spoke string, ch <-chan struct{}) {
				for {
					select {
					case <-r.Context().Done():
						return
					case _, ok := <-ch:
						if !ok {
							return
						}
						select {
						case wakes <- spoke:
						case <-r.Context().Done():
							return
						}
					}
				}
			}(spoke, ch)
		}
	}
	subscribePhases(phases)

	heartbeat := time.NewTicker(sessionEventStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case spoke := <-wakes:
			pending := map[string]bool{spoke: true}
		drain:
			for {
				select {
				case next := <-wakes:
					pending[next] = true
				default:
					break drain
				}
			}
			orch, phases, err = s.orchestrationRuns.GetWithPhases(r.Context(), orchestrationID)
			if err != nil {
				writeSSEJSONEvent(w, "stream-error", "", map[string]any{"reason": "read_orchestration_failed", "detail": err.Error()})
				flusher.Flush()
				return
			}
			subscribePhases(phases)
			for wokeSpoke := range pending {
				if err := s.emitOrchestrationCIStatusEvents(r.Context(), w, eventStore, wokeSpoke, cursors, phases); err != nil {
					writeSSEJSONEvent(w, "stream-error", "", map[string]any{"reason": "event_page_failed", "detail": err.Error()})
					flusher.Flush()
					return
				}
			}
			writeOrchestrationSnapshotSSE(w, orch, phases)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (s *appServer) emitOrchestrationCIStatusEvents(ctx context.Context, w http.ResponseWriter, eventStore store.SessionEventStore, spoke string, cursors map[string]string, phases []pgstore.OrchestrationPhase) error {
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	phaseBySpoke := map[string]pgstore.OrchestrationPhase{}
	for _, phase := range phases {
		if strings.TrimSpace(phase.SpokeSessionID) != "" {
			phaseBySpoke[phase.SpokeSessionID] = phase
		}
	}
	for {
		page, err := eventStore.ListBySession(ctx, spoke, store.SessionEventCursor{AfterOrderKey: cursors[spoke]}, orchestrationEventStreamPageLimit)
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if stringMapField(event, "type") != string(conversation.EventCIStatusUpdated) {
				continue
			}
			phase := phaseBySpoke[spoke]
			writeSSEJSONEvent(w, "phase-status", eventOrderKeyForSSE(event), map[string]any{
				"spoke_session_id": spoke,
				"phase":            orchestrationPhaseDTO(phase),
				"ci_status":        event["payload"],
			})
		}
		if page.NextOrderKey != "" {
			cursors[spoke] = page.NextOrderKey
		}
		if !page.HasMore {
			return nil
		}
	}
}

func writeOrchestrationSnapshotSSE(w http.ResponseWriter, orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase) {
	outPhases := make([]orchestrationPhaseJSON, 0, len(phases))
	for _, phase := range phases {
		outPhases = append(outPhases, orchestrationPhaseDTO(phase))
	}
	writeSSEJSONEvent(w, "orchestration-snapshot", "", orchestrationReadResponse{
		Orchestration: orchestrationRunDTO(orch),
		Phases:        outPhases,
	})
}

func eventOrderKeyForSSE(event map[string]any) string {
	if v, ok := event["order_key"].(string); ok {
		return v
	}
	return ""
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
