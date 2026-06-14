package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const maxControlActionPayloadBytes = 16 << 10

type controlActionEventJSON struct {
	EventID       string          `json:"event_id"`
	InvocationID  string          `json:"invocation_id"`
	CreatedAt     string          `json:"created_at,omitempty"`
	OwnerEmail    string          `json:"owner_email,omitempty"`
	SessionScope  string          `json:"session_scope,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	SourceService string          `json:"source_service"`
	SourceTool    string          `json:"source_tool"`
	Action        string          `json:"action"`
	Status        string          `json:"status"`
	TargetKind    string          `json:"target_kind"`
	TargetRef     string          `json:"target_ref"`
	RepoOwner     string          `json:"repo_owner,omitempty"`
	RepoName      string          `json:"repo_name,omitempty"`
	PRNumber      *int            `json:"pr_number,omitempty"`
	ResultSHA     string          `json:"result_sha,omitempty"`
	Error         string          `json:"error,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type hotSwapVerificationRequest struct {
	Repo             string `json:"repo"`
	Branch           string `json:"branch"`
	SHA              string `json:"sha"`
	ArtifactKind     string `json:"artifact_kind,omitempty"`
	ValidationTarget string `json:"validation_target,omitempty"`
	SourceTool       string `json:"source_tool,omitempty"`
}

type hotSwapVerificationResponse struct {
	Allowed          bool     `json:"allowed"`
	Reasons          []string `json:"reasons,omitempty"`
	Repo             string   `json:"repo"`
	Branch           string   `json:"branch"`
	SHA              string   `json:"sha"`
	PRNumber         *int     `json:"pr_number,omitempty"`
	PublishVerified  bool     `json:"publish_verified"`
	CIVerified       bool     `json:"ci_verified"`
	MergeVerified    bool     `json:"merge_verified"`
	ArtifactKind     string   `json:"artifact_kind,omitempty"`
	ValidationTarget string   `json:"validation_target,omitempty"`
	SourceTool       string   `json:"source_tool,omitempty"`
}

type prLaneApprovalRequest struct {
	Note        string   `json:"note"`
	BranchNames []string `json:"branch_names,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Unlimited   bool     `json:"unlimited,omitempty"`
}

type prLaneAutoApprovalRequest struct {
	Repo        string   `json:"repo"`
	Limit       int      `json:"limit"`
	Unlimited   bool     `json:"unlimited"`
	BranchNames []string `json:"branch_names"`
	Reason      string   `json:"reason"`
}

type prLaneAuthorizationResponse struct {
	Allowed         bool     `json:"allowed"`
	Reasons         []string `json:"reasons,omitempty"`
	RequestEventID  string   `json:"request_event_id,omitempty"`
	ApprovalEventID string   `json:"approval_event_id,omitempty"`
	Repo            string   `json:"repo,omitempty"`
	LaneName        string   `json:"lane_name,omitempty"`
	Relationship    string   `json:"relationship,omitempty"`
	Base            string   `json:"base,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	ProposedBranch  string   `json:"proposed_branch,omitempty"`
	AutoApproved    bool     `json:"auto_approved,omitempty"`
}

func (s *appServer) handleInternalAppendControlAction(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/control-actions")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		recordControlActionEvent("", "", "", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		recordControlActionEvent("", "", "", "", "bad_request")
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	var body controlActionEventJSON
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	if err := dec.Decode(&body); err != nil {
		recordControlActionEvent("", "", "", "", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	event, err := controlActionFromJSON(body, user.ActorEmail, s.sessionScope, sessionID)
	if err != nil {
		recordControlActionEvent(body.SourceService, body.SourceTool, body.Action, body.Status, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := s.controlActions.Append(r.Context(), event)
	if err != nil {
		recordControlActionEvent(event.SourceService, event.SourceTool, event.Action, event.Status, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, controlActionToJSON(row, true))
}

func (s *appServer) handleListControlActions(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.OwnerEmail(), s.sessionScope, sessionID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]controlActionEventJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, controlActionToJSON(row, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *appServer) handleInternalGrantGitBreakGlass(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/git-break-glass/grants")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		recordControlActionEvent("", "", "", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	var body struct {
		Repo           string   `json:"repo"`
		TTLSeconds     int      `json:"ttl_seconds"`
		Operations     []string `json:"operations"`
		RequestEventID string   `json:"request_event_id"`
		Reason         string   `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	repo := strings.TrimSpace(body.Repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
		writeError(w, http.StatusBadRequest, "repo must be a GitHub slug like owner/name")
		return
	}
	ttl := body.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 24*3600 {
		ttl = 24 * 3600
	}
	operations := normalizeBreakGlassOperations(body.Operations)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttl) * time.Second)
	payload, _ := json.Marshal(map[string]any{
		"approved_by":      user.ActorEmail,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"ttl_seconds":      ttl,
		"operations":       operations,
		"request_event_id": strings.TrimSpace(body.RequestEventID),
		"reason":           strings.TrimSpace(body.Reason),
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-break-glass-grant-" + sessionID + "-" + randomHex(12),
		InvocationID:  "tank-break-glass-grant-" + randomHex(12),
		OwnerEmail:    user.ActorEmail,
		SessionScope:  s.sessionScope,
		SessionID:     sessionID,
		SourceService: "tank-operator",
		SourceTool:    "git_break_glass_approval",
		Action:        "github.break_glass.grant",
		Status:        "succeeded",
		TargetKind:    "github_repository",
		TargetRef:     "https://github.com/" + repo,
		RepoOwner:     owner,
		RepoName:      name,
		Payload:       payload,
	}
	row, err := s.controlActions.Append(r.Context(), event)
	if err != nil {
		recordControlActionEvent(event.SourceService, event.SourceTool, event.Action, event.Status, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":        true,
		"event_id":      row.EventID,
		"repo":          repo,
		"expires_at":    expiresAt.Format(time.RFC3339),
		"operations":    operations,
		"session_id":    sessionID,
		"session_scope": s.sessionScope,
	})
}

func (s *appServer) handleInternalGetGitBreakGlassGrant(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/git-break-glass/grant")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if sessionID == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "session_id and repo are required")
		return
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.ActorEmail, s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if row.Action != "github.break_glass.grant" || row.Status != "succeeded" {
			continue
		}
		if row.RepoOwner+"/"+row.RepoName != repo {
			continue
		}
		var payload struct {
			ExpiresAt  string   `json:"expires_at"`
			Operations []string `json:"operations"`
			Reason     string   `json:"reason"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
		if err != nil || !expiresAt.After(now) {
			continue
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"active":        true,
			"event_id":      row.EventID,
			"repo":          repo,
			"expires_at":    expiresAt.UTC().Format(time.RFC3339),
			"operations":    normalizeBreakGlassOperations(payload.Operations),
			"reason":        payload.Reason,
			"session_id":    sessionID,
			"session_scope": s.sessionScope,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false, "repo": repo, "session_id": sessionID})
}

func (s *appServer) handleApprovePRLaneRequest(w http.ResponseWriter, r *http.Request) {
	s.handlePRLaneDecision(w, r, "approve")
}

func (s *appServer) handleDenyPRLaneRequest(w http.ResponseWriter, r *http.Request) {
	s.handlePRLaneDecision(w, r, "deny")
}

func (s *appServer) handlePRLaneDecision(w http.ResponseWriter, r *http.Request, decision string) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	requestEventID := strings.TrimSpace(r.PathValue("request_event_id"))
	if sessionID == "" || requestEventID == "" {
		writeError(w, http.StatusBadRequest, "session_id and request_event_id are required")
		return
	}
	var body prLaneApprovalRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes)).Decode(&body); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.OwnerEmail(), s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	request, ok := findPendingPRLaneRequest(rows, requestEventID)
	if !ok {
		writeError(w, http.StatusNotFound, "pending PR lane request not found")
		return
	}
	var requestPayload struct {
		AllocationRequest bool     `json:"allocation_request"`
		LaneNames         []string `json:"lane_names"`
		ProposedBranches  []string `json:"proposed_branches"`
		RequestedCount    int      `json:"requested_count"`
		Unlimited         bool     `json:"unlimited"`
		Reason            string   `json:"reason"`
	}
	_ = json.Unmarshal(request.Payload, &requestPayload)
	if decision == "approve" && requestPayload.AllocationRequest {
		branchNames := normalizePRLaneBranchNames(append(append([]string{}, requestPayload.LaneNames...), requestPayload.ProposedBranches...), sessionID, request.RepoName)
		branchNames = uniqueStrings(branchNames)
		limit := requestPayload.RequestedCount
		unlimited := requestPayload.Unlimited
		overrideBranchNames := normalizePRLaneBranchNames(body.BranchNames, sessionID, request.RepoName)
		if len(overrideBranchNames) > 0 {
			branchNames = overrideBranchNames
		}
		if body.Limit > 0 {
			limit = body.Limit
		}
		if body.Unlimited {
			unlimited = true
		}
		if !unlimited {
			if limit <= 0 && len(branchNames) > 0 {
				limit = len(branchNames)
			}
			if limit <= 0 {
				limit = 10
			}
			if limit > 50 {
				limit = 50
			}
		}
		payload, _ := json.Marshal(map[string]any{
			"request_event_id": request.EventID,
			"request_payload":  json.RawMessage(request.Payload),
			"note":             strings.TrimSpace(body.Note),
			"approved_by":      user.Email,
			"repo":             strings.TrimSpace(request.RepoOwner + "/" + request.RepoName),
			"limit":            limit,
			"unlimited":        unlimited,
			"branch_names":     branchNames,
			"reason":           firstNonEmptyControlAction(strings.TrimSpace(requestPayload.Reason), strings.TrimSpace(body.Note)),
			"scope":            "session",
		})
		event := pgstore.ControlActionEvent{
			EventID:       "tank-pr-lane-auto-approve-" + sessionID + "-" + randomHex(12),
			InvocationID:  request.InvocationID,
			OwnerEmail:    user.OwnerEmail(),
			SessionScope:  s.sessionScope,
			SessionID:     sessionID,
			SourceService: "tank-operator",
			SourceTool:    "pr_lane_approval",
			Action:        "github.pr_lane.auto_approve",
			Status:        "succeeded",
			TargetKind:    request.TargetKind,
			TargetRef:     request.TargetRef,
			RepoOwner:     request.RepoOwner,
			RepoName:      request.RepoName,
			Payload:       payload,
		}
		row, err := s.controlActions.Append(r.Context(), event)
		if err != nil {
			recordControlActionEvent(event.SourceService, event.SourceTool, event.Action, event.Status, "store_error")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
		writeJSON(w, http.StatusCreated, controlActionToJSON(row, false))
		return
	}
	action := "github.pr_lane.approve"
	status := "succeeded"
	if decision == "deny" {
		action = "github.pr_lane.deny"
		status = "failed"
	}
	payload, _ := json.Marshal(map[string]any{
		"request_event_id": request.EventID,
		"request_payload":  json.RawMessage(request.Payload),
		"note":             strings.TrimSpace(body.Note),
		"decided_by":       user.Email,
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-pr-lane-" + decision + "-" + sessionID + "-" + randomHex(12),
		InvocationID:  request.InvocationID,
		OwnerEmail:    user.OwnerEmail(),
		SessionScope:  s.sessionScope,
		SessionID:     sessionID,
		SourceService: "tank-operator",
		SourceTool:    "pr_lane_approval",
		Action:        action,
		Status:        status,
		TargetKind:    request.TargetKind,
		TargetRef:     request.TargetRef,
		RepoOwner:     request.RepoOwner,
		RepoName:      request.RepoName,
		Payload:       payload,
	}
	row, err := s.controlActions.Append(r.Context(), event)
	if err != nil {
		recordControlActionEvent(event.SourceService, event.SourceTool, event.Action, event.Status, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, controlActionToJSON(row, false))
}

func (s *appServer) handleAutoApprovePRLanes(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	var body prLaneAutoApprovalRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	repo := strings.TrimSpace(body.Repo)
	owner, name := "", ""
	if repo != "" {
		var ok bool
		owner, name, ok = strings.Cut(repo, "/")
		if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
			writeError(w, http.StatusBadRequest, "repo must be empty or a GitHub slug like owner/name")
			return
		}
	}
	branchNames := normalizePRLaneBranchNames(body.BranchNames, sessionID, name)
	limit := body.Limit
	if !body.Unlimited {
		if limit <= 0 && len(branchNames) > 0 {
			limit = len(branchNames)
		}
		if limit <= 0 {
			limit = 10
		}
		if limit > 50 {
			limit = 50
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"repo":         repo,
		"limit":        limit,
		"unlimited":    body.Unlimited,
		"branch_names": branchNames,
		"reason":       strings.TrimSpace(body.Reason),
		"approved_by":  user.Email,
		"scope":        "session",
	})
	targetRef := "tank://session/" + sessionID + "/pr-lanes"
	if repo != "" {
		targetRef = "https://github.com/" + repo
	}
	event := pgstore.ControlActionEvent{
		EventID:       "tank-pr-lane-auto-approve-" + sessionID + "-" + randomHex(12),
		InvocationID:  "tank-pr-lane-auto-approve-" + randomHex(12),
		OwnerEmail:    user.OwnerEmail(),
		SessionScope:  s.sessionScope,
		SessionID:     sessionID,
		SourceService: "tank-operator",
		SourceTool:    "pr_lane_approval",
		Action:        "github.pr_lane.auto_approve",
		Status:        "succeeded",
		TargetKind:    "github_repository",
		TargetRef:     targetRef,
		RepoOwner:     owner,
		RepoName:      name,
		Payload:       payload,
	}
	row, err := s.controlActions.Append(r.Context(), event)
	if err != nil {
		recordControlActionEvent(event.SourceService, event.SourceTool, event.Action, event.Status, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, controlActionToJSON(row, false))
}

func (s *appServer) handleInternalGetPRLaneAutoApproval(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/pr-lane-auto-approval")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.ActorEmail, s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	laneName := strings.TrimSpace(r.URL.Query().Get("lane_name"))
	proposedBranch := strings.TrimSpace(r.URL.Query().Get("proposed_branch"))
	grant := activePRLaneAutoApproval(rows, repo, laneName, proposedBranch)
	writeJSON(w, http.StatusOK, map[string]any{
		"active":        grant.Active,
		"event_id":      grant.EventID,
		"limit":         grant.Limit,
		"unlimited":     grant.Unlimited,
		"remaining":     grant.Remaining,
		"branch_names":  grant.BranchNames,
		"repo":          repo,
		"session_id":    sessionID,
		"session_scope": s.sessionScope,
	})
}

func (s *appServer) handleInternalGetPRLaneAuthorization(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/pr-lane-requests/{request_event_id}/authorization")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	requestEventID := strings.TrimSpace(r.PathValue("request_event_id"))
	if sessionID == "" || requestEventID == "" {
		writeError(w, http.StatusBadRequest, "session_id and request_event_id are required")
		return
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.ActorEmail, s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := evaluatePRLaneAuthorization(rows, requestEventID)
	status := http.StatusOK
	if !resp.Allowed {
		status = http.StatusConflict
	}
	writeJSON(w, status, resp)
}

func (s *appServer) handleInternalVerifyHotSwap(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/hot-swap/verify")
	if user == nil {
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	var body hotSwapVerificationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	repo := strings.TrimSpace(body.Repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
		writeError(w, http.StatusBadRequest, "repo must be a GitHub slug like owner/name")
		return
	}
	branch := strings.TrimSpace(body.Branch)
	if branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	sha := strings.ToLower(strings.TrimSpace(body.SHA))
	if !isFullGitSHA(sha) {
		writeError(w, http.StatusBadRequest, "sha must be a full 40-character git SHA")
		return
	}
	rows, err := s.controlActions.ListBySession(r.Context(), user.ActorEmail, s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := evaluateHotSwapVerification(rows, owner, name, branch, sha)
	resp.Repo = repo
	resp.Branch = branch
	resp.SHA = sha
	resp.ArtifactKind = strings.TrimSpace(body.ArtifactKind)
	resp.ValidationTarget = strings.TrimSpace(body.ValidationTarget)
	resp.SourceTool = strings.TrimSpace(body.SourceTool)
	if resp.Allowed {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusConflict, resp)
}

func evaluatePRLaneAuthorization(rows []pgstore.ControlActionEvent, requestEventID string) prLaneAuthorizationResponse {
	var request pgstore.ControlActionEvent
	for _, row := range rows {
		if row.EventID == requestEventID && row.Action == "github.pr_lane.request" {
			request = row
			break
		}
	}
	if request.EventID == "" {
		return prLaneAuthorizationResponse{Allowed: false, Reasons: []string{"PR lane request not found"}}
	}
	resp := prLaneAuthorizationResponse{
		RequestEventID: request.EventID,
		Repo:           request.RepoOwner + "/" + request.RepoName,
	}
	var payload struct {
		LaneName            string `json:"lane_name"`
		Relationship        string `json:"relationship"`
		Base                string `json:"base"`
		Scope               string `json:"scope"`
		Reason              string `json:"reason"`
		ProposedBranch      string `json:"proposed_branch"`
		AutoApproved        bool   `json:"auto_approved"`
		AutoApprovalEventID string `json:"auto_approval_event_id"`
	}
	_ = json.Unmarshal(request.Payload, &payload)
	resp.LaneName = strings.TrimSpace(payload.LaneName)
	resp.Relationship = strings.TrimSpace(payload.Relationship)
	resp.Base = strings.TrimSpace(payload.Base)
	resp.Scope = strings.TrimSpace(payload.Scope)
	resp.Reason = strings.TrimSpace(payload.Reason)
	resp.ProposedBranch = strings.TrimSpace(payload.ProposedBranch)
	resp.AutoApproved = payload.AutoApproved || request.Status == "succeeded"
	if resp.AutoApproved {
		resp.ApprovalEventID = strings.TrimSpace(payload.AutoApprovalEventID)
	}
	if resp.LaneName == "" || resp.ProposedBranch == "" || request.RepoOwner == "" || request.RepoName == "" {
		resp.Reasons = append(resp.Reasons, "PR lane request is missing lane or repository metadata")
	}
	for _, row := range rows {
		if row.InvocationID != request.InvocationID {
			continue
		}
		switch row.Action {
		case "github.pr_lane.deny":
			resp.Reasons = append(resp.Reasons, "PR lane request was denied")
			return resp
		case "github.pr_lane.approve":
			if row.Status == "succeeded" {
				resp.ApprovalEventID = row.EventID
				resp.Allowed = len(resp.Reasons) == 0
				return resp
			}
		case "github.pr_lane.create":
			if row.Status == "succeeded" {
				resp.Reasons = append(resp.Reasons, "PR lane request has already been created")
				return resp
			}
		}
	}
	if resp.AutoApproved && len(resp.Reasons) == 0 {
		if resp.ApprovalEventID != "" && !prLaneAutoApprovalGrantAllows(rows, resp.ApprovalEventID, resp.Repo, resp.LaneName, resp.ProposedBranch) {
			resp.Reasons = append(resp.Reasons, "PR lane auto-approval no longer covers this branch")
			return resp
		}
		resp.Allowed = true
		return resp
	}
	resp.Reasons = append(resp.Reasons, "PR lane request is pending approval")
	return resp
}

func findPendingPRLaneRequest(rows []pgstore.ControlActionEvent, eventID string) (pgstore.ControlActionEvent, bool) {
	var request pgstore.ControlActionEvent
	for _, row := range rows {
		if row.Action == "github.pr_lane.request" && row.EventID == eventID && row.Status == "started" {
			request = row
			break
		}
	}
	if request.EventID == "" {
		return pgstore.ControlActionEvent{}, false
	}
	for _, row := range rows {
		if row.InvocationID != request.InvocationID {
			continue
		}
		switch row.Action {
		case "github.pr_lane.approve", "github.pr_lane.deny", "github.pr_lane.auto_approve":
			return pgstore.ControlActionEvent{}, false
		}
	}
	return request, true
}

type prLaneAutoApprovalGrant struct {
	Active      bool
	EventID     string
	Limit       int
	Unlimited   bool
	Remaining   int
	BranchNames []string
}

func activePRLaneAutoApproval(rows []pgstore.ControlActionEvent, repo, laneName, proposedBranch string) prLaneAutoApprovalGrant {
	requestedLane := normalizePRLaneBranchName(firstNonEmptyControlAction(laneName, proposedBranch), "", "")
	for _, row := range rows {
		if row.Action != "github.pr_lane.auto_approve" || row.Status != "succeeded" {
			continue
		}
		rowRepo := strings.TrimSpace(row.RepoOwner + "/" + row.RepoName)
		if row.RepoOwner == "" || row.RepoName == "" {
			rowRepo = ""
		}
		if repo != "" && rowRepo != "" && rowRepo != repo {
			continue
		}
		limit := 10
		var payload struct {
			Limit       int      `json:"limit"`
			Unlimited   bool     `json:"unlimited"`
			BranchNames []string `json:"branch_names"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		branchNames := normalizePRLaneBranchNames(payload.BranchNames, row.SessionID, row.RepoName)
		if requestedLane != "" && len(branchNames) > 0 && !stringInSlice(requestedLane, branchNames) {
			continue
		}
		if payload.Unlimited {
			return prLaneAutoApprovalGrant{
				Active:      true,
				EventID:     row.EventID,
				Unlimited:   true,
				BranchNames: branchNames,
			}
		}
		if payload.Limit > 0 {
			limit = payload.Limit
		} else if len(branchNames) > 0 {
			limit = len(branchNames)
		}
		used := countPRLaneAutoApprovalUses(rows, row.EventID)
		if used >= limit {
			continue
		}
		return prLaneAutoApprovalGrant{
			Active:      true,
			EventID:     row.EventID,
			Limit:       limit,
			Remaining:   limit - used,
			BranchNames: branchNames,
		}
	}
	return prLaneAutoApprovalGrant{}
}

func prLaneAutoApprovalGrantAllows(rows []pgstore.ControlActionEvent, eventID, repo, laneName, proposedBranch string) bool {
	if strings.TrimSpace(eventID) == "" {
		return false
	}
	requestedLane := normalizePRLaneBranchName(firstNonEmptyControlAction(laneName, proposedBranch), "", "")
	for _, row := range rows {
		if row.EventID != eventID || row.Action != "github.pr_lane.auto_approve" || row.Status != "succeeded" {
			continue
		}
		rowRepo := strings.TrimSpace(row.RepoOwner + "/" + row.RepoName)
		if repo != "" && rowRepo != "" && rowRepo != repo {
			return false
		}
		var payload struct {
			BranchNames []string `json:"branch_names"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		branchNames := normalizePRLaneBranchNames(payload.BranchNames, row.SessionID, row.RepoName)
		return requestedLane == "" || len(branchNames) == 0 || stringInSlice(requestedLane, branchNames)
	}
	return false
}

func countPRLaneAutoApprovalUses(rows []pgstore.ControlActionEvent, grantEventID string) int {
	used := 0
	for _, row := range rows {
		if row.Action != "github.pr_lane.request" || row.Status != "succeeded" {
			continue
		}
		if controlActionPayloadString(row.Payload, "auto_approval_event_id") == grantEventID {
			used++
		}
	}
	return used
}

func normalizePRLaneBranchNames(values []string, sessionID, repo string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if normalized := normalizePRLaneBranchName(value, sessionID, repo); normalized != "" {
			out = append(out, normalized)
		}
	}
	return uniqueStrings(out)
}

func normalizePRLaneBranchName(value, sessionID, repo string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "refs/heads/")
	if sessionID != "" && repo != "" {
		trimmed = strings.TrimPrefix(trimmed, "tank/session/"+sessionID+"/"+repo+"/")
	}
	if strings.Contains(trimmed, "/") {
		parts := strings.Split(trimmed, "/")
		trimmed = parts[len(parts)-1]
	}
	var b strings.Builder
	for _, ch := range trimmed {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			b.WriteRune(ch)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func firstNonEmptyControlAction(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isFullGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, ch := range value {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}

func evaluateHotSwapVerification(rows []pgstore.ControlActionEvent, owner, repo, branch, sha string) hotSwapVerificationResponse {
	resp := hotSwapVerificationResponse{}
	var sawPublish, sawCI, sawMerge bool
	for _, row := range rows {
		if row.RepoOwner != owner || row.RepoName != repo || !strings.EqualFold(row.ResultSHA, sha) {
			continue
		}
		switch row.Action {
		case "github.commit.push", "github.break_glass.push":
			if sawPublish {
				continue
			}
			if controlActionPayloadString(row.Payload, "branch") != branch {
				continue
			}
			sawPublish = true
			if row.Status == "succeeded" {
				resp.PublishVerified = true
			} else {
				resp.Reasons = append(resp.Reasons, "latest governed publish for this commit has not succeeded")
			}
		case "github.commit.ci":
			if sawCI {
				continue
			}
			sawCI = true
			if row.Status == "succeeded" {
				resp.CIVerified = true
			} else {
				reason := "latest CI observation for this commit is not green"
				if row.Error != "" {
					reason += ": " + row.Error
				}
				resp.Reasons = append(resp.Reasons, reason)
			}
		case "github.pull_request.mergeability":
			if sawMerge {
				continue
			}
			if controlActionPayloadString(row.Payload, "branch") != branch {
				continue
			}
			sawMerge = true
			resp.PRNumber = row.PRNumber
			if row.Status == "succeeded" {
				resp.MergeVerified = true
			} else {
				reason := "latest PR mergeability observation for this commit is not clean"
				if row.Error != "" {
					reason += ": " + row.Error
				}
				resp.Reasons = append(resp.Reasons, reason)
			}
		}
	}
	if !sawPublish {
		resp.Reasons = append(resp.Reasons, "no governed publish record exists for this commit on this branch")
	}
	if !sawCI {
		resp.Reasons = append(resp.Reasons, "no CI success record exists for this commit")
	}
	if !sawMerge {
		resp.Reasons = append(resp.Reasons, "no clean PR mergeability record exists for this commit on this branch")
	}
	resp.Allowed = resp.PublishVerified && resp.CIVerified && resp.MergeVerified
	return resp
}

func controlActionPayloadString(payload json.RawMessage, key string) string {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	return strings.TrimSpace(asString(body[key]))
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func normalizeBreakGlassOperations(in []string) []string {
	allowed := map[string]bool{"mint_full_git_token": true, "push_current_head": true, "apply_test_slot_hot_swap": true}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		op := strings.TrimSpace(raw)
		if allowed[op] && !seen[op] {
			out = append(out, op)
			seen[op] = true
		}
	}
	if len(out) == 0 {
		out = []string{"mint_full_git_token", "push_current_head", "apply_test_slot_hot_swap"}
	}
	return out
}

func randomHex(n int) string {
	if n <= 0 {
		n = 12
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func controlActionFromJSON(body controlActionEventJSON, ownerEmail, defaultScope, sessionID string) (pgstore.ControlActionEvent, error) {
	payload := body.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return pgstore.ControlActionEvent{}, errors.New("payload must be valid JSON")
	}
	status := strings.TrimSpace(body.Status)
	switch status {
	case "started", "succeeded", "failed":
	default:
		return pgstore.ControlActionEvent{}, errors.New("status must be one of started, succeeded, failed")
	}
	action := strings.TrimSpace(body.Action)
	switch action {
	case "github.pull_request.merge",
		"github.pull_request.rename",
		"github.pull_request.update_body",
		"github.pull_request.ready_for_review",
		"github.pull_request.open",
		"github.pull_request.mergeability",
		"github.pr_lane.request",
		"github.pr_lane.approve",
		"github.pr_lane.deny",
		"github.pr_lane.auto_approve",
		"github.pr_lane.create",
		"github.commit.write",
		"github.commit.push",
		"github.commit.ci",
		"github.break_glass.request",
		"github.break_glass.grant",
		"github.break_glass.token",
		"github.break_glass.push":
	default:
		return pgstore.ControlActionEvent{}, errors.New("unsupported control action")
	}
	return pgstore.ControlActionEvent{
		EventID:       body.EventID,
		InvocationID:  body.InvocationID,
		OwnerEmail:    ownerEmail,
		SessionScope:  defaultScope,
		SessionID:     sessionID,
		SourceService: body.SourceService,
		SourceTool:    body.SourceTool,
		Action:        action,
		Status:        status,
		TargetKind:    body.TargetKind,
		TargetRef:     body.TargetRef,
		RepoOwner:     body.RepoOwner,
		RepoName:      body.RepoName,
		PRNumber:      body.PRNumber,
		ResultSHA:     body.ResultSHA,
		Error:         body.Error,
		Payload:       payload,
	}, nil
}

func controlActionToJSON(row pgstore.ControlActionEvent, includeOwner bool) controlActionEventJSON {
	payload := json.RawMessage(row.Payload)
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	out := controlActionEventJSON{
		EventID:       row.EventID,
		InvocationID:  row.InvocationID,
		CreatedAt:     row.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		SessionScope:  row.SessionScope,
		SessionID:     row.SessionID,
		SourceService: row.SourceService,
		SourceTool:    row.SourceTool,
		Action:        row.Action,
		Status:        row.Status,
		TargetKind:    row.TargetKind,
		TargetRef:     row.TargetRef,
		RepoOwner:     row.RepoOwner,
		RepoName:      row.RepoName,
		PRNumber:      row.PRNumber,
		ResultSHA:     row.ResultSHA,
		Error:         row.Error,
		Payload:       payload,
	}
	if includeOwner {
		out.OwnerEmail = row.OwnerEmail
	}
	return out
}
