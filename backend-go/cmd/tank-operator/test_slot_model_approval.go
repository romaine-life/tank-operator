package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
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
	base := envDefault("AUTH_ROMAINE_BREAK_GLASS_URL", "https://auth.romaine.life/admin")
	params := url.Values{}
	params.Set("intent", "test-slot-model")
	params.Set("session_id", approval.SessionID)
	params.Set("session_scope", normalizeSessionScope(sessionScope))
	params.Set("request_event_id", approval.EventID)
	params.Set("mode", approval.Mode)
	params.Set("provider", approval.Provider)
	params.Set("model", approval.Model)
	params.Set("effort", approval.Effort)
	params.Set("low_model", approval.LowModel)
	params.Set("low_effort", approval.LowEffort)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + params.Encode()
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

type testSlotModelGrantInput struct {
	SessionID      string
	SessionScope   string
	OwnerEmail     string
	Mode           string
	Model          string
	Effort         string
	RequestEventID string
	Reason         string
	TTLSeconds     int
	ApprovedBy     string
}

func (s *appServer) appendTestSlotModelGrant(ctx context.Context, in testSlotModelGrantInput) (pgstore.ControlActionEvent, testSlotModelApproval, time.Time, error) {
	mode, status, detail := validateCreateSessionMode(in.Mode)
	if status != 0 {
		return pgstore.ControlActionEvent{}, testSlotModelApproval{}, time.Time{}, statusError{status: status, detail: detail}
	}
	provider, ok := sdkProviderForMode(mode)
	if !ok {
		return pgstore.ControlActionEvent{}, testSlotModelApproval{}, time.Time{}, statusError{status: http.StatusBadRequest, detail: "model approval is only supported for SDK chat sessions"}
	}
	runConfig, status, detail := validateCreateRunConfig(mode, in.Model, in.Effort)
	if status != 0 {
		return pgstore.ControlActionEvent{}, testSlotModelApproval{}, time.Time{}, statusError{status: status, detail: detail}
	}
	if !testSlotRunConfigNeedsApproval(mode, provider, runConfig.Model, runConfig.Effort) {
		return pgstore.ControlActionEvent{}, testSlotModelApproval{}, time.Time{}, statusError{status: http.StatusBadRequest, detail: "requested model and effort already match the low-cost test-slot baseline"}
	}
	ttl := in.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 24*3600 {
		ttl = 24 * 3600
	}
	expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
	approval := testSlotModelApproval{
		Mode:      mode,
		Provider:  provider,
		Model:     runConfig.Model,
		Effort:    runConfig.Effort,
		LowModel:  lowCostModelForProvider(provider),
		LowEffort: lowCostEffortForProvider(provider),
		SessionID: in.SessionID,
	}
	sessionScope := normalizeSessionScope(firstNonEmptyControlAction(in.SessionScope, s.localSessionScope()))
	payload, _ := json.Marshal(map[string]any{
		"approved_by":      strings.TrimSpace(in.ApprovedBy),
		"expires_at":       expiresAt.Format(time.RFC3339),
		"ttl_seconds":      ttl,
		"request_event_id": strings.TrimSpace(in.RequestEventID),
		"reason":           strings.TrimSpace(in.Reason),
		"mode":             approval.Mode,
		"provider":         approval.Provider,
		"model":            approval.Model,
		"effort":           approval.Effort,
		"low_model":        approval.LowModel,
		"low_effort":       approval.LowEffort,
	})
	event := pgstore.ControlActionEvent{
		EventID:       "tank-test-slot-model-grant-" + in.SessionID + "-" + randomHex(12),
		InvocationID:  "tank-test-slot-model-grant-" + randomHex(12),
		OwnerEmail:    in.OwnerEmail,
		SessionScope:  sessionScope,
		SessionID:     in.SessionID,
		SourceService: "tank-operator",
		SourceTool:    "test_slot_model_approval",
		Action:        testSlotModelGrantAction,
		Status:        "succeeded",
		TargetKind:    "tank_session_model",
		TargetRef:     testSlotModelTargetRef(sessionScope, approval),
		Payload:       payload,
	}
	row, err := s.controlActions.Append(ctx, event)
	return row, approval, expiresAt, err
}

type statusError struct {
	status int
	detail string
}

func (e statusError) Error() string {
	return e.detail
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
	row, approval, expiresAt, err := s.appendTestSlotModelGrant(r.Context(), testSlotModelGrantInput{
		SessionID:      sessionID,
		SessionScope:   s.localSessionScope(),
		OwnerEmail:     user.ActorEmail,
		Mode:           body.Mode,
		Model:          body.Model,
		Effort:         body.Effort,
		RequestEventID: body.RequestEventID,
		Reason:         body.Reason,
		TTLSeconds:     body.TTLSeconds,
		ApprovedBy:     user.ActorEmail,
	})
	if err != nil {
		if se, ok := err.(statusError); ok {
			writeError(w, se.status, se.detail)
			return
		}
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
		"mode":             approval.Mode,
		"provider":         approval.Provider,
		"model":            approval.Model,
		"effort":           approval.Effort,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"request_event_id": strings.TrimSpace(body.RequestEventID),
	})
}

func (s *appServer) handleAdminGrantTestSlotModelApproval(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.controlActions == nil {
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
	if sessionScope == prodSessionScope {
		writeError(w, http.StatusBadRequest, "agent selection break glass is only used for test-slot sessions")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	info, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
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
	row, approval, expiresAt, err := s.appendTestSlotModelGrant(r.Context(), testSlotModelGrantInput{
		SessionID:      sessionID,
		SessionScope:   sessionScope,
		OwnerEmail:     info.Owner,
		Mode:           body.Mode,
		Model:          body.Model,
		Effort:         body.Effort,
		RequestEventID: body.RequestEventID,
		Reason:         body.Reason,
		TTLSeconds:     body.TTLSeconds,
		ApprovedBy:     user.Email,
	})
	if err != nil {
		if se, ok := err.(statusError); ok {
			writeError(w, se.status, se.detail)
			return
		}
		recordControlActionEvent("tank-operator", "test_slot_model_approval", testSlotModelGrantAction, "succeeded", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordControlActionEvent(row.SourceService, row.SourceTool, row.Action, row.Status, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"active":           true,
		"event_id":         row.EventID,
		"session_id":       sessionID,
		"session_scope":    sessionScope,
		"owner_email":      info.Owner,
		"mode":             approval.Mode,
		"provider":         approval.Provider,
		"model":            approval.Model,
		"effort":           approval.Effort,
		"low_model":        approval.LowModel,
		"low_effort":       approval.LowEffort,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"request_event_id": strings.TrimSpace(body.RequestEventID),
	})
}
