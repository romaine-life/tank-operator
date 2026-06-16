package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
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
	Note        string      `json:"note"`
	RepoScope   repoScope   `json:"repo_scope,omitempty"`
	BranchScope branchScope `json:"branch_scope,omitempty"`
}

type prLaneAutoApprovalRequest struct {
	RepoScope   repoScope   `json:"repo_scope,omitempty"`
	BranchScope branchScope `json:"branch_scope,omitempty"`
	Reason      string      `json:"reason"`
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

type repoScope struct {
	Kind  string   `json:"kind"`
	Repo  string   `json:"repo,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

type branchScope struct {
	Kind     string   `json:"kind"`
	Branches []string `json:"branches,omitempty"`
	Count    int      `json:"count,omitempty"`
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
	if !s.internalCallerMatchesSession(user, sessionID) {
		recordControlActionEvent("", "", "", "", "forbidden")
		writeError(w, http.StatusForbidden, "control action writes require caller session identity to match the target session")
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

// internalCallerMatchesSession authorizes a session-scoped internal write by the
// caller's *verified* per-session service identity. The service-principal subject
// is minted by auth.romaine.life from the pod's tank-operator/session-id annotation
// (the same identity nats-auth-callout trusts) and is unforgeable, so it is the sole
// authorization factor: a caller may write only its own session's ledger, on the
// backend whose scope its subject encodes. Caller-asserted request headers are not
// an authorization input — a self-reported session id adds nothing over the verified
// subject, and requiring one stranded every already-running session pod (the #1207
// regression that silently froze the control-action ledger on 2026-06-16).
func (s *appServer) internalCallerMatchesSession(user *auth.User, sessionID string) bool {
	return user != nil && s.serviceSubjectMatchesSession(user.Sub, sessionID)
}

// serviceSubjectMatchesSession reports whether the verified service-principal subject
// is this session's own identity *on this backend's scope*. Production sessions carry
// subject "svc:tank:<id>" and are valid only on the default-scope backend; test-slot
// sessions carry "svc:tank:slot-<n>-session-<id>" and are valid only on that slot's
// backend. Binding scope to the subject (not a caller header) keeps production and
// slot identities from crossing scopes even though session ids overlap.
func (s *appServer) serviceSubjectMatchesSession(sub, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	const subjectPrefix = "svc:tank:"
	value, ok := strings.CutPrefix(strings.TrimSpace(sub), subjectPrefix)
	if !ok {
		return false
	}
	value = strings.TrimSpace(value)
	if slotValue := slotServiceSubjectValue(s.localSessionScope(), sessionID); slotValue != "" {
		// Slot backend: only the matching slot-scoped subject is this session.
		return value == slotValue
	}
	// Default-scope backend: only the plain per-session subject is this session.
	return s.localSessionScope() == prodSessionScope && value == sessionID
}

func slotServiceSubjectValue(scope, sessionID string) string {
	const slotScopePrefix = "tank-operator-slot-"
	scope = normalizeSessionScope(scope)
	if !strings.HasPrefix(scope, slotScopePrefix) {
		return ""
	}
	slot := strings.TrimSpace(strings.TrimPrefix(scope, slotScopePrefix))
	if slot == "" {
		return ""
	}
	return "slot-" + slot + "-session-" + strings.TrimSpace(sessionID)
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

func (s *appServer) handleGetBreakGlassRequest(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	requestEventID := strings.TrimSpace(r.PathValue("request_event_id"))
	request, status, err := s.loadBreakGlassRequest(r.Context(), sessionID, requestEventID)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	decision, err := s.controlActions.BreakGlassDecisionForRequest(r.Context(), s.sessionScope, sessionID, request.EventID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{
		"request": controlActionToJSON(request, true),
		"pending": decision.EventID == "",
	}
	if decision.EventID != "" {
		resp["decision"] = controlActionToJSON(decision, true)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *appServer) handleAdminBreakGlassRequests(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	if s.controlActions == nil {
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	switch statusFilter {
	case "", "pending":
		statusFilter = "pending"
	case "recent", "all":
	default:
		writeError(w, http.StatusBadRequest, "status must be pending, recent, or all")
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
	if limit > 200 {
		limit = 200
	}
	queryLimit := limit
	if statusFilter == "pending" {
		queryLimit = 500
	}
	rows, err := s.controlActions.ListBreakGlassRequests(r.Context(), s.sessionScope, queryLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, request := range rows {
		if !isBreakGlassRequestAction(request.Action) {
			continue
		}
		decision, err := s.controlActions.BreakGlassDecisionForRequest(r.Context(), s.sessionScope, request.SessionID, request.EventID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		pending := decision.EventID == ""
		if statusFilter == "pending" && !pending {
			continue
		}
		item := map[string]any{
			"request": controlActionToJSON(request, true),
			"pending": pending,
		}
		if decision.EventID != "" {
			item["decision"] = controlActionToJSON(decision, true)
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requests":      items,
		"status":        statusFilter,
		"session_scope": s.sessionScope,
	})
}

func (s *appServer) handleApproveBreakGlassRequest(w http.ResponseWriter, r *http.Request) {
	s.handleBreakGlassDecision(w, r, "approve")
}

func (s *appServer) handleDenyBreakGlassRequest(w http.ResponseWriter, r *http.Request) {
	s.handleBreakGlassDecision(w, r, "deny")
}

type breakGlassDecisionBody struct {
	Note        string      `json:"note"`
	RepoScope   repoScope   `json:"repo_scope,omitempty"`
	BranchScope branchScope `json:"branch_scope,omitempty"`
}

func (s *appServer) handleBreakGlassDecision(w http.ResponseWriter, r *http.Request, decision string) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	if s.controlActions == nil {
		recordControlActionEvent("", "", "", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	requestEventID := strings.TrimSpace(r.PathValue("request_event_id"))
	request, status, err := s.loadBreakGlassRequest(r.Context(), sessionID, requestEventID)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var body breakGlassDecisionBody
	if r.Body != nil {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, http.ErrBodyReadAfterClose) {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	existing, err := s.controlActions.BreakGlassDecisionForRequest(r.Context(), s.sessionScope, sessionID, request.EventID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing.EventID != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "already_decided",
			"request":  controlActionToJSON(request, true),
			"decision": controlActionToJSON(existing, true),
		})
		return
	}
	if request.Status != "started" {
		writeError(w, http.StatusConflict, "break-glass request is not pending")
		return
	}

	if decision == "deny" {
		row, err := s.appendBreakGlassDeny(r.Context(), request, user.Email, body.Note)
		if err != nil {
			recordControlActionEvent("tank-operator", "break_glass_approval", row.Action, row.Status, "store_error")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
		writeJSON(w, http.StatusCreated, map[string]any{
			"status":   "denied",
			"request":  controlActionToJSON(request, true),
			"decision": controlActionToJSON(row, true),
		})
		return
	}

	row, expiresAt, agentNotification, err := s.appendBreakGlassGrantForRequest(r.Context(), request, user.Email, body)
	if err != nil {
		var validationErr breakGlassRequestValidationError
		if errors.As(err, &validationErr) {
			recordControlActionEvent("tank-operator", "break_glass_approval", row.Action, row.Status, "bad_request")
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		recordControlActionEvent("tank-operator", "break_glass_approval", row.Action, row.Status, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":             true,
		"status":             "approved",
		"event_id":           row.EventID,
		"request":            controlActionToJSON(request, true),
		"decision":           controlActionToJSON(row, true),
		"expires_at":         expiresAt.Format(time.RFC3339),
		"session_id":         sessionID,
		"session_scope":      s.sessionScope,
		"agent_notification": agentNotification,
	})
}

func (s *appServer) loadBreakGlassRequest(ctx context.Context, sessionID, requestEventID string) (pgstore.ControlActionEvent, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	requestEventID = strings.TrimSpace(requestEventID)
	if sessionID == "" || requestEventID == "" {
		return pgstore.ControlActionEvent{}, http.StatusBadRequest, errors.New("session_id and request_event_id are required")
	}
	request, err := s.controlActions.GetBySessionEvent(ctx, s.sessionScope, sessionID, requestEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgstore.ControlActionEvent{}, http.StatusNotFound, errors.New("break-glass request not found")
	}
	if err != nil {
		return pgstore.ControlActionEvent{}, http.StatusInternalServerError, err
	}
	if !isBreakGlassRequestAction(request.Action) {
		return pgstore.ControlActionEvent{}, http.StatusNotFound, errors.New("break-glass request not found")
	}
	return request, 0, nil
}

func isBreakGlassRequestAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "github.break_glass.request", "azure.break_glass.request":
		return true
	default:
		return false
	}
}

func isBreakGlassDecisionAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "github.break_glass.grant", "github.break_glass.deny", "azure.break_glass.grant", "azure.break_glass.deny":
		return true
	default:
		return false
	}
}

type breakGlassRequestValidationError struct {
	err error
}

func (e breakGlassRequestValidationError) Error() string {
	if e.err == nil {
		return "invalid break-glass request"
	}
	return e.err.Error()
}

func (e breakGlassRequestValidationError) Unwrap() error {
	return e.err
}

func invalidBreakGlassRequest(err error) error {
	return breakGlassRequestValidationError{err: err}
}

func (s *appServer) appendBreakGlassDeny(ctx context.Context, request pgstore.ControlActionEvent, decidedBy, note string) (pgstore.ControlActionEvent, error) {
	action := "github.break_glass.deny"
	targetKind := request.TargetKind
	targetRef := request.TargetRef
	if request.Action == "azure.break_glass.request" {
		action = "azure.break_glass.deny"
		targetKind = "azure_mcp"
		targetRef = "azure-personal"
	}
	payload, _ := json.Marshal(map[string]any{
		"request_event_id": request.EventID,
		"request_payload":  json.RawMessage(request.Payload),
		"note":             strings.TrimSpace(note),
		"decided_by":       strings.TrimSpace(decidedBy),
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-break-glass-deny-" + request.SessionID + "-" + randomHex(12),
		InvocationID:  request.InvocationID,
		OwnerEmail:    request.OwnerEmail,
		SessionScope:  request.SessionScope,
		SessionID:     request.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "break_glass_approval",
		Action:        action,
		Status:        "failed",
		TargetKind:    targetKind,
		TargetRef:     targetRef,
		RepoOwner:     request.RepoOwner,
		RepoName:      request.RepoName,
		Payload:       payload,
	}
	return s.controlActions.Append(ctx, event)
}

func (s *appServer) appendBreakGlassGrantForRequest(ctx context.Context, request pgstore.ControlActionEvent, approvedBy string, body breakGlassDecisionBody) (pgstore.ControlActionEvent, time.Time, map[string]any, error) {
	switch request.Action {
	case "github.break_glass.request":
		var payload struct {
			RepoScope   repoScope   `json:"repo_scope"`
			BranchScope branchScope `json:"branch_scope"`
			Reason      string      `json:"reason"`
			Operations  []string    `json:"operations"`
		}
		_ = json.Unmarshal(request.Payload, &payload)
		repoScope, err := normalizeRepoScope(payload.RepoScope, rowDefaultRepo(request))
		if err != nil {
			return pgstore.ControlActionEvent{Action: "github.break_glass.grant", Status: "succeeded"}, time.Time{}, nil, invalidBreakGlassRequest(err)
		}
		if strings.TrimSpace(body.RepoScope.Kind) != "" {
			repoScope, err = normalizeRepoScope(body.RepoScope, "")
			if err != nil {
				return pgstore.ControlActionEvent{Action: "github.break_glass.grant", Status: "succeeded"}, time.Time{}, nil, invalidBreakGlassRequest(err)
			}
		}
		branchScope := payload.BranchScope
		if strings.TrimSpace(body.BranchScope.Kind) != "" {
			branchScope = body.BranchScope
		}
		branchScope, err = normalizeBranchScope(branchScope, request.SessionID, singleRepoName(repoScopeRepos(repoScope), repoScope.Kind == "all_repos"))
		if err != nil {
			return pgstore.ControlActionEvent{Action: "github.break_glass.grant", Status: "succeeded"}, time.Time{}, nil, invalidBreakGlassRequest(err)
		}
		repos := repoScopeRepos(repoScope)
		allRepos := repoScope.Kind == "all_repos"
		row, expiresAt, err := s.appendGitBreakGlassGrant(ctx, gitBreakGlassGrantInput{
			SessionID:      request.SessionID,
			OwnerEmail:     request.OwnerEmail,
			RepoOwner:      singleRepoOwner(repos, allRepos),
			RepoName:       singleRepoName(repos, allRepos),
			RepoScope:      repoScope,
			BranchScope:    branchScope,
			TTLSeconds:     0,
			Operations:     payload.Operations,
			RequestEventID: request.EventID,
			Reason:         firstNonEmptyControlAction(strings.TrimSpace(body.Note), strings.TrimSpace(payload.Reason)),
			ApprovedBy:     approvedBy,
		})
		agentNotification := map[string]any{"delivered": false}
		if err != nil {
			return row, expiresAt, agentNotification, err
		}
		if notifyResp, status, detail := s.enqueueGitBreakGlassApprovalTurn(ctx, row, expiresAt); status != 0 {
			agentNotification["error"] = strings.TrimSpace(detail)
			recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "notify_error")
			slog.Warn("git break-glass approval grant persisted but agent notification turn failed",
				"session_id", request.SessionID, "grant_event_id", row.EventID, "status", status, "detail", detail)
		} else {
			agentNotification["delivered"] = true
			if turnID := turnIDFromEnqueueResponse(notifyResp); turnID != "" {
				agentNotification["turn_id"] = turnID
			}
		}
		return row, expiresAt, agentNotification, nil
	case "azure.break_glass.request":
		var payload struct {
			Reason     string   `json:"reason"`
			Operations []string `json:"operations"`
		}
		_ = json.Unmarshal(request.Payload, &payload)
		row, expiresAt, err := s.appendAzureBreakGlassGrant(ctx, azureBreakGlassGrantInput{
			SessionID:      request.SessionID,
			OwnerEmail:     request.OwnerEmail,
			TTLSeconds:     0,
			Operations:     payload.Operations,
			RequestEventID: request.EventID,
			Reason:         firstNonEmptyControlAction(strings.TrimSpace(body.Note), strings.TrimSpace(payload.Reason)),
			ApprovedBy:     approvedBy,
		})
		agentNotification := map[string]any{"delivered": false}
		if err != nil {
			return row, expiresAt, agentNotification, err
		}
		if notifyResp, status, detail := s.enqueueAzureBreakGlassApprovalTurn(ctx, row, expiresAt); status != 0 {
			agentNotification["error"] = strings.TrimSpace(detail)
			recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "notify_error")
			slog.Warn("azure break-glass approval grant persisted but agent activation turn failed",
				"session_id", request.SessionID, "grant_event_id", row.EventID, "status", status, "detail", detail)
		} else {
			agentNotification["delivered"] = true
			if turnID := turnIDFromEnqueueResponse(notifyResp); turnID != "" {
				agentNotification["turn_id"] = turnID
			}
		}
		return row, expiresAt, agentNotification, nil
	default:
		return pgstore.ControlActionEvent{}, time.Time{}, nil, invalidBreakGlassRequest(errors.New("unsupported break-glass request"))
	}
}

type gitBreakGlassGrantInput struct {
	SessionID      string
	OwnerEmail     string
	RepoOwner      string
	RepoName       string
	RepoScope      repoScope
	BranchScope    branchScope
	TTLSeconds     int
	Operations     []string
	RequestEventID string
	Reason         string
	ApprovedBy     string
}

type azureBreakGlassGrantInput struct {
	SessionID      string
	OwnerEmail     string
	TTLSeconds     int
	Operations     []string
	RequestEventID string
	Reason         string
	ApprovedBy     string
}

// appendGitBreakGlassGrant writes a github.break_glass.grant control-action and
// returns the persisted row plus its computed expiry. Tank's approval route owns
// the human decision; the agent-side grant lookup reads this durable event.
func (s *appServer) appendGitBreakGlassGrant(ctx context.Context, in gitBreakGlassGrantInput) (pgstore.ControlActionEvent, time.Time, error) {
	ttl := in.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 24*3600 {
		ttl = 24 * 3600
	}
	operations := normalizeBreakGlassOperations(in.Operations)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttl) * time.Second)
	repo := in.RepoOwner + "/" + in.RepoName
	resolvedRepoScope := in.RepoScope
	if strings.TrimSpace(resolvedRepoScope.Kind) == "" && in.RepoOwner != "" && in.RepoName != "" {
		resolvedRepoScope = repoScope{Kind: "current_repo", Repo: repo}
	}
	resolvedBranchScope := in.BranchScope
	if strings.TrimSpace(resolvedBranchScope.Kind) == "" {
		resolvedBranchScope = branchScope{Kind: "unlimited"}
	}
	payload, _ := json.Marshal(map[string]any{
		"approved_by":      strings.TrimSpace(in.ApprovedBy),
		"expires_at":       expiresAt.Format(time.RFC3339),
		"ttl_seconds":      ttl,
		"operations":       operations,
		"request_event_id": strings.TrimSpace(in.RequestEventID),
		"reason":           strings.TrimSpace(in.Reason),
		"repo_scope":       resolvedRepoScope,
		"branch_scope":     resolvedBranchScope,
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-break-glass-grant-" + in.SessionID + "-" + randomHex(12),
		InvocationID:  "tank-break-glass-grant-" + randomHex(12),
		OwnerEmail:    in.OwnerEmail,
		SessionScope:  s.sessionScope,
		SessionID:     in.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "git_break_glass_approval",
		Action:        "github.break_glass.grant",
		Status:        "succeeded",
		TargetKind:    "github_repository",
		TargetRef:     repoScopeTargetRef(in.SessionID, resolvedRepoScope, "git-break-glass"),
		RepoOwner:     in.RepoOwner,
		RepoName:      in.RepoName,
		Payload:       payload,
	}
	row, err := s.controlActions.Append(ctx, event)
	return row, expiresAt, err
}

func (s *appServer) enqueueGitBreakGlassApprovalTurn(ctx context.Context, grant pgstore.ControlActionEvent, expiresAt time.Time) (map[string]any, int, string) {
	if s == nil || s.sessionBus == nil || s.mgr == nil {
		return nil, http.StatusServiceUnavailable, "session turn enqueue unavailable"
	}
	sessionID := strings.TrimSpace(grant.SessionID)
	ownerEmail := strings.TrimSpace(grant.OwnerEmail)
	if sessionID == "" || ownerEmail == "" {
		return nil, http.StatusBadRequest, "grant missing session or owner"
	}
	seed := controlActionPayloadString(grant.Payload, "request_event_id")
	if seed == "" {
		seed = grant.EventID
	}
	seed = sessionID + ":" + seed
	prompt := gitBreakGlassApprovalPrompt(grant, expiresAt)
	return s.enqueueSDKTurn(ctx, ownerEmail, sessionID, sdkTurnRequest{
		ClientNonce:  gitBreakGlassApprovalTurnNonce(seed),
		RequireNonce: true,
		Prompt:       prompt,
		DisplayText:  gitBreakGlassApprovalDisplayText(grant, expiresAt),
		Source:       string(conversation.TurnSubmittedSourceBreakGlassApproval),
		CreatedAt:    time.Now().UTC(),
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
}

func gitBreakGlassApprovalTurnNonce(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = randomHex(12)
	}
	sum := sha256.Sum256([]byte(seed))
	return "turn_breakglass_approved_" + hex.EncodeToString(sum[:12])
}

func gitBreakGlassApprovalDisplayText(grant pgstore.ControlActionEvent, expiresAt time.Time) string {
	repo := strings.Trim(strings.TrimSpace(grant.RepoOwner+"/"+grant.RepoName), "/")
	if repo == "" {
		repo = "the approved repo scope"
	}
	expiry := ""
	if !expiresAt.IsZero() {
		expiry = " The grant expires at " + expiresAt.UTC().Format(time.RFC3339) + "."
	}
	return "Break-glass approval granted for " + repo + "." + expiry
}

func gitBreakGlassApprovalPrompt(grant pgstore.ControlActionEvent, expiresAt time.Time) string {
	lines := []string{
		"System message: Your GitHub break-glass request was approved by the user.",
		gitBreakGlassApprovalDisplayText(grant, expiresAt),
		"Call request_git_break_glass again to activate the tank-git-break-glass MCP server for this session, then continue with the approved work.",
	}
	if reason := controlActionPayloadString(grant.Payload, "reason"); reason != "" {
		lines = append(lines, "Approval reason: "+reason)
	}
	return strings.Join(lines, "\n")
}

func (s *appServer) appendAzureBreakGlassGrant(ctx context.Context, in azureBreakGlassGrantInput) (pgstore.ControlActionEvent, time.Time, error) {
	ttl := in.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 24*3600 {
		ttl = 24 * 3600
	}
	operations := normalizeAzureBreakGlassOperations(in.Operations)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttl) * time.Second)
	payload, _ := json.Marshal(map[string]any{
		"approved_by":      strings.TrimSpace(in.ApprovedBy),
		"expires_at":       expiresAt.Format(time.RFC3339),
		"ttl_seconds":      ttl,
		"operations":       operations,
		"request_event_id": strings.TrimSpace(in.RequestEventID),
		"reason":           strings.TrimSpace(in.Reason),
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-azure-break-glass-grant-" + in.SessionID + "-" + randomHex(12),
		InvocationID:  "tank-azure-break-glass-grant-" + randomHex(12),
		OwnerEmail:    in.OwnerEmail,
		SessionScope:  s.sessionScope,
		SessionID:     in.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "azure_break_glass_approval",
		Action:        "azure.break_glass.grant",
		Status:        "succeeded",
		TargetKind:    "azure_mcp",
		TargetRef:     "azure-personal",
		Payload:       payload,
	}
	row, err := s.controlActions.Append(ctx, event)
	return row, expiresAt, err
}

// azureMCPBreakGlassServerName / URL identify the azure-personal MCP listener
// the mcp-auth-proxy always runs on 127.0.0.1:9991. On grant the runner
// auto-surfaces this server (the activation half of break-glass); the entry
// alone grants nothing — mcp-azure-personal re-checks the grant on every call.
const (
	azureMCPBreakGlassServerName = "azure-personal"
	azureMCPBreakGlassServerURL  = "http://127.0.0.1:9991/"
)

// enqueueAzureBreakGlassApprovalTurn enqueues the approval submit_turn carrying
// the azure-personal MCP-activation payload. The runner registers the server
// and rebuilds at the next idle boundary so the tools surface for this turn —
// the B-auto path that removes the agent's need to re-request. Mirrors
// enqueueGitBreakGlassApprovalTurn (no repo dimension; adds MCP activation).
func (s *appServer) enqueueAzureBreakGlassApprovalTurn(ctx context.Context, grant pgstore.ControlActionEvent, expiresAt time.Time) (map[string]any, int, string) {
	if s == nil || s.sessionBus == nil || s.mgr == nil {
		return nil, http.StatusServiceUnavailable, "session turn enqueue unavailable"
	}
	sessionID := strings.TrimSpace(grant.SessionID)
	ownerEmail := strings.TrimSpace(grant.OwnerEmail)
	if sessionID == "" || ownerEmail == "" {
		return nil, http.StatusBadRequest, "grant missing session or owner"
	}
	seed := controlActionPayloadString(grant.Payload, "request_event_id")
	if seed == "" {
		seed = grant.EventID
	}
	// Distinct seed namespace from git so the deterministic nonce never collides
	// with a git approval turn for the same session.
	seed = sessionID + ":azure:" + seed
	return s.enqueueSDKTurn(ctx, ownerEmail, sessionID, sdkTurnRequest{
		ClientNonce:     gitBreakGlassApprovalTurnNonce(seed),
		RequireNonce:    true,
		Prompt:          azureBreakGlassApprovalPrompt(grant, expiresAt),
		DisplayText:     azureBreakGlassApprovalDisplayText(grant, expiresAt),
		Source:          string(conversation.TurnSubmittedSourceBreakGlassApproval),
		CreatedAt:       time.Now().UTC(),
		AuthorKind:      string(conversation.AuthorKindSystem),
		MCPActivateName: azureMCPBreakGlassServerName,
		MCPActivateURL:  azureMCPBreakGlassServerURL,
	})
}

func azureBreakGlassApprovalDisplayText(_ pgstore.ControlActionEvent, expiresAt time.Time) string {
	expiry := ""
	if !expiresAt.IsZero() {
		expiry = " The grant expires at " + expiresAt.UTC().Format(time.RFC3339) + "."
	}
	return "Azure break-glass approved: the azure-personal MCP tools are now available for this session." + expiry
}

func azureBreakGlassApprovalPrompt(grant pgstore.ControlActionEvent, expiresAt time.Time) string {
	lines := []string{
		"System message: Your Azure break-glass request was approved by the user.",
		azureBreakGlassApprovalDisplayText(grant, expiresAt),
		"The azure-personal tools (pg_query, keyvault_get_secret, etc.) are now active in your MCP registry — call them directly; no further request is needed. They re-lock automatically when the grant expires.",
	}
	if reason := controlActionPayloadString(grant.Payload, "reason"); reason != "" {
		lines = append(lines, "Approval reason: "+reason)
	}
	return strings.Join(lines, "\n")
}

// splitRepoSlug parses a trimmed "owner/name" GitHub slug.
func splitRepoSlug(repo string) (string, string, bool) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if !ok || owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
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
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
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
		var payload struct {
			ExpiresAt   string      `json:"expires_at"`
			Operations  []string    `json:"operations"`
			Reason      string      `json:"reason"`
			RepoScope   repoScope   `json:"repo_scope"`
			BranchScope branchScope `json:"branch_scope"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		repoScope, err := normalizeRepoScope(payload.RepoScope, rowDefaultRepo(row))
		if err != nil || !repoScopeCoversRepo(repoScope, repo) {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
		if err != nil || !expiresAt.After(now) {
			continue
		}
		branchScope, err := normalizeBranchScope(payload.BranchScope, row.SessionID, singleRepoName(repoScopeRepos(repoScope), repoScope.Kind == "all_repos"))
		if err != nil {
			continue
		}
		branchLimit := branchScope.Count
		remainingBranches := 0
		if branchScope.Kind == "count" {
			usedBranches := countBreakGlassGrantBranches(rows, row.EventID)
			if usedBranches >= branchLimit {
				continue
			}
			remainingBranches = branchLimit - usedBranches
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"active":             true,
			"event_id":           row.EventID,
			"repo":               repo,
			"repo_scope":         repoScope,
			"branch_scope":       branchScope,
			"expires_at":         expiresAt.UTC().Format(time.RFC3339),
			"operations":         normalizeBreakGlassOperations(payload.Operations),
			"reason":             payload.Reason,
			"remaining_branches": remainingBranches,
			"session_id":         sessionID,
			"session_scope":      s.sessionScope,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false, "repo": repo, "session_id": sessionID})
}

func (s *appServer) handleAdminGrantGitBreakGlass(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.controlActions == nil {
		recordControlActionEvent("", "", "", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if sessionScope != s.localSessionScope() {
		writeError(w, http.StatusBadRequest, "break-glass grants must be issued from the target session scope")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	info, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var body struct {
		Repo           string      `json:"repo"`
		RepoScope      repoScope   `json:"repo_scope"`
		BranchScope    branchScope `json:"branch_scope"`
		TTLSeconds     int         `json:"ttl_seconds"`
		Operations     []string    `json:"operations"`
		RequestEventID string      `json:"request_event_id"`
		Reason         string      `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	repoScope, err := normalizeRepoScope(body.RepoScope, body.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	branchScope, err := normalizeBranchScope(body.BranchScope, sessionID, singleRepoName(repoScopeRepos(repoScope), repoScope.Kind == "all_repos"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repos := repoScopeRepos(repoScope)
	allRepos := repoScope.Kind == "all_repos"
	row, expiresAt, err := s.appendGitBreakGlassGrant(r.Context(), gitBreakGlassGrantInput{
		SessionID:      sessionID,
		OwnerEmail:     info.Owner,
		RepoOwner:      singleRepoOwner(repos, allRepos),
		RepoName:       singleRepoName(repos, allRepos),
		RepoScope:      repoScope,
		BranchScope:    branchScope,
		TTLSeconds:     body.TTLSeconds,
		Operations:     body.Operations,
		RequestEventID: body.RequestEventID,
		Reason:         body.Reason,
		ApprovedBy:     user.Email,
	})
	if err != nil {
		recordControlActionEvent("tank-operator", "git_break_glass_approval", "github.break_glass.grant", "succeeded", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agentNotification := map[string]any{"delivered": false}
	if notifyResp, status, detail := s.enqueueGitBreakGlassApprovalTurn(r.Context(), row, expiresAt); status != 0 {
		recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "notify_error")
		slog.Warn("admin git break-glass grant persisted but agent notification turn failed",
			"session_id", sessionID, "grant_event_id", row.EventID, "status", status, "detail", detail)
		writeError(w, http.StatusInternalServerError, "git break-glass grant persisted but agent notification failed: "+strings.TrimSpace(detail))
		return
	} else {
		agentNotification["delivered"] = true
		if turnID := turnIDFromEnqueueResponse(notifyResp); turnID != "" {
			agentNotification["turn_id"] = turnID
		}
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":             true,
		"event_id":           row.EventID,
		"repo":               strings.Trim(strings.TrimSpace(row.RepoOwner+"/"+row.RepoName), "/"),
		"repo_scope":         repoScope,
		"branch_scope":       branchScope,
		"expires_at":         expiresAt.Format(time.RFC3339),
		"operations":         normalizeBreakGlassOperations(body.Operations),
		"session_id":         sessionID,
		"session_scope":      sessionScope,
		"owner_email":        info.Owner,
		"agent_notification": agentNotification,
	})
}

func (s *appServer) handleAdminGrantAzureBreakGlass(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.controlActions == nil {
		recordControlActionEvent("", "", "", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "control action store unavailable")
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if sessionScope != s.localSessionScope() {
		writeError(w, http.StatusBadRequest, "break-glass grants must be issued from the target session scope")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	info, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var body struct {
		TTLSeconds     int      `json:"ttl_seconds"`
		Operations     []string `json:"operations"`
		RequestEventID string   `json:"request_event_id"`
		Reason         string   `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	row, expiresAt, err := s.appendAzureBreakGlassGrant(r.Context(), azureBreakGlassGrantInput{
		SessionID:      sessionID,
		OwnerEmail:     info.Owner,
		TTLSeconds:     body.TTLSeconds,
		Operations:     body.Operations,
		RequestEventID: body.RequestEventID,
		Reason:         body.Reason,
		ApprovedBy:     user.Email,
	})
	if err != nil {
		recordControlActionEvent("tank-operator", "azure_break_glass_approval", "azure.break_glass.grant", "succeeded", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	agentNotification := map[string]any{"delivered": false}
	if notifyResp, status, detail := s.enqueueAzureBreakGlassApprovalTurn(r.Context(), row, expiresAt); status != 0 {
		recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "notify_error")
		slog.Warn("admin azure break-glass grant persisted but agent activation turn failed",
			"session_id", sessionID, "grant_event_id", row.EventID, "status", status, "detail", detail)
	} else {
		agentNotification["delivered"] = true
		if turnID := turnIDFromEnqueueResponse(notifyResp); turnID != "" {
			agentNotification["turn_id"] = turnID
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":             true,
		"event_id":           row.EventID,
		"resource":           "azure-personal",
		"expires_at":         expiresAt.Format(time.RFC3339),
		"operations":         normalizeAzureBreakGlassOperations(body.Operations),
		"session_id":         sessionID,
		"session_scope":      sessionScope,
		"owner_email":        info.Owner,
		"agent_notification": agentNotification,
	})
}

// handleInternalGetAzureBreakGlassGrant returns the active azure-personal MCP
// break-glass grant for a session, if any. mcp-azure-personal calls this on
// every tool list/call (short-cached) to decide whether to serve azure tools.
// Mirrors handleInternalGetGitBreakGlassGrant without the repo dimension.
func (s *appServer) handleInternalGetAzureBreakGlassGrant(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/azure-break-glass/grant")
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
	rows, err := s.controlActions.ListBySession(r.Context(), user.ActorEmail, s.sessionScope, sessionID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if row.Action != "azure.break_glass.grant" || row.Status != "succeeded" {
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
			"resource":      "azure-personal",
			"expires_at":    expiresAt.UTC().Format(time.RFC3339),
			"operations":    normalizeAzureBreakGlassOperations(payload.Operations),
			"reason":        payload.Reason,
			"session_id":    sessionID,
			"session_scope": s.sessionScope,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false, "resource": "azure-personal", "session_id": sessionID})
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
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
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
		AllocationRequest bool        `json:"allocation_request"`
		RepoScope         repoScope   `json:"repo_scope"`
		BranchScope       branchScope `json:"branch_scope"`
		Reason            string      `json:"reason"`
	}
	_ = json.Unmarshal(request.Payload, &requestPayload)
	if decision == "approve" && requestPayload.AllocationRequest {
		requestRepoScope, err := normalizeRepoScope(requestPayload.RepoScope, rowDefaultRepo(request))
		if err != nil {
			writeError(w, http.StatusBadRequest, "PR lane request has invalid repo_scope")
			return
		}
		requestBranchScope, err := normalizeBranchScope(requestPayload.BranchScope, sessionID, request.RepoName)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PR lane request has invalid branch_scope")
			return
		}
		resolvedRepoScope := requestRepoScope
		resolvedBranchScope := requestBranchScope
		if body.BranchScope.Kind != "" {
			resolvedBranchScope, err = normalizeBranchScope(body.BranchScope, sessionID, request.RepoName)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if body.RepoScope.Kind != "" {
			resolvedRepoScope, err = normalizeRepoScope(body.RepoScope, "")
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		repos := repoScopeRepos(resolvedRepoScope)
		allRepos := resolvedRepoScope.Kind == "all_repos"
		payload, _ := json.Marshal(map[string]any{
			"request_event_id": request.EventID,
			"request_payload":  json.RawMessage(request.Payload),
			"note":             strings.TrimSpace(body.Note),
			"approved_by":      user.Email,
			"repo_scope":       resolvedRepoScope,
			"branch_scope":     resolvedBranchScope,
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
			TargetRef:     repoScopeTargetRef(sessionID, resolvedRepoScope, "pr-lanes"),
			RepoOwner:     singleRepoOwner(repos, allRepos),
			RepoName:      singleRepoName(repos, allRepos),
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	resolvedRepoScope, err := normalizeRepoScope(body.RepoScope, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repos := repoScopeRepos(resolvedRepoScope)
	allRepos := resolvedRepoScope.Kind == "all_repos"
	owner, name := singleRepoOwner(repos, allRepos), singleRepoName(repos, allRepos)
	resolvedBranchScope, err := normalizeBranchScope(body.BranchScope, sessionID, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"repo_scope":   resolvedRepoScope,
		"branch_scope": resolvedBranchScope,
		"reason":       strings.TrimSpace(body.Reason),
		"approved_by":  user.Email,
		"scope":        "session",
	})
	targetRef := repoScopeTargetRef(sessionID, resolvedRepoScope, "pr-lanes")
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
		"repos":         grant.Repos,
		"all_repos":     grant.AllRepos,
		"repo_scope":    grant.RepoScope,
		"branch_scope":  grant.BranchScope,
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
	Repos       []string
	AllRepos    bool
	RepoScope   repoScope
	BranchScope branchScope
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
			RepoScope   repoScope   `json:"repo_scope"`
			BranchScope branchScope `json:"branch_scope"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		repoScope, err := normalizeRepoScope(payload.RepoScope, rowDefaultRepo(row))
		if err != nil {
			continue
		}
		if !repoScopeCoversRepo(repoScope, repo) {
			continue
		}
		branchScope, err := normalizeBranchScope(payload.BranchScope, row.SessionID, row.RepoName)
		if err != nil {
			continue
		}
		branchNames := branchScope.Branches
		if requestedLane != "" && len(branchNames) > 0 && !stringInSlice(requestedLane, branchNames) {
			continue
		}
		repos := repoScopeRepos(repoScope)
		if branchScope.Kind == "unlimited" {
			return prLaneAutoApprovalGrant{
				Active:      true,
				EventID:     row.EventID,
				Unlimited:   true,
				BranchNames: branchNames,
				Repos:       repos,
				AllRepos:    repoScope.Kind == "all_repos",
				RepoScope:   repoScope,
				BranchScope: branchScope,
			}
		}
		if branchScope.Kind == "count" && branchScope.Count > 0 {
			limit = branchScope.Count
		} else if branchScope.Kind == "named" && len(branchNames) > 0 {
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
			Repos:       repos,
			AllRepos:    repoScope.Kind == "all_repos",
			RepoScope:   repoScope,
			BranchScope: branchScope,
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
			RepoScope   repoScope   `json:"repo_scope"`
			BranchScope branchScope `json:"branch_scope"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		repoScope, err := normalizeRepoScope(payload.RepoScope, rowDefaultRepo(row))
		if err != nil || !repoScopeCoversRepo(repoScope, repo) {
			return false
		}
		branchScope, err := normalizeBranchScope(payload.BranchScope, row.SessionID, row.RepoName)
		if err != nil {
			return false
		}
		branchNames := branchScope.Branches
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

func countBreakGlassGrantBranches(rows []pgstore.ControlActionEvent, grantEventID string) int {
	branches := map[string]bool{}
	for _, row := range rows {
		if row.Action != "github.break_glass.push" || row.Status != "succeeded" {
			continue
		}
		if controlActionPayloadString(row.Payload, "grant_event_id") != grantEventID {
			continue
		}
		branch := strings.TrimSpace(controlActionPayloadString(row.Payload, "branch"))
		if branch != "" {
			branches[branch] = true
		}
	}
	return len(branches)
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

// normalizeAzureBreakGlassOperations bounds the azure grant operation set.
// azure-personal break-glass is all-or-nothing (the whole MCP is gated), so the
// only operation is use_azure_personal_mcp; the slice shape is kept parallel to
// normalizeBreakGlassOperations so the grant model reads the same for both
// resources.
func normalizeAzureBreakGlassOperations(in []string) []string {
	allowed := map[string]bool{"use_azure_personal_mcp": true}
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
		out = []string{"use_azure_personal_mcp"}
	}
	return out
}

func normalizeRepoScope(scope repoScope, fallbackRepo string) (repoScope, error) {
	kind := strings.TrimSpace(scope.Kind)
	switch kind {
	case "current_repo":
		if len(scope.Repos) > 0 {
			return repoScope{}, errors.New("repo_scope current_repo rejects repos")
		}
		repo := strings.TrimSpace(firstNonEmptyControlAction(scope.Repo, fallbackRepo))
		if repo == "" {
			return repoScope{}, errors.New("repo_scope current_repo requires repo")
		}
		repos, err := normalizeGitHubRepoList([]string{repo})
		if err != nil {
			return repoScope{}, err
		}
		return repoScope{Kind: kind, Repo: repos[0]}, nil
	case "repos":
		if strings.TrimSpace(scope.Repo) != "" {
			return repoScope{}, errors.New("repo_scope repos rejects repo")
		}
		repos, err := normalizeGitHubRepoList(scope.Repos)
		if err != nil {
			return repoScope{}, err
		}
		if len(repos) == 0 {
			return repoScope{}, errors.New("repo_scope repos requires at least one repo")
		}
		return repoScope{Kind: kind, Repos: repos}, nil
	case "all_repos":
		if strings.TrimSpace(scope.Repo) != "" || len(scope.Repos) > 0 {
			return repoScope{}, errors.New("repo_scope all_repos rejects repo and repos")
		}
		return repoScope{Kind: kind}, nil
	default:
		return repoScope{}, errors.New("repo_scope.kind must be current_repo, repos, or all_repos")
	}
}

func normalizeGitHubRepoList(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		slug := strings.TrimSpace(raw)
		if slug == "" {
			continue
		}
		owner, name, ok := strings.Cut(slug, "/")
		owner = strings.TrimSpace(owner)
		name = strings.TrimSpace(name)
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, errors.New("repo values must be GitHub slugs like owner/name")
		}
		slug = owner + "/" + name
		if !seen[slug] {
			out = append(out, slug)
			seen[slug] = true
		}
	}
	return out, nil
}

func normalizeBranchScope(scope branchScope, sessionID, repo string) (branchScope, error) {
	kind := strings.TrimSpace(scope.Kind)
	switch kind {
	case "named":
		if scope.Count != 0 {
			return branchScope{}, errors.New("branch_scope named rejects count")
		}
		branches := normalizePRLaneBranchNames(scope.Branches, sessionID, repo)
		if len(branches) == 0 {
			return branchScope{}, errors.New("branch_scope named requires branches")
		}
		return branchScope{Kind: kind, Branches: branches}, nil
	case "count":
		if len(scope.Branches) > 0 {
			return branchScope{}, errors.New("branch_scope count rejects branches")
		}
		if scope.Count <= 0 {
			return branchScope{}, errors.New("branch_scope count requires a positive count")
		}
		return branchScope{Kind: kind, Count: normalizedPositiveLimit(scope.Count, 0, 50)}, nil
	case "unlimited":
		if len(scope.Branches) > 0 || scope.Count != 0 {
			return branchScope{}, errors.New("branch_scope unlimited rejects branches and count")
		}
		return branchScope{Kind: kind}, nil
	default:
		return branchScope{}, errors.New("branch_scope.kind must be named, count, or unlimited")
	}
}

func repoScopeRepos(scope repoScope) []string {
	switch scope.Kind {
	case "current_repo":
		if scope.Repo == "" {
			return []string{}
		}
		return []string{scope.Repo}
	case "repos":
		return append([]string{}, scope.Repos...)
	default:
		return []string{}
	}
}

func repoScopeCoversRepo(scope repoScope, repo string) bool {
	if scope.Kind == "all_repos" {
		return true
	}
	if strings.TrimSpace(repo) == "" {
		return false
	}
	return stringInSlice(repo, repoScopeRepos(scope))
}

func repoScopeTargetRef(sessionID string, scope repoScope, path string) string {
	if scope.Kind == "all_repos" {
		return "tank://session/" + sessionID + "/" + path + "/all-repos"
	}
	repos := repoScopeRepos(scope)
	if len(repos) == 1 {
		return "https://github.com/" + repos[0]
	}
	return "tank://session/" + sessionID + "/" + path + "/repos"
}

func rowDefaultRepo(row pgstore.ControlActionEvent) string {
	if row.RepoOwner == "" || row.RepoName == "" {
		return ""
	}
	return row.RepoOwner + "/" + row.RepoName
}

func singleRepoOwner(repos []string, allRepos bool) string {
	if allRepos || len(repos) != 1 {
		return ""
	}
	owner, _, _ := strings.Cut(repos[0], "/")
	return owner
}

func singleRepoName(repos []string, allRepos bool) string {
	if allRepos || len(repos) != 1 {
		return ""
	}
	_, name, _ := strings.Cut(repos[0], "/")
	return name
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func normalizedPositiveLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if maximum > 0 && value > maximum {
		return maximum
	}
	return value
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
		"github.break_glass.deny",
		"github.break_glass.token",
		"github.break_glass.push",
		"azure.break_glass.request",
		"azure.break_glass.grant",
		"azure.break_glass.deny",
		"azure.break_glass.use",
		testSlotModelRequestAction,
		testSlotModelGrantAction:
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
