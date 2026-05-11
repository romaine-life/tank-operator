package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/coder/websocket"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

const agentRunnerDialTimeout = 10 * time.Second

// handleAgentWebSocket reverse-proxies the SPA's WebSocket onto the
// pod's agent-runner (Phase B Node service listening on
// localhost:AgentRunnerWSPort). Auth happens here (JWT via cookie or
// query param); inside the cluster the orchestrator-to-pod hop trusts
// network policy and the pod's owner label check.
//
// Bytes are piped raw — no message inspection, no serialization touch.
// The runner produces the wire format; this handler is a pure pipe so
// SDK protocol changes don't require orchestrator changes.
func (s *appServer) handleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWSAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}

	// Ownership check + pod-name resolution via the existing helper.
	info, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}
	_ = info

	// Need the pod IP to dial directly. Pod IPs are routable within the
	// cluster from the orchestrator namespace per the AKS default CNI.
	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(r.Context(), podName, metav1.GetOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pod fetch failed: "+err.Error())
		return
	}
	if pod.Status.PodIP == "" {
		writeError(w, http.StatusServiceUnavailable, "pod has no IP yet")
		return
	}
	if !podHasAgentRunner(pod) {
		writeError(w, http.StatusBadRequest, "session pod has no agent-runner container (legacy claude_cli/codex/pi modes don't use the SDK runner)")
		return
	}

	// Upgrade the browser-side WebSocket.
	browserConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer browserConn.Close(websocket.StatusNormalClosure, "")

	// Dial the pod's agent-runner. The runner listens on 0.0.0.0:WSPort
	// inside the pod; from the orchestrator we reach it by pod IP.
	podURL := fmt.Sprintf("ws://%s:%d/", pod.Status.PodIP, compat.AgentRunnerWSPort)
	dialCtx, dialCancel := context.WithTimeout(r.Context(), agentRunnerDialTimeout)
	defer dialCancel()
	podConn, _, err := websocket.Dial(dialCtx, podURL, nil)
	if err != nil {
		_ = browserConn.Write(r.Context(), websocket.MessageText,
			mustJSON(map[string]any{"error": "agent-runner not reachable: " + err.Error()}))
		return
	}
	defer podConn.Close(websocket.StatusNormalClosure, "")

	// Bidirectional pipe — independent goroutines per direction so neither
	// blocks the other. First error in either direction (close, network
	// hiccup, browser disconnect, pod crash) tears down both sides.
	errCh := make(chan error, 2)
	go func() {
		for {
			mt, data, err := browserConn.Read(r.Context())
			if err != nil {
				errCh <- err
				return
			}
			if err := podConn.Write(r.Context(), mt, data); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		for {
			mt, data, err := podConn.Read(r.Context())
			if err != nil {
				errCh <- err
				return
			}
			if err := browserConn.Write(r.Context(), mt, data); err != nil {
				errCh <- err
				return
			}
		}
	}()
	if firstErr := <-errCh; firstErr != nil {
		slog.Debug("agent-ws pipe ended", "session", sessionID, "err", firstErr)
	}
}

// podHasAgentRunner returns true if the pod spec includes the
// agent-runner container (claude_gui mode only today; Phase B
// added it conditionally in compat.PodManifest).
func podHasAgentRunner(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == "agent-runner" {
			return true
		}
	}
	return false
}

// handleListSessionEvents reads canonical SDK events from the
// `session-events` Cosmos container for the SPA's history-replay path.
// Returns events strictly after the watermark `after` (a v7 UUID — sorts
// by emit order), in ascending order, up to `limit` (max 1000).
//
// SPA contract: on session open, fetch with after="" to get the full
// history; then open the WebSocket for live updates and dedupe by uuid.
func (s *appServer) handleListSessionEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	// Ownership check via the existing reader path.
	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	after := r.URL.Query().Get("after")
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	events, err := s.sessionEvents.ListBySession(r.Context(), sessionID, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"events":     events,
	})
}
