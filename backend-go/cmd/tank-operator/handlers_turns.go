package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxSDKTurnPromptBytes = 256 * 1024

// handleEnqueueSessionTurn is the durable submit boundary for SDK runtime
// sessions. The browser writes work here and reads transcript events from the
// durable SSE stream; runner-local transports are not part of the UI contract.
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

	resp, status, detail := s.enqueueSDKTurn(r.Context(), user.Email, sessionID, sdkTurnRequest{
		ClientNonce:    body.ClientNonce,
		RequireNonce:   true,
		Prompt:         body.Prompt,
		Model:          body.Model,
		PermissionMode: body.PermissionMode,
		SkillName:      body.SkillName,
		FollowUp:       body.FollowUp,
	})
	if detail != "" {
		writeError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *appServer) handleInterruptSessionTurn(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	targetTurnID := strings.TrimSpace(r.PathValue("turn_id"))
	if sessionID == "" || targetTurnID == "" || !turnIDPattern.MatchString(targetTurnID) {
		writeError(w, http.StatusBadRequest, "turn_id is required and must match turn id syntax")
		return
	}

	info, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		writeError(w, http.StatusBadRequest, "session mode does not support app chat turns")
		return
	}
	if s.turnQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "turn queue unavailable")
		return
	}

	if err := s.turnQueue.Enqueue(r.Context(), store.TurnRecord{
		TurnID:       "interrupt_" + auth.RandomHex(12),
		SessionID:    sessionID,
		Email:        user.Email,
		Provider:     provider,
		Source:       "interrupt",
		ClientNonce:  targetTurnID,
		TargetTurnID: targetTurnID,
		Status:       store.TurnPending,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "enqueue interrupt: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":         "queued",
		"target_turn_id": targetTurnID,
	})
}

type sdkTurnRequest struct {
	ClientNonce    string
	RequireNonce   bool
	Prompt         string
	Model          string
	PermissionMode string
	SkillName      string
	FollowUp       bool
}

func (s *appServer) enqueueSDKTurn(ctx context.Context, email, sessionID string, req sdkTurnRequest) (map[string]string, int, string) {
	clientNonce := strings.TrimSpace(req.ClientNonce)
	if clientNonce == "" {
		if req.RequireNonce {
			return nil, http.StatusBadRequest, "client_nonce is required and must match turn id syntax"
		}
		clientNonce = auth.RandomHex(12)
	}
	if !turnIDPattern.MatchString(clientNonce) {
		return nil, http.StatusBadRequest, "client_nonce is required and must match turn id syntax"
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, http.StatusBadRequest, "missing prompt"
	}
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		return nil, http.StatusBadRequest, "prompt too large"
	}

	info, err := s.mgr.GetByOwner(ctx, email, sessionID)
	if err != nil {
		return nil, http.StatusNotFound, "session not found"
	}

	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		return nil, http.StatusBadRequest, "session mode does not support app chat turns"
	}
	skillName := validateSkillName(req.SkillName)
	if strings.TrimSpace(req.SkillName) != "" && skillName == "" {
		return nil, http.StatusBadRequest, "skill_name is invalid"
	}
	if skillName != "" && !promptMatchesSkillTrigger(provider, skillName, prompt) {
		return nil, http.StatusBadRequest, "skill_name does not match prompt trigger"
	}
	if info.PodName == nil {
		return nil, http.StatusServiceUnavailable, "session pod not ready"
	}
	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(ctx, *info.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, http.StatusServiceUnavailable, "pod fetch failed: " + err.Error()
	}
	if !podHasSDKRunner(pod) {
		return nil, http.StatusBadRequest, "session pod has no SDK runner container"
	}

	if s.turnQueue == nil {
		return nil, http.StatusServiceUnavailable, "turn queue unavailable"
	}
	if err := s.turnQueue.Enqueue(ctx, store.TurnRecord{
		TurnID:         clientNonce,
		SessionID:      sessionID,
		Email:          email,
		Provider:       provider,
		Source:         "sdk",
		ClientNonce:    clientNonce,
		Prompt:         prompt,
		Model:          validateTurnArg(req.Model),
		PermissionMode: validateTurnArg(req.PermissionMode),
		SkillName:      skillName,
		FollowUp:       req.FollowUp,
	}); err != nil {
		return nil, http.StatusInternalServerError, "enqueue turn: " + err.Error()
	}

	return map[string]string{
		"status":       "queued",
		"turn_id":      clientNonce,
		"client_nonce": clientNonce,
		"provider":     provider,
	}, 0, ""
}

func promptMatchesSkillTrigger(provider, skillName, prompt string) bool {
	trigger := skillPromptTrigger(provider, skillName)
	return prompt == trigger || strings.HasPrefix(prompt, trigger+" ") || strings.HasPrefix(prompt, trigger+"\n")
}

func skillPromptTrigger(provider, skillName string) string {
	if provider == "codex" {
		return "$" + skillName
	}
	return "/" + skillName
}

func sdkProviderForMode(mode string) (string, bool) {
	switch compat.NormalizeSessionMode(mode) {
	case compat.ClaudeGUIMode:
		return "claude", true
	case compat.CodexGUIMode:
		return "codex", true
	default:
		return "", false
	}
}
