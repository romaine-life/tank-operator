package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

// repoSelectionBucket coarsely bins a repo-count for the
// tank_session_repos_selected_total counter. Three labels keep
// cardinality bounded ("none" / "one" / "many") while still surfacing
// the operational shape: are users selecting any repos at all, and
// is the "many" path getting exercised? The exact count is
// recoverable from the durable column when needed.
func repoSelectionBucket(count int) string {
	switch {
	case count <= 0:
		return "none"
	case count == 1:
		return "one"
	default:
		return "many"
	}
}

// handleCreateSession creates a new session pod. Accepts the optional
// `repos[]` selection from the splash picker; the slugs are validated
// at this boundary (validateRepoSlugs / sessionModeSupportsRepos),
// persisted on the registry row by manager.Create, and auto-cloned into
// /workspace by the repo-cloner init container at pod boot.
func (s *appServer) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode  string   `json:"mode"`
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Mode = ""
		body.Repos = nil
	}
	repos, err := validateRepoSlugs(body.Repos)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(repos) > 0 && !sessionModeSupportsRepos(body.Mode) {
		writeError(w, http.StatusBadRequest, errReposUnsupportedForMode.Error())
		return
	}
	owner := user.OwnerEmail()
	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner: owner,
		Mode:  body.Mode,
		Repos: repos,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessionReposSelectedTotal.WithLabelValues(repoSelectionBucket(len(repos))).Inc()
	writeJSON(w, http.StatusCreated, info)
}

// stampSnapshotCursorHeader writes Tank-Sessions-Snapshot-Cursor on
// the response: MAX(row_version) for (owner, scope) at snapshot time.
// The SPA passes this value as the SSE cursor when it opens
// /api/sessions/events, so the row-update catch-up only emits rows
// that changed AFTER the snapshot — closes the race between the
// snapshot query and the SSE open. Absent header (fresh owner, no
// rows yet) is the signal that lets the SSE handler fast-forward an
// empty cursor to current tip on its own.
//
// Phase 3 of docs/session-list-redesign.md made this the only
// session-list cursor on the wire; the pre-Phase-3 ledger-tip
// header was retired in the same PR.
func (s *appServer) stampSnapshotCursorHeader(ctx context.Context, w http.ResponseWriter, owner string) {
	if s.pgPool == nil {
		return
	}
	cursor, err := queryRowVersionTip(ctx, s.pgPool, owner, s.sessionScope)
	if err != nil {
		slog.Warn("list sessions: snapshot cursor lookup failed",
			"owner", owner, "scope", s.sessionScope, "error", err)
		return
	}
	if cursor == 0 {
		return
	}
	w.Header().Set("Tank-Sessions-Snapshot-Cursor", fmt.Sprintf("%d", cursor))
}

// handleListSessions lists sessions for the authenticated user, or for
// a different owner when an admin passes `?owner=<email>`. The query
// param is the explicit signal that unlocks the admin cross-user path
// (intentional opt-in so the default response stays own-only and an
// admin token isn't a footgun for tools that didn't expect cross-user
// reads).
//
// The Tank-Sessions-Snapshot-Cursor response header carries
// MAX(row_version) for this (owner, scope) at snapshot time. The
// SPA passes that value as the SSE cursor when it opens
// /api/sessions/events, so the row-update catch-up only emits row
// changes that landed *after* the snapshot — closing the race
// between the snapshot query and the SSE open. Without this header,
// the SSE handler fast-forwards an empty cursor to the current tip;
// either way, cold opens never replay history. See
// docs/product-inspirations.md: "Live transport should wake clients
// and runners; it should not be the only place product state exists" —
// the snapshot is the source of cold-open state, lifecycle events are
// deltas on top.
func (s *appServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	owner := listSessionsOwner(user, r)

	// Stamp Tank-Sessions-Snapshot-Cursor BEFORE listing sessions so
	// the cursor is conservative (older than every row included in the
	// snapshot). The SPA hands this to the SSE handler as its initial
	// cursor; the row-update catch-up then covers anything that
	// changed during the snapshot query itself.
	s.stampSnapshotCursorHeader(r.Context(), w, owner)

	infos, err := s.mgr.ListSessions(r.Context(), owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// handleSessionsEvents streams row-update payloads over SSE for the
// per-owner session list. Per docs/session-list-redesign.md Phase 3
// the wire shape is the row itself — `id:` = row_version, `event:
// ready` on open, `event: session-row` per row update,
// `event: stream-error` on transport failures.
//
// Catch-up at open: rows whose row_version > cursor are streamed from
// the sessions table before subscribing to NATS, so a slow reconnect
// doesn't silently miss updates that landed during the disconnect
// window. Live delivery is driven by NATS payloads forwarded verbatim.
// The SPA's SessionStore is a row cache that replaces-by-id on each
// delivery — no event-type switch, no placeholder synthesis.
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

	cursor := parseRowVersionCursor(r)

	// Cursor-empty cold-open fast-forward: when the client opens with
	// no Last-Event-ID and no after_row_version query param, the server
	// jumps the cursor to the current MAX(row_version). Cold opens get
	// their state from GET /api/sessions; replaying every row from
	// row_version=0 is the bug class the redesign retires. New clients
	// pass the Tank-Sessions-Snapshot-Cursor header value they got from
	// the snapshot response, so the catch-up still covers any row
	// updates that landed between the snapshot query and the SSE open.
	if cursor == 0 && s.pgPool != nil {
		tip, err := queryRowVersionTip(r.Context(), s.pgPool, owner, s.sessionScope)
		if err != nil {
			recordSessionListStreamError()
			writeSSEJSONEvent(w, "stream-error", "", map[string]any{
				"reason": "tip_lookup_failed",
				"detail": err.Error(),
			})
			flusher.Flush()
			return
		}
		if tip > 0 {
			cursor = tip
			sessionListStreamColdOpenFastForwardTotal.Inc()
		}
	}

	writeSSEJSONEvent(w, "ready", "", map[string]any{
		"cursor": fmt.Sprintf("%d", cursor),
	})
	flusher.Flush()
	sessionListStreamOpenTotal.Inc()
	if cursor > 0 {
		sessionListStreamReconnectTotal.Inc()
	}

	natsCh, unsubscribe, err := s.sessionBus.SubscribeSessionRowUpdates(r.Context(), owner, s.sessionScope)
	if err != nil {
		recordSessionListStreamError()
		slog.Warn("session row updates subscribe failed", "caller", user.Email, "owner", owner, "scope", s.sessionScope, "error", err)
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "subscribe_failed",
			"detail": err.Error(),
		})
		flusher.Flush()
		return
	}
	defer unsubscribe()

	// Catch up from the sessions table for any row that changed past
	// the cursor and before the NATS subscription was active. Pages
	// capped at listEventStreamPageLimit; we loop until the page is
	// short.
	for {
		hasMore, written, err := s.writeSessionRowUpdatesPage(r.Context(), w, owner, &cursor)
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
			slog.Debug("session row updates catch-up emitted rows",
				"caller", user.Email,
				"owner", owner,
				"count", written,
				"cursor", cursor,
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
			s.emitSessionRowPayload(w, &cursor, payload)
			flusher.Flush()
		case <-keepalive.C:
			sessionListStreamHeartbeatTotal.Inc()
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// queryRowVersionTip returns MAX(row_version) for (owner, scope) or 0
// when the owner has no rows yet. Used both by the snapshot-cursor
// header and the SSE cold-open fast-forward.
func queryRowVersionTip(ctx context.Context, pool *pgxpool.Pool, owner, scope string) (int64, error) {
	const q = `
		SELECT COALESCE(MAX(row_version), 0)
		FROM sessions
		WHERE email = $1 AND session_scope = $2
	`
	var tip int64
	if err := pool.QueryRow(ctx, q, owner, scope).Scan(&tip); err != nil {
		return 0, err
	}
	return tip, nil
}

// parseRowVersionCursor extracts the SSE cursor from either the
// EventSource Last-Event-ID header (set by the browser on auto-
// reconnect — value is the last `id:` line we sent) or the explicit
// `after_row_version` query param. Non-integer values silently
// degrade to 0 (cold-open fast-forward); a legitimate cursor is a
// stringified BIGSERIAL row_version.
func parseRowVersionCursor(r *http.Request) int64 {
	for _, raw := range []string{
		r.Header.Get("Last-Event-ID"),
		r.URL.Query().Get("after_row_version"),
	} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
			return v
		}
	}
	return 0
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
	owner := user.OwnerEmail()
	if err := s.mgr.Delete(r.Context(), owner, sessionID); err != nil {
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
	owner := user.OwnerEmail()
	if _, err := s.mgr.GetByOwner(r.Context(), owner, sessionID); err != nil {
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
	owner := user.OwnerEmail()
	info, err := s.mgr.SetName(r.Context(), owner, sessionID, body.Name)
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
	owner := user.OwnerEmail()
	info, err := s.mgr.SetTestState(r.Context(), owner, sessionID, body.Active, body.SlotIndex, body.URL)
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
	owner := user.OwnerEmail()
	info, err := s.mgr.SetRolloutState(r.Context(), owner, sessionID, body.Active)
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

	owner := user.OwnerEmail()
	info, podName, herr := s.resolveSessionPod(r.Context(), owner, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	doSaveCredentials(w, r, s, owner, info.Mode, podName)
}

// handlePasteImage saves a pasted image into /workspace/.tank-pastes/{session_id}/.
func (s *appServer) handlePasteImage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))

	owner := user.OwnerEmail()
	_, podName, herr := s.resolveSessionPod(r.Context(), owner, sessionID)
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

	owner := user.OwnerEmail()
	resp, status, detail := s.enqueueSDKTurn(r.Context(), owner, sessionID, sdkTurnRequest{
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
		GlimmungRunRef        string   `json:"glimmung_run_ref"`
		GlimmungIssueRef      string   `json:"glimmung_issue_ref"`
		GlimmungTouchpointRef string   `json:"glimmung_touchpoint_ref"`
		ValidationURL         string   `json:"validation_url"`
		CallerEmail           string   `json:"caller_email"`
		Mode                  string   `json:"mode"`
		Repos                 []string `json:"repos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	email := user.OwnerEmail()
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

	repos, err := validateRepoSlugs(body.Repos)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(repos) > 0 && !sessionModeSupportsRepos(body.Mode) {
		writeError(w, http.StatusBadRequest, errReposUnsupportedForMode.Error())
		return
	}

	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner:           email,
		Mode:            body.Mode,
		GlimmungContext: glimmungContext,
		Repos:           repos,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessionReposSelectedTotal.WithLabelValues(repoSelectionBucket(len(repos))).Inc()
	writeJSON(w, http.StatusCreated, info)
}
