package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

const maxSDKTurnPromptBytes = 256 * 1024

// handleEnqueueSessionTurn is the durable submit boundary for SDK runtime
// sessions. /agent-ws remains the live transport; this handler records the
// work item before any browser-to-runner socket is involved.
func (s *appServer) handleEnqueueSessionTurn(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}

	var body struct {
		ClientNonce    string `json:"client_nonce"`
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		SkillName      string `json:"skill_name"`
		FollowUp       bool   `json:"follow_up"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	clientNonce := strings.TrimSpace(body.ClientNonce)
	if clientNonce == "" || !runIDPattern.MatchString(clientNonce) {
		writeError(w, http.StatusBadRequest, "client_nonce is required and must match run id syntax")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "missing prompt")
		return
	}
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		writeError(w, http.StatusBadRequest, "prompt too large")
		return
	}

	info, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if info.Runtime != "sdk" {
		writeError(w, http.StatusBadRequest, "session does not have an SDK runner")
		return
	}

	mode := compat.NormalizeSessionMode(info.Mode)
	provider := "claude"
	if mode == compat.CodexGUIMode {
		provider = "codex"
	} else if mode != compat.ClaudeGUIMode {
		writeError(w, http.StatusBadRequest, "session mode does not support SDK turns")
		return
	}

	if s.turnQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "turn queue unavailable")
		return
	}
	if err := s.turnQueue.Enqueue(r.Context(), store.TurnRecord{
		RunID:          clientNonce,
		SessionID:      sessionID,
		Email:          user.Email,
		Provider:       provider,
		Source:         "sdk",
		ClientNonce:    clientNonce,
		Prompt:         prompt,
		Model:          validateHeadlessArg(body.Model),
		PermissionMode: validateHeadlessArg(body.PermissionMode),
		SkillName:      validateSkillName(body.SkillName),
		FollowUp:       body.FollowUp,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "enqueue turn: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":       "queued",
		"run_id":       clientNonce,
		"client_nonce": clientNonce,
		"provider":     provider,
	})
}
