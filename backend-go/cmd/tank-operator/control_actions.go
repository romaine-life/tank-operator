package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	case "github.pull_request.merge", "github.pull_request.ready_for_review":
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
