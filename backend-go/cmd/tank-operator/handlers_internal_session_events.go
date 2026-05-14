package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

type internalSessionPodCaller struct {
	Email     string
	SessionID string
	PodName   string
	PodUID    string
}

func (s *appServer) handleInternalSessionEventsNotify(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		writeError(w, http.StatusForbidden, "session event notify target does not match caller pod")
		return
	}

	var body struct {
		LastOrderKey string `json:"last_order_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.Body != nil {
		body.LastOrderKey = ""
	}

	if s.eventBroker != nil {
		s.eventBroker.Notify(sessionID)
	}
	slog.Debug("session event stream notified",
		"session_id", sessionID,
		"pod", caller.PodName,
		"email", caller.Email,
		"last_order_key", strings.TrimSpace(body.LastOrderKey),
	)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "notified"})
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
