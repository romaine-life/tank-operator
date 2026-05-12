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
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
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
	if !podHasSDKRunner(pod) {
		writeError(w, http.StatusBadRequest, "session pod has no SDK runner container (legacy claude_cli/codex_cli/pi modes don't use a runner)")
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

// podHasSDKRunner returns true if the pod spec includes either of the
// SDK runner sidecars: agent-runner (claude_gui, @anthropic-ai/claude-
// agent-sdk) or codex-runner (codex_gui, @openai/codex-sdk). Both
// listen on the same agent-ws port and speak the same client-side
// protocol, so the WS reverse-proxy doesn't care which one is on the
// other end. Without this, codex_gui sessions were 400'd at WS upgrade
// despite having a working codex-runner — visible to the user as a
// codex session timing out the moment they tried to send a message.
func podHasSDKRunner(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == "agent-runner" || c.Name == "codex-runner" {
			return true
		}
	}
	return false
}

// handleListSessionEvents reads canonical SDK events from the
// `session-events` Cosmos container for the SPA's history-replay path.
// Returns events strictly after `after_order_key`, the same cursor the SPA
// renders by. The legacy `after` document-id cursor remains accepted for old
// tabs.
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

	cursor := sessionEventCursorFromRequest(r)
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	page, err := s.sessionEvents.ListBySession(r.Context(), sessionID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if page.Events == nil {
		page.Events = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":      sessionID,
		"events":          page.Events,
		"next_order_key":  page.NextOrderKey,
		"has_more":        page.HasMore,
		"cursor_semantic": "render_order",
	})
}

func (s *appServer) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	s.handleListSessionEvents(w, r)
}

func sessionEventCursorFromRequest(r *http.Request) store.SessionEventCursor {
	if afterOrderKey := r.URL.Query().Get("after_order_key"); afterOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: afterOrderKey}
	}
	if afterOrderKey := r.URL.Query().Get("after_sequence"); afterOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: afterOrderKey}
	}
	if afterID := r.URL.Query().Get("after"); afterID != "" {
		return store.SessionEventCursor{AfterID: afterID}
	}
	return store.SessionEventCursor{}
}
