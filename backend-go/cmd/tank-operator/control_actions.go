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
		"github.pull_request.ready_for_review",
		"github.pull_request.open",
		"github.pull_request.mergeability",
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
