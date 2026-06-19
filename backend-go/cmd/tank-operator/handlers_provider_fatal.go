package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
)

// providerFatalReportTotal counts runner-reported provider-fatal events by
// provider and result.
var providerFatalReportTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_session_provider_fatal_total",
		Help: "Provider-fatal reports from pod-side runners, by provider and result.",
	},
	[]string{"provider", "result"},
)

// handleInternalProviderFatal is the pod-side runner's report that its agent
// process died and the session cannot continue. Provider-process death is
// session-terminal: the session row moves to
// Failed through the same RowWriter transition the K8s watch uses for pod
// death, so the sidebar, activity, and UI gating behave identically. There
// is deliberately no revival path.
//
// Auth matches the other internal session-pod surfaces: the caller presents
// its projected SA token and may only report against its own session.
// Repeat reports are idempotent (the row is already Failed).
func (s *appServer) handleInternalProviderFatal(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		providerFatalReportTotal.WithLabelValues("unknown", "unauthorized").Inc()
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		providerFatalReportTotal.WithLabelValues("unknown", "bad_request").Inc()
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		providerFatalReportTotal.WithLabelValues("unknown", "forbidden").Inc()
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.rowWriter == nil || s.mgr == nil {
		providerFatalReportTotal.WithLabelValues("unknown", "unavailable").Inc()
		writeError(w, http.StatusServiceUnavailable, "session row writer unavailable")
		return
	}

	var body struct {
		Provider string `json:"provider"`
		Reason   string `json:"reason"`
		ExitCode *int   `json:"exit_code"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		providerFatalReportTotal.WithLabelValues("unknown", "bad_request").Inc()
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	provider := strings.TrimSpace(body.Provider)
	reason := strings.TrimSpace(body.Reason)
	if provider == "" || reason == "" {
		providerFatalReportTotal.WithLabelValues(provider, "bad_request").Inc()
		writeError(w, http.StatusBadRequest, "provider and reason are required")
		return
	}

	info, err := s.mgr.GetRegisteredByOwner(r.Context(), caller.Email, sessionID)
	if err != nil {
		providerFatalReportTotal.WithLabelValues(provider, "not_found").Inc()
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	modeProvider, ok := sdkProviderForMode(info.Mode)
	if !ok || modeProvider != provider {
		providerFatalReportTotal.WithLabelValues(provider, "provider_mismatch").Inc()
		writeError(w, http.StatusBadRequest, "provider does not match session mode")
		return
	}

	payload := map[string]any{
		"status":   "Failed",
		"provider": provider,
		"reason":   reason,
		"pod_name": caller.PodName,
	}
	if body.ExitCode != nil {
		payload["exit_code"] = *body.ExitCode
	}
	if msg := strings.TrimSpace(body.Message); msg != "" {
		payload["message"] = msg
	}

	event := sessioncontroller.Event{
		Email:        caller.Email,
		SessionScope: s.sessionScope,
		SessionID:    sessionID,
		Type:         sessioncontroller.EventTypeProviderFatal,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Payload:      payload,
	}
	if _, err := s.rowWriter.RecordTransition(r.Context(), event); err != nil {
		providerFatalReportTotal.WithLabelValues(provider, "write_failed").Inc()
		slog.Error("provider-fatal row transition failed",
			"session_id", sessionID, "provider", provider, "reason", reason, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record provider-fatal transition")
		return
	}
	providerFatalReportTotal.WithLabelValues(provider, "ok").Inc()
	slog.Warn("session marked Failed by runner provider-fatal report",
		"session_id", sessionID, "provider", provider, "reason", reason, "pod", caller.PodName)

	// Reap the pod so a crash-looping runner actually stops. The durable
	// terminal is already recorded above and the row stays Failed (ReapPod does
	// not mark it deleted). Best-effort: if the delete fails, the restart-budget
	// backstop in the K8s watch reaps a still-looping pod.
	if err := s.mgr.ReapPod(r.Context(), caller.Email, sessionID); err != nil {
		slog.Warn("provider-fatal pod reap failed",
			"session_id", sessionID, "provider", provider, "error", err)
	} else {
		sessionPodReapedTotal.WithLabelValues("provider_fatal").Inc()
	}
	w.WriteHeader(http.StatusNoContent)
}
