package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

type internalSessionPodCaller struct {
	Email     string
	SessionID string
	PodName   string
	PodUID    string
}

func (s *appServer) handleInternalSessionTurnTerminal(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	turnID := strings.TrimSpace(r.PathValue("turn_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if turnID == "" || !turnIDPattern.MatchString(turnID) {
		writeError(w, http.StatusBadRequest, "turn_id is required and must match turn id syntax")
		return
	}
	if sessionID != caller.SessionID {
		writeError(w, http.StatusForbidden, "session turn target does not match caller pod")
		return
	}

	if s.sessionEvents == nil {
		writeJSON(w, http.StatusOK, map[string]any{"terminal": false})
		return
	}
	event, err := s.sessionEvents.FindTurnTerminal(r.Context(), sessionID, turnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if event == nil {
		writeJSON(w, http.StatusOK, map[string]any{"terminal": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"terminal": true,
		"event":    event,
	})
}

func (s *appServer) handleInternalSessionRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		recordSessionRuntimeConfigUpdate("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		recordSessionRuntimeConfigUpdate("unknown", "forbidden")
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}

	var body struct {
		Model                 string         `json:"model"`
		Effort                string         `json:"effort"`
		ContextWindowTokens   int64          `json:"context_window_tokens"`
		ContextWindowSource   string         `json:"context_window_source"`
		ProviderRateLimitInfo map[string]any `json:"provider_rate_limit_info"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		recordSessionRuntimeConfigUpdate("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if s.mgr == nil {
		recordSessionRuntimeConfigUpdate("unknown", "manager_unavailable")
		writeError(w, http.StatusServiceUnavailable, "session manager unavailable")
		return
	}
	model := strings.TrimSpace(body.Model)

	info, err := s.mgr.GetRegisteredByOwner(r.Context(), caller.Email, sessionID)
	if err != nil {
		recordSessionRuntimeConfigUpdate("unknown", "not_found")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		recordSessionRuntimeConfigUpdate("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "session mode does not support SDK runtime config")
		return
	}
	if model != "" && validateModelArg(provider, model) == "" {
		recordSessionRuntimeConfigUpdate(provider, "bad_request")
		writeError(w, http.StatusBadRequest, "model is invalid")
		return
	}
	effortInput := strings.TrimSpace(body.Effort)
	effort := validateEffort(provider, effortInput)
	if effortInput != "" && effort == "" {
		recordSessionRuntimeConfigUpdate(provider, "bad_request")
		if provider == "antigravity" {
			writeError(w, http.StatusBadRequest, effortUnsupportedMessage(provider, "sessions"))
			return
		}
		if provider == "codex" {
			writeError(w, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh")
			return
		}
		writeError(w, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh|max")
		return
	}

	var updated sessions.Info
	if model != "" || effort != "" {
		updated, err = s.mgr.SetRuntimeConfig(r.Context(), caller.Email, sessionID, model, effort)
		if err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				recordSessionRuntimeConfigUpdate(provider, "not_found")
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			recordSessionRuntimeConfigUpdate(provider, "update_failed")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		updated = info
	}
	if body.ContextWindowTokens > 0 {
		updated, err = s.mgr.SetRuntimeContextWindow(
			r.Context(),
			caller.Email,
			sessionID,
			body.ContextWindowTokens,
			body.ContextWindowSource,
		)
		if err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				recordSessionContextWindowReport(provider, body.ContextWindowSource, "not_found")
				recordSessionRuntimeConfigUpdate(provider, "not_found")
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			recordSessionContextWindowReport(provider, body.ContextWindowSource, "update_failed")
			recordSessionRuntimeConfigUpdate(provider, "update_failed")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordSessionContextWindowReport(provider, body.ContextWindowSource, "ok")
	} else {
		recordSessionContextWindowReport(provider, body.ContextWindowSource, "ignored")
	}
	if rateLimitInfo := sanitizeProviderRateLimitInfo(body.ProviderRateLimitInfo); len(rateLimitInfo) > 0 {
		updated, err = s.mgr.SetProviderRateLimitInfo(r.Context(), caller.Email, sessionID, rateLimitInfo)
		if err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				recordSessionRuntimeConfigUpdate(provider, "not_found")
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			recordSessionRuntimeConfigUpdate(provider, "update_failed")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	recordSessionRuntimeConfigUpdate(provider, "ok")
	writeJSON(w, http.StatusOK, updated)
}

func sanitizeProviderRateLimitInfo(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	allowed := map[string]struct{}{
		"provider":              {},
		"status":                {},
		"rateLimitType":         {},
		"resetsAt":              {},
		"utilization":           {},
		"overageStatus":         {},
		"overageResetsAt":       {},
		"overageDisabledReason": {},
		"isUsingOverage":        {},
		"surpassedThreshold":    {},
		"uuid":                  {},
		"session_id":            {},
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		if _, ok := allowed[key]; !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			v = strings.TrimSpace(v)
			if v != "" {
				if len(v) > 512 {
					v = v[:512]
				}
				out[key] = v
			}
		case float64:
			out[key] = v
		case bool:
			out[key] = v
		}
	}
	return out
}

func (s *appServer) requireInternalSessionPodCaller(w http.ResponseWriter, r *http.Request) (internalSessionPodCaller, bool) {
	token, err := auth.ParseSAToken(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return internalSessionPodCaller{}, false
	}
	subject, err := auth.ValidateSAToken(r.Context(), s.k8s, token, []string{"tank-operator"})
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return internalSessionPodCaller{}, false
	}
	if subject.Namespace != s.namespace || subject.Name != s.sessionServiceAccount {
		writeError(w, http.StatusForbidden, "caller is not the Tank session service account")
		return internalSessionPodCaller{}, false
	}
	podName := subject.ExtraValue("authentication.kubernetes.io/pod-name")
	podUID := subject.ExtraValue("authentication.kubernetes.io/pod-uid")
	if podName == "" || podUID == "" {
		writeError(w, http.StatusForbidden, "service account token is not bound to a session pod")
		return internalSessionPodCaller{}, false
	}
	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(r.Context(), podName, metav1.GetOptions{})
	if err != nil {
		writeError(w, http.StatusForbidden, "session pod not found")
		return internalSessionPodCaller{}, false
	}
	if pod.Spec.ServiceAccountName != s.sessionServiceAccount || string(pod.UID) != podUID {
		writeError(w, http.StatusForbidden, "service account token does not match session pod")
		return internalSessionPodCaller{}, false
	}
	if pod.Labels["app.kubernetes.io/managed-by"] != "tank-operator" {
		writeError(w, http.StatusForbidden, "pod is not managed by Tank")
		return internalSessionPodCaller{}, false
	}
	sessionID := strings.TrimSpace(pod.Labels["tank-operator/session-id"])
	sessionScope := strings.TrimSpace(pod.Labels["tank-operator/session-scope"])
	expectedScope := strings.TrimSpace(s.sessionScope)
	if expectedScope == "" {
		expectedScope = "default"
	}
	if sessionID == "" || sessionScope == "" || sessionScope != expectedScope {
		writeError(w, http.StatusForbidden, "pod is not in the active Tank session scope")
		return internalSessionPodCaller{}, false
	}
	email := strings.ToLower(strings.TrimSpace(pod.Annotations["tank-operator/owner-email"]))
	if email == "" {
		writeError(w, http.StatusForbidden, "session pod is missing owner identity")
		return internalSessionPodCaller{}, false
	}
	return internalSessionPodCaller{
		Email:     email,
		SessionID: sessionID,
		PodName:   podName,
		PodUID:    podUID,
	}, true
}
