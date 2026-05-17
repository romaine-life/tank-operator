package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

// handleCreateSession creates a new session pod.
func (s *appServer) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Mode = ""
	}
	info, err := s.mgr.Create(r.Context(), user.Email, body.Mode, nil, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// handleListSessions lists sessions for the authenticated user, or for
// a different owner when an admin passes `?owner=<email>`. The query
// param is the explicit signal that unlocks the admin cross-user path
// (intentional opt-in so the default response stays own-only and an
// admin token isn't a footgun for tools that didn't expect cross-user
// reads).
func (s *appServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	owner := listSessionsOwner(user, r)
	infos, err := s.mgr.ListSessions(r.Context(), owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// handleSessionsEvents streams typed session-lifecycle events over SSE
// for the per-owner session list. Mirrors the chat-window SSE shape on
// /api/sessions/{id}/events: `id:` = order_key, `event: ready` on open,
// `event: session-event` per typed payload, `event: resync_required`
// when the client's Last-Event-ID isn't in the durable ledger, and
// `event: stream-error` on transport failures.
//
// Catch-up at open: any rows past the cursor are streamed from Postgres
// before subscribing to NATS so a slow reconnect doesn't silently miss
// events that landed during the disconnect window. Live delivery is
// driven by NATS payloads forwarded verbatim.
func (s *appServer) handleSessionsEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	owner := listSessionsOwner(user, r)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	cursor := sessionListCursorFromRequest(r)
	if s.lifecycleEvents != nil {
		if ok, err := s.lifecycleEvents.HasOrderKey(r.Context(), owner, cursor.AfterOrderKey); err != nil {
			recordSessionListStreamError()
			writeSSEJSONEvent(w, "stream-error", "", map[string]any{
				"reason": "cursor_check_failed",
				"detail": err.Error(),
			})
			flusher.Flush()
			return
		} else if !ok {
			sessionListStreamResyncTotal.Inc()
			slog.Warn("session list stream resync required",
				"caller", user.Email,
				"owner", owner,
				"last_order_key", cursor.AfterOrderKey,
			)
			writeSSEJSONEvent(w, "resync_required", "", map[string]any{
				"reason":         "cursor_not_found",
				"last_order_key": cursor.AfterOrderKey,
			})
			flusher.Flush()
			return
		}
	}

	writeSSEJSONEvent(w, "ready", "", map[string]any{
		"last_order_key": cursor.AfterOrderKey,
	})
	flusher.Flush()
	sessionListStreamOpenTotal.Inc()
	if cursor.AfterOrderKey != "" {
		sessionListStreamReconnectTotal.Inc()
	}

	natsCh, unsubscribe, err := s.sessionBus.SubscribeSessionListEvents(r.Context(), owner)
	if err != nil {
		recordSessionListStreamError()
		slog.Warn("session list events subscribe failed", "caller", user.Email, "owner", owner, "error", err)
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "subscribe_failed",
			"detail": err.Error(),
		})
		flusher.Flush()
		return
	}
	defer unsubscribe()

	// Catch up from Postgres for anything that landed after the cursor
	// and before the NATS subscription was active. Pages capped at
	// listEventStreamPageLimit; we loop until the page is short.
	for {
		hasMore, written, err := s.writeSessionListStreamPage(r.Context(), w, owner, &cursor)
		if err != nil {
			recordSessionListStreamError()
			writeSSEJSONEvent(w, "stream-error", "", map[string]any{
				"reason": "catch_up_failed",
				"detail": err.Error(),
			})
			flusher.Flush()
			return
		}
		flusher.Flush()
		if written > 0 {
			slog.Debug("session list stream catch-up emitted events",
				"caller", user.Email,
				"owner", owner,
				"count", written,
				"last_order_key", cursor.AfterOrderKey,
				"has_more", hasMore,
			)
		}
		if !hasMore {
			break
		}
	}

	keepalive := time.NewTicker(sessionListStreamHeartbeat)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, ok := <-natsCh:
			if !ok {
				return
			}
			s.emitSessionListPayload(w, &cursor, payload)
			flusher.Flush()
		case <-keepalive.C:
			sessionListStreamHeartbeatTotal.Inc()
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// handleDeleteSession deletes a session.
func (s *appServer) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if err := s.mgr.Delete(r.Context(), user.Email, sessionID); err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusNotFound, "session not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetSession returns info for a single session.
func (s *appServer) handleGetSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	info, status, err := s.authorizeSessionRead(r.Context(), user, sessionID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleTouchSession updates the idle timer for a session.
func (s *appServer) handleTouchSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	// Verify ownership.
	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	s.mgr.Touch(sessionID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePatchSession sets the display name.
func (s *appServer) handlePatchSession(w http.ResponseWriter, r *http.Request) {
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
		Name *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetName(r.Context(), user.Email, sessionID, body.Name)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, info)
	case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
		writeError(w, http.StatusNotFound, "session not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// handleSetTestState sets the test state annotation.
func (s *appServer) handleSetTestState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	var body struct {
		Active    bool    `json:"active"`
		SlotIndex *int    `json:"slot_index"`
		URL       *string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetTestState(r.Context(), user.Email, sessionID, body.Active, body.SlotIndex, body.URL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleSetRolloutState sets the rollout state annotation.
func (s *appServer) handleSetRolloutState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	var body struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetRolloutState(r.Context(), user.Email, sessionID, body.Active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleSaveCredentials harvests credentials from a session pod and writes to Key Vault.
func (s *appServer) handleSaveCredentials(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))

	info, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	doSaveCredentials(w, r, s, user.Email, info.Mode, podName)
}

// handlePasteImage saves a pasted image into /workspace/.tank-pastes/{session_id}/.
func (s *appServer) handlePasteImage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))

	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		ext := "png"
		if ct := r.Header.Get("Content-Type"); strings.Contains(ct, "jpeg") || strings.Contains(ct, "jpg") {
			ext = "jpg"
		}
		name = fmt.Sprintf("clipboard-%d.%s", time.Now().UnixMilli(), ext)
	}

	pasteDir := fmt.Sprintf("/workspace/.tank-pastes/%s", sessionID)
	destPath := pasteDir + "/" + name

	data, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	if err := kubeexec.WriteFile(r.Context(), s.k8s, s.restCfg, s.namespace, podName, destPath, data); err != nil {
		writeError(w, http.StatusInternalServerError, "write file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"path": destPath})
}

// handleSendMessage enqueues a fire-and-forget follow-up turn to a chat-capable session.
func (s *appServer) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))

	var body struct {
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		SkillName      string `json:"skill_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		writeError(w, http.StatusBadRequest, "missing prompt")
		return
	}

	resp, status, detail := s.enqueueSDKTurn(r.Context(), user.Email, sessionID, sdkTurnRequest{
		Prompt:         body.Prompt,
		Model:          body.Model,
		PermissionMode: body.PermissionMode,
		SkillName:      body.SkillName,
		FollowUp:       true,
	})
	if detail != "" {
		writeError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// handleCreateSessionWithContext creates a session with glimmung context.
func (s *appServer) handleCreateSessionWithContext(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		GlimmungRunRef        string `json:"glimmung_run_ref"`
		GlimmungIssueRef      string `json:"glimmung_issue_ref"`
		GlimmungTouchpointRef string `json:"glimmung_touchpoint_ref"`
		ValidationURL         string `json:"validation_url"`
		CallerEmail           string `json:"caller_email"`
		Mode                  string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	email := user.Email
	if body.CallerEmail != "" {
		email = body.CallerEmail
	}

	glimmungContext := map[string]any{}
	if body.GlimmungRunRef != "" {
		glimmungContext["glimmung_run_ref"] = body.GlimmungRunRef
	}
	if body.GlimmungIssueRef != "" {
		glimmungContext["glimmung_issue_ref"] = body.GlimmungIssueRef
	}
	if body.GlimmungTouchpointRef != "" {
		glimmungContext["glimmung_touchpoint_ref"] = body.GlimmungTouchpointRef
	}
	if body.ValidationURL != "" {
		glimmungContext["validation_url"] = body.ValidationURL
	}

	info, err := s.mgr.Create(r.Context(), email, body.Mode, glimmungContext, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, info)
}
