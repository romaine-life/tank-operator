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
const agentWSReplayPageLimit = 1000
const agentWSReplayMaxPages = 50

type agentWSMessage struct {
	mt   websocket.MessageType
	data []byte
	err  error
}

// handleAgentWebSocket reverse-proxies the SPA's WebSocket onto the
// pod-side SDK runner listening on localhost:AgentRunnerWSPort. Auth happens
// here (JWT via cookie or query param); inside the cluster the
// orchestrator-to-pod hop trusts network policy and the pod's owner label
// check.
//
// Bytes are piped raw — no message inspection, no serialization touch.
// The runner produces the wire format; this handler is a pure pipe so
// SDK protocol changes don't require orchestrator changes. The handler adds
// one orchestrator-owned transport envelope around that pipe: replay missed
// durable events from Cosmos before flushing buffered live runner frames.
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
		writeError(w, http.StatusBadRequest, "session pod has no SDK runner container")
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

	// Buffer pod frames before replay so any live events produced during the
	// Cosmos catch-up are delivered after the subscribe ack.
	podMessages := make(chan agentWSMessage, 1024)
	go readAgentWSMessages(r.Context(), podConn, podMessages)

	cursor := sessionEventCursorFromRequest(r)
	replayed, lastOrderKey, replayErr := s.replaySessionEventsToWebSocket(
		r.Context(),
		browserConn,
		sessionID,
		cursor,
	)
	if replayErr != nil {
		_ = browserConn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{
			"type":    "tank.transport.error",
			"error":   "replay_failed",
			"message": replayErr.Error(),
		}))
		browserConn.Close(websocket.StatusInternalError, "replay failed")
		return
	}
	_ = browserConn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{
		"type":                  "tank.transport.subscribed",
		"session_id":            sessionID,
		"last_order_key":        lastOrderKey,
		"replayed":              replayed,
		"heartbeat_interval_ms": 15000,
	}))

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
	for {
		select {
		case firstErr := <-errCh:
			if firstErr != nil {
				slog.Debug("agent-ws pipe ended", "session", sessionID, "err", firstErr)
			}
			return
		case msg, open := <-podMessages:
			if !open {
				return
			}
			if msg.err != nil {
				slog.Debug("agent-ws pod reader ended", "session", sessionID, "err", msg.err)
				return
			}
			if err := browserConn.Write(r.Context(), msg.mt, msg.data); err != nil {
				slog.Debug("agent-ws browser write ended", "session", sessionID, "err", err)
				return
			}
		}
	}
}

func readAgentWSMessages(ctx context.Context, conn *websocket.Conn, out chan<- agentWSMessage) {
	defer close(out)
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			out <- agentWSMessage{err: err}
			return
		}
		out <- agentWSMessage{mt: mt, data: append([]byte(nil), data...)}
	}
}

func (s *appServer) replaySessionEventsToWebSocket(
	ctx context.Context,
	conn *websocket.Conn,
	sessionID string,
	cursor store.SessionEventCursor,
) (int, string, error) {
	replayed := 0
	lastOrderKey := cursor.AfterOrderKey
	for pageNo := 0; pageNo < agentWSReplayMaxPages; pageNo++ {
		page, err := s.sessionEvents.ListBySession(ctx, sessionID, cursor, agentWSReplayPageLimit)
		if err != nil {
			return replayed, lastOrderKey, err
		}
		for _, event := range page.Events {
			if err := conn.Write(ctx, websocket.MessageText, mustJSON(event)); err != nil {
				return replayed, lastOrderKey, err
			}
			replayed++
		}
		if page.NextOrderKey != "" {
			lastOrderKey = page.NextOrderKey
		}
		if !page.HasMore || page.NextOrderKey == "" || page.NextOrderKey == cursor.AfterOrderKey {
			break
		}
		cursor = store.SessionEventCursor{AfterOrderKey: page.NextOrderKey}
	}
	return replayed, lastOrderKey, nil
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
// renders by. The `after` document-id cursor remains accepted for older tabs.
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
	readState, err := s.getSessionReadState(r, user.Email, sessionID)
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
		"read_state":      sessionReadStateBody(readState),
	})
}

func (s *appServer) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	s.handleListSessionEvents(w, r)
}

func sessionEventCursorFromRequest(r *http.Request) store.SessionEventCursor {
	if afterOrderKey := r.URL.Query().Get("after_order_key"); afterOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: afterOrderKey}
	}
	if lastOrderKey := r.URL.Query().Get("last_order_key"); lastOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: lastOrderKey}
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		return store.SessionEventCursor{AfterOrderKey: cursor}
	}
	if cursor := r.URL.Query().Get("last_cursor"); cursor != "" {
		return store.SessionEventCursor{AfterOrderKey: cursor}
	}
	if afterOrderKey := r.URL.Query().Get("after_sequence"); afterOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: afterOrderKey}
	}
	if afterID := r.URL.Query().Get("after"); afterID != "" {
		return store.SessionEventCursor{AfterID: afterID}
	}
	return store.SessionEventCursor{}
}
