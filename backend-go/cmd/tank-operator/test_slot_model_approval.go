package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const (
	testSlotModelRequestAction = "tank.test_slot_model.request"
	testSlotModelGrantAction   = "tank.test_slot_model.grant"
)

type testSlotModelApproval struct {
	Mode        string
	Provider    string
	Model       string
	Effort      string
	LowModel    string
	LowEffort   string
	SessionID   string
	ApprovalURL string
	EventID     string
}

func lowCostModelForProvider(provider string) string {
	switch provider {
	case "claude":
		return "claude-haiku-4-5"
	case "codex":
		return "gpt-5.3-codex-spark"
	default:
		return ""
	}
}

func lowCostEffortForProvider(provider string) string {
	switch provider {
	case "claude", "codex":
		return "low"
	default:
		return ""
	}
}

func testSlotRunConfigNeedsApproval(mode, provider, model, effort string) bool {
	lowModel := lowCostModelForProvider(provider)
	lowEffort := lowCostEffortForProvider(provider)
	if lowModel == "" || lowEffort == "" {
		return false
	}
	return strings.TrimSpace(model) != lowModel || strings.TrimSpace(effort) != lowEffort
}

func (s *appServer) requireTestSlotLowCostRunConfig(ctx context.Context, r *http.Request, user auth.User, mode string, runConfig sessionRunConfig) (*testSlotModelApproval, int, string) {
	if s.localSessionScope() == prodSessionScope {
		return nil, 0, ""
	}
	provider, ok := sdkProviderForMode(mode)
	if !ok {
		return nil, 0, ""
	}
	if !testSlotRunConfigNeedsApproval(mode, provider, runConfig.Model, runConfig.Effort) {
		return nil, 0, ""
	}
	originSessionID := testSlotModelApprovalOriginSessionID(r, user)
	if originSessionID == "" {
		return nil, http.StatusBadRequest, "expensive test-slot model selection requires a requesting session id"
	}
	approval := testSlotModelApproval{
		Mode:      mode,
		Provider:  provider,
		Model:     runConfig.Model,
		Effort:    runConfig.Effort,
		LowModel:  lowCostModelForProvider(provider),
		LowEffort: lowCostEffortForProvider(provider),
		SessionID: originSessionID,
	}
	if s.controlActions == nil {
		return &approval, http.StatusServiceUnavailable, "control action store unavailable for test-slot model approval"
	}
	rows, err := s.controlActions.ListBySession(ctx, user.ActorEmail, s.localSessionScope(), originSessionID, 200)
	if err != nil {
		return &approval, http.StatusInternalServerError, err.Error()
	}
	if activeTestSlotModelGrant(rows, approval) != "" {
		return nil, 0, ""
	}
	row, err := s.appendTestSlotModelApprovalRequest(ctx, user.ActorEmail, approval)
	if err != nil {
		return &approval, http.StatusInternalServerError, err.Error()
	}
	approval.EventID = row.EventID
	approval.ApprovalURL = testSlotModelApprovalURL(approval, s.localSessionScope())
	return &approval, http.StatusConflict, "test-slot session model approval required"
}

func testSlotModelApprovalOriginSessionID(r *http.Request, user auth.User) string {
	if r != nil {
		if value := strings.TrimSpace(r.Header.Get(originSessionHeader)); value != "" {
			return value
		}
	}
	sub := strings.TrimSpace(user.Sub)
	for _, prefix := range []string{"svc:tank:", "tank:"} {
		if strings.HasPrefix(sub, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(sub, prefix))
		}
	}
	return ""
}

func (s *appServer) appendTestSlotModelApprovalRequest(ctx context.Context, ownerEmail string, approval testSlotModelApproval) (pgstore.ControlActionEvent, error) {
	approval.EventID = "tank-test-slot-model-request-" + approval.SessionID + "-" + randomHex(12)
	approval.ApprovalURL = testSlotModelApprovalURL(approval, s.localSessionScope())
	payload, _ := json.Marshal(map[string]any{
		"approval_url": approval.ApprovalURL,
		"mode":         approval.Mode,
		"provider":     approval.Provider,
		"model":        approval.Model,
		"effort":       approval.Effort,
		"low_model":    approval.LowModel,
		"low_effort":   approval.LowEffort,
		"reason":       "test-slot session requested a non-low-cost model or effort",
	})
	event := pgstore.ControlActionEvent{
		EventID:       approval.EventID,
		InvocationID:  "tank-test-slot-model-" + randomHex(12),
		OwnerEmail:    ownerEmail,
		SessionScope:  s.localSessionScope(),
		SessionID:     approval.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "create_session",
		Action:        testSlotModelRequestAction,
		Status:        "started",
		TargetKind:    "tank_session_model",
		TargetRef:     testSlotModelTargetRef(s.localSessionScope(), approval),
		Payload:       payload,
	}
	return s.controlActions.Append(ctx, event)
}

func testSlotModelApprovalURL(approval testSlotModelApproval, sessionScope string) string {
	base := strings.TrimRight(envDefault("TANK_UI_HOST", "https://tank.romaine.life"), "/")
	return base + "/sessions/" + url.PathEscape(approval.SessionID) + "/test-slot-model/" + url.PathEscape(approval.EventID)
}

func testSlotModelTargetRef(sessionScope string, approval testSlotModelApproval) string {
	return "tank://session-scope/" + normalizeSessionScope(sessionScope) + "/sessions/" + approval.SessionID + "/test-slot-model/" + approval.Mode
}

func activeTestSlotModelGrant(rows []pgstore.ControlActionEvent, approval testSlotModelApproval) string {
	now := time.Now().UTC()
	for _, row := range rows {
		if row.Action != testSlotModelGrantAction || row.Status != "succeeded" {
			continue
		}
		var payload struct {
			Mode      string `json:"mode"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			Effort    string `json:"effort"`
			ExpiresAt string `json:"expires_at"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
		if err != nil || !expiresAt.After(now) {
			continue
		}
		if strings.TrimSpace(payload.Mode) == approval.Mode &&
			strings.TrimSpace(payload.Provider) == approval.Provider &&
			strings.TrimSpace(payload.Model) == approval.Model &&
			strings.TrimSpace(payload.Effort) == approval.Effort {
			return row.EventID
		}
	}
	return ""
}

func (s *appServer) handleInternalGrantTestSlotModelApproval(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/test-slot-model-approvals/grants")
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
	if s.localSessionScope() == prodSessionScope {
		writeError(w, http.StatusBadRequest, "test-slot model approval is not used for production sessions")
		return
	}
	var body struct {
		Mode           string `json:"mode"`
		Model          string `json:"model"`
		Effort         string `json:"effort"`
		RequestEventID string `json:"request_event_id"`
		Reason         string `json:"reason"`
		TTLSeconds     int    `json:"ttl_seconds"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	mode, status, detail := validateCreateSessionMode(body.Mode)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	provider, ok := sdkProviderForMode(mode)
	if !ok {
		writeError(w, http.StatusBadRequest, "model approval is only supported for SDK chat sessions")
		return
	}
	runConfig, status, detail := validateCreateRunConfig(mode, body.Model, body.Effort)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	if !testSlotRunConfigNeedsApproval(mode, provider, runConfig.Model, runConfig.Effort) {
		writeError(w, http.StatusBadRequest, "requested model and effort already match the low-cost test-slot baseline")
		return
	}
	row, expiresAt, err := s.appendTestSlotModelGrant(r.Context(), testSlotModelGrantInput{
		SessionID:      sessionID,
		OwnerEmail:     user.ActorEmail,
		Mode:           mode,
		Provider:       provider,
		Model:          runConfig.Model,
		Effort:         runConfig.Effort,
		RequestEventID: body.RequestEventID,
		Reason:         body.Reason,
		TTLSeconds:     body.TTLSeconds,
		ApprovedBy:     user.ActorEmail,
	})
	if err != nil {
		recordControlActionEvent("tank-operator", "test_slot_model_approval", testSlotModelGrantAction, "succeeded", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":           true,
		"event_id":         row.EventID,
		"session_id":       sessionID,
		"session_scope":    s.localSessionScope(),
		"mode":             mode,
		"provider":         provider,
		"model":            runConfig.Model,
		"effort":           runConfig.Effort,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"request_event_id": strings.TrimSpace(body.RequestEventID),
	})
}

func (s *appServer) handleGetTestSlotModelApprovalRequest(w http.ResponseWriter, r *http.Request) {
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
	request, status, err := s.loadTestSlotModelApprovalRequest(r.Context(), sessionID, requestEventID)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	decision, err := s.controlActions.TestSlotModelDecisionForRequest(r.Context(), s.sessionScope, sessionID, request.EventID)
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

type testSlotModelDecisionBody struct {
	Note string `json:"note"`
}

func (s *appServer) handleApproveTestSlotModelApprovalRequest(w http.ResponseWriter, r *http.Request) {
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
	request, status, err := s.loadTestSlotModelApprovalRequest(r.Context(), sessionID, requestEventID)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var body testSlotModelDecisionBody
	if r.Body != nil {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, http.ErrBodyReadAfterClose) {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	existing, err := s.controlActions.TestSlotModelDecisionForRequest(r.Context(), s.sessionScope, sessionID, request.EventID)
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
		writeError(w, http.StatusConflict, "test-slot model request is not pending")
		return
	}
	row, expiresAt, err := s.appendTestSlotModelGrantForRequest(r.Context(), request, user.Email, body.Note)
	agentNotification := map[string]any{"delivered": false}
	if err != nil {
		recordControlActionEvent("tank-operator", "test_slot_model_approval", testSlotModelGrantAction, "succeeded", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	if notifyResp, status, detail := s.enqueueTestSlotModelApprovalTurn(r.Context(), row, expiresAt); status != 0 {
		agentNotification["error"] = strings.TrimSpace(detail)
		recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "notify_error")
		slog.Warn("test-slot model approval grant persisted but agent notification turn failed",
			"session_id", request.SessionID, "grant_event_id", row.EventID, "status", status, "detail", detail)
	} else {
		agentNotification["delivered"] = true
		if turnID := turnIDFromEnqueueResponse(notifyResp); turnID != "" {
			agentNotification["turn_id"] = turnID
		}
	}
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

func (s *appServer) loadTestSlotModelApprovalRequest(ctx context.Context, sessionID, requestEventID string) (pgstore.ControlActionEvent, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	requestEventID = strings.TrimSpace(requestEventID)
	if sessionID == "" || requestEventID == "" {
		return pgstore.ControlActionEvent{}, http.StatusBadRequest, errors.New("session_id and request_event_id are required")
	}
	request, err := s.controlActions.GetBySessionEvent(ctx, s.sessionScope, sessionID, requestEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgstore.ControlActionEvent{}, http.StatusNotFound, errors.New("test-slot model request not found")
	}
	if err != nil {
		return pgstore.ControlActionEvent{}, http.StatusInternalServerError, err
	}
	if request.Action != testSlotModelRequestAction {
		return pgstore.ControlActionEvent{}, http.StatusNotFound, errors.New("test-slot model request not found")
	}
	return request, 0, nil
}

type testSlotModelGrantInput struct {
	SessionID      string
	OwnerEmail     string
	Mode           string
	Provider       string
	Model          string
	Effort         string
	RequestEventID string
	Reason         string
	TTLSeconds     int
	ApprovedBy     string
}

func (s *appServer) appendTestSlotModelGrantForRequest(ctx context.Context, request pgstore.ControlActionEvent, approvedBy, note string) (pgstore.ControlActionEvent, time.Time, error) {
	var payload struct {
		Mode      string `json:"mode"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Effort    string `json:"effort"`
		Reason    string `json:"reason"`
		LowModel  string `json:"low_model"`
		LowEffort string `json:"low_effort"`
	}
	_ = json.Unmarshal(request.Payload, &payload)
	return s.appendTestSlotModelGrant(ctx, testSlotModelGrantInput{
		SessionID:      request.SessionID,
		OwnerEmail:     request.OwnerEmail,
		Mode:           payload.Mode,
		Provider:       payload.Provider,
		Model:          payload.Model,
		Effort:         payload.Effort,
		RequestEventID: request.EventID,
		Reason:         firstNonEmptyControlAction(strings.TrimSpace(note), strings.TrimSpace(payload.Reason)),
		ApprovedBy:     approvedBy,
	})
}

func (s *appServer) appendTestSlotModelGrant(ctx context.Context, in testSlotModelGrantInput) (pgstore.ControlActionEvent, time.Time, error) {
	ttl := in.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 24*3600 {
		ttl = 24 * 3600
	}
	mode, status, detail := validateCreateSessionMode(in.Mode)
	if status != 0 {
		return pgstore.ControlActionEvent{}, time.Time{}, errors.New(detail)
	}
	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		var ok bool
		provider, ok = sdkProviderForMode(mode)
		if !ok {
			return pgstore.ControlActionEvent{}, time.Time{}, errors.New("model approval is only supported for SDK chat sessions")
		}
	}
	runConfig, status, detail := validateCreateRunConfig(mode, in.Model, in.Effort)
	if status != 0 {
		return pgstore.ControlActionEvent{}, time.Time{}, errors.New(detail)
	}
	if !testSlotRunConfigNeedsApproval(mode, provider, runConfig.Model, runConfig.Effort) {
		return pgstore.ControlActionEvent{}, time.Time{}, errors.New("requested model and effort already match the low-cost test-slot baseline")
	}
	expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
	payload, _ := json.Marshal(map[string]any{
		"approved_by":      strings.TrimSpace(in.ApprovedBy),
		"expires_at":       expiresAt.Format(time.RFC3339),
		"ttl_seconds":      ttl,
		"request_event_id": strings.TrimSpace(in.RequestEventID),
		"reason":           strings.TrimSpace(in.Reason),
		"mode":             mode,
		"provider":         provider,
		"model":            runConfig.Model,
		"effort":           runConfig.Effort,
		"low_model":        lowCostModelForProvider(provider),
		"low_effort":       lowCostEffortForProvider(provider),
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-test-slot-model-grant-" + in.SessionID + "-" + randomHex(12),
		InvocationID:  "tank-test-slot-model-grant-" + randomHex(12),
		OwnerEmail:    in.OwnerEmail,
		SessionScope:  s.localSessionScope(),
		SessionID:     in.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "test_slot_model_approval",
		Action:        testSlotModelGrantAction,
		Status:        "succeeded",
		TargetKind:    "tank_session_model",
		TargetRef: testSlotModelTargetRef(s.localSessionScope(), testSlotModelApproval{
			Mode: mode, Provider: provider, Model: runConfig.Model, Effort: runConfig.Effort, SessionID: in.SessionID,
		}),
		Payload: payload,
	}
	row, err := s.controlActions.Append(ctx, event)
	return row, expiresAt, err
}

func (s *appServer) enqueueTestSlotModelApprovalTurn(ctx context.Context, grant pgstore.ControlActionEvent, expiresAt time.Time) (map[string]any, int, string) {
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
	seed = sessionID + ":test-slot-model:" + seed
	return s.enqueueSDKTurn(ctx, ownerEmail, sessionID, sdkTurnRequest{
		ClientNonce:  gitBreakGlassApprovalTurnNonce(seed),
		RequireNonce: true,
		Prompt:       testSlotModelApprovalPrompt(grant, expiresAt),
		DisplayText:  testSlotModelApprovalDisplayText(grant, expiresAt),
		Source:       string(conversation.TurnSubmittedSourceTestSlotModelApproval),
		CreatedAt:    time.Now().UTC(),
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
}

func testSlotModelApprovalDisplayText(grant pgstore.ControlActionEvent, expiresAt time.Time) string {
	model := controlActionPayloadString(grant.Payload, "model")
	effort := controlActionPayloadString(grant.Payload, "effort")
	target := strings.TrimSpace(strings.TrimSpace(model) + " / " + strings.TrimSpace(effort))
	if target == "/" {
		target = "the requested model"
	}
	expiry := ""
	if !expiresAt.IsZero() {
		expiry = " The grant expires at " + expiresAt.UTC().Format(time.RFC3339) + "."
	}
	return "Test-slot model approval granted for " + target + "." + expiry
}

func testSlotModelApprovalPrompt(grant pgstore.ControlActionEvent, expiresAt time.Time) string {
	lines := []string{
		"System message: Your test-slot model request was approved by the user.",
		testSlotModelApprovalDisplayText(grant, expiresAt),
		"Retry the test-slot session creation now using the approved model and effort.",
	}
	if reason := controlActionPayloadString(grant.Payload, "reason"); reason != "" {
		lines = append(lines, "Approval reason: "+reason)
	}
	return strings.Join(lines, "\n")
}
