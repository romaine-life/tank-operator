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

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/kubeexec"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
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

func validateCreateSessionCapabilities(mode string, raw []string) ([]string, int, string) {
	capabilities, err := sessionmodel.NormalizeSessionCapabilities(raw)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	return capabilities, 0, ""
}

type createSessionInitialTurnRequest struct {
	ClientNonce        string                               `json:"client_nonce"`
	Prompt             string                               `json:"prompt"`
	DisplayAttachments []conversation.UserMessageAttachment `json:"display_attachments,omitempty"`
	Model              string                               `json:"model,omitempty"`
	Effort             string                               `json:"effort,omitempty"`
	PermissionMode     string                               `json:"permission_mode,omitempty"`
	SkillName          string                               `json:"skill_name,omitempty"`
	Deferred           bool                                 `json:"deferred,omitempty"`
}

func validateCreateSessionInitialTurn(mode string, turn *createSessionInitialTurnRequest) (createSessionInitialTurnRequest, int, string) {
	if turn == nil {
		return createSessionInitialTurnRequest{}, 0, ""
	}
	runtime, ok := turnRuntimeForSessionMode(mode)
	if !ok {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn is only supported for durable chat sessions"
	}
	clientNonce := strings.TrimSpace(turn.ClientNonce)
	if clientNonce == "" || !turnIDPattern.MatchString(clientNonce) {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.client_nonce is required and must match turn id syntax"
	}
	prompt := strings.TrimSpace(turn.Prompt)
	if prompt == "" {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.prompt is required"
	}
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.prompt too large"
	}
	displayAttachments, attachmentStatus, attachmentDetail := normalizeDisplayAttachments(turn.DisplayAttachments)
	if attachmentStatus != 0 {
		return createSessionInitialTurnRequest{}, attachmentStatus, "initial_turn." + attachmentDetail
	}
	skillName := validateSkillName(turn.SkillName)
	if strings.TrimSpace(turn.SkillName) != "" && skillName == "" {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.skill_name is invalid"
	}
	if skillName != "" && !promptMatchesSkillTrigger(runtime, skillName, prompt) {
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.skill_name does not match prompt trigger"
	}
	effort := validateEffort(runtime, strings.TrimSpace(turn.Effort))
	if strings.TrimSpace(turn.Effort) != "" && effort == "" {
		if runtime == "codex" {
			return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.effort is invalid; want one of low|medium|high|xhigh"
		}
		return createSessionInitialTurnRequest{}, http.StatusBadRequest, "initial_turn.effort is invalid; want one of low|medium|high|xhigh|max"
	}
	return createSessionInitialTurnRequest{
		ClientNonce:        clientNonce,
		Prompt:             prompt,
		DisplayAttachments: displayAttachments,
		Model:              strings.TrimSpace(turn.Model),
		Effort:             effort,
		PermissionMode:     strings.TrimSpace(turn.PermissionMode),
		SkillName:          skillName,
		Deferred:           turn.Deferred,
	}, 0, ""
}

func turnRuntimeForSessionMode(mode string) (string, bool) {
	return sdkProviderForMode(mode)
}

// handleCreateSession creates a new session pod. Accepts the optional
// `repos[]` selection from the splash picker and optional `bug_label`
// staged on the same new-session surface. Repos are persisted on the
// registry row by manager.Create and auto-cloned into /workspace by the
// repo-cloner init container at pod boot; bug labels are normalized at this
// boundary and attached before the POST response is returned.
func (s *appServer) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode         string                           `json:"mode"`
		Model        string                           `json:"model,omitempty"`
		Effort       string                           `json:"effort,omitempty"`
		Name         *string                          `json:"name,omitempty"`
		Repos        []string                         `json:"repos"`
		BugLabel     *string                          `json:"bug_label,omitempty"`
		Capabilities []string                         `json:"capabilities"`
		InitialTurn  *createSessionInitialTurnRequest `json:"initial_turn,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Mode = ""
		body.Model = ""
		body.Effort = ""
		body.Name = nil
		body.Repos = nil
		body.BugLabel = nil
		body.InitialTurn = nil
	}
	mode := sessionmodel.NormalizeSessionMode(body.Mode)
	repos, err := validateRepoSlugs(body.Repos)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(repos) > 0 && !sessionModeSupportsRepos(mode) {
		writeError(w, http.StatusBadRequest, errReposUnsupportedForMode.Error())
		return
	}
	bugLabel, err := sessionmodel.NormalizeBugLabelName(body.BugLabel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	capabilities, status, detail := validateCreateSessionCapabilities(mode, body.Capabilities)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	runConfig, status, detail := validateCreateRunConfig(mode, body.Model, body.Effort)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	initialTurn, status, detail := validateCreateSessionInitialTurn(mode, body.InitialTurn)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	if body.InitialTurn != nil && s.sessionEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "initial_turn submit path unavailable")
		return
	}
	if body.InitialTurn != nil && !initialTurn.Deferred && s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "initial_turn submit path unavailable")
		return
	}
	owner := user.OwnerEmail()
	launchTurnAt := time.Time{}
	requestedAt := ""
	if body.InitialTurn != nil {
		launchTurnAt = time.Now().UTC()
		requestedAt = launchTurnAt.Add(2 * time.Millisecond).Format(time.RFC3339Nano)
	}
	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner:        owner,
		Mode:         mode,
		Name:         body.Name,
		Repos:        repos,
		BugLabel:     bugLabel,
		Capabilities: capabilities,
		Model:        runConfig.Model,
		Effort:       runConfig.Effort,
		RequestedAt:  requestedAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.InitialTurn != nil {
		if initialTurn.Deferred {
			if status, detail := s.persistInitialTurnUserMessage(r.Context(), owner, info.ID, initialTurn, launchTurnAt, authorKindForUser(user)); status != 0 {
				s.rollbackCreatedSession(r.Context(), owner, info.ID, "persist deferred initial turn", detail)
				writeError(w, status, detail)
				return
			}
		} else {
			if _, status, detail := s.enqueueSDKTurn(r.Context(), owner, info.ID, sdkTurnRequest{
				ClientNonce:        initialTurn.ClientNonce,
				RequireNonce:       true,
				Prompt:             initialTurn.Prompt,
				DisplayAttachments: initialTurn.DisplayAttachments,
				Model:              initialTurn.Model,
				Effort:             initialTurn.Effort,
				PermissionMode:     initialTurn.PermissionMode,
				SkillName:          initialTurn.SkillName,
				FollowUp:           false,
				AllowBeforeReady:   true,
				SessionMode:        info.Mode,
				CreatedAt:          launchTurnAt,
				OrderBase:          launchTurnAt,
				AuthorKind:         authorKindForUser(user),
			}); status != 0 {
				s.rollbackCreatedSession(r.Context(), owner, info.ID, "submit initial turn", detail)
				writeError(w, status, detail)
				return
			}
		}
	}
	// Provider-credential backfill: when a new session's mode requires a
	// provider whose Layer 1 row is currently in a failed state, emit a
	// session.status:failed banner into the freshly-created session's
	// transcript ledger so the SPA renders the same "<provider> sign-in
	// expired" line that already-active sessions see. The ordering rule
	// from docs/features/transcript/contract.md is satisfied: this fires
	// AFTER the initial-turn block above writes user_message.created (or
	// the deferred persistInitialTurnUserMessage) and AFTER manager.Create
	// inserts the sessions row whose trigger emits session.status:ready —
	// never before user_message. A best-effort error here (Postgres
	// blip, NATS not yet healthy) is logged but does not roll back the
	// session create; the next poll cycle will fan out and pick it up.
	s.backfillProviderHealthBanner(r.Context(), owner, info)
	sessionReposSelectedTotal.WithLabelValues(repoSelectionBucket(len(repos))).Inc()
	writeJSON(w, http.StatusCreated, info)
}

// backfillProviderHealthBanner is the session-create-time read-side of
// the provider-credential-banner pipeline. The poll loop is the steady-
// state writer; this method covers the gap for sessions created during
// a sustained outage. Skipped silently when the mode's provider has no
// Layer 1 entry yet (nothing to backfill) or when the row is healthy.
func (s *appServer) backfillProviderHealthBanner(ctx context.Context, owner string, info sessions.Info) {
	if s == nil || s.providerHealth == nil {
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		return
	}
	row, present, err := s.providerHealth.CurrentHealth(ctx, provider)
	if err != nil {
		slog.Warn("providerhealth current-health lookup failed on session create",
			"provider", provider, "session_id", info.ID, "error", err)
		return
	}
	if !present || row.Status != pgstore.ProviderHealthStatusFailed {
		return
	}
	if err := s.providerHealth.EmitForSession(ctx, provider, info.ID, owner, row); err != nil {
		slog.Warn("providerhealth session-create backfill emit failed",
			"provider", provider, "session_id", info.ID, "error", err)
	}
}

func (s *appServer) persistInitialTurnUserMessage(ctx context.Context, owner, sessionID string, turn createSessionInitialTurnRequest, createdAt time.Time, authorKind string) (int, string) {
	info, err := s.mgr.GetByOwner(ctx, owner, sessionID)
	if err != nil {
		return http.StatusNotFound, "session not found"
	}
	runtime, ok := turnRuntimeForSessionMode(info.Mode)
	if !ok {
		return http.StatusBadRequest, "session mode does not support durable initial turns"
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	_, events, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             owner,
		ClientNonce:       turn.ClientNonce,
		Text:              turn.Prompt,
		Message:           map[string]any{"role": "user", "content": turn.Prompt},
		Attachments:       turn.DisplayAttachments,
		Runtime:           runtime,
		SkillName:         turn.SkillName,
		AuthorKind:        strings.TrimSpace(authorKind),
		Now:               createdAt.UTC(),
	})
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	retimeTurnBoundaryEvents(events, createdAt)
	for _, event := range events {
		if event["type"] != string(conversation.EventUserMessageCreated) {
			continue
		}
		if writeErr := s.persistBackendEvent(ctx, storageKey, event); writeErr != nil {
			return http.StatusInternalServerError, "persist initial user message: " + writeErr.Error()
		}
		return 0, ""
	}
	return http.StatusInternalServerError, "initial user message event missing"
}

func (s *appServer) rollbackCreatedSession(ctx context.Context, owner, sessionID, action, detail string) {
	if s == nil || s.mgr == nil {
		return
	}
	if err := s.mgr.Delete(ctx, owner, sessionID); err != nil {
		slog.Warn("create session rollback failed",
			"session_id", sessionID,
			"owner", owner,
			"action", action,
			"detail", detail,
			"error", err,
		)
	}
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
func (s *appServer) stampSnapshotCursorHeader(ctx context.Context, w http.ResponseWriter, owner, scope string) {
	if s.pgPool == nil {
		return
	}
	cursor, err := queryRowVersionTip(ctx, s.pgPool, owner, scope)
	if err != nil {
		slog.Warn("list sessions: snapshot cursor lookup failed",
			"owner", owner, "scope", scope, "error", err)
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
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}

	// Stamp Tank-Sessions-Snapshot-Cursor BEFORE listing sessions so
	// the cursor is conservative (older than every row included in the
	// snapshot). The SPA hands this to the SSE handler as its initial
	// cursor; the row-update catch-up then covers anything that
	// changed during the snapshot query itself.
	s.stampSnapshotCursorHeader(r.Context(), w, owner, sessionScope)

	infos, err := s.listSessionsInScope(r.Context(), owner, sessionScope)
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
	user, sessionScope, ok := s.requireBrowserStreamAuth(w, r, streamKindSessionList, "")
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
		tip, err := queryRowVersionTip(r.Context(), s.pgPool, owner, sessionScope)
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

	natsCh, unsubscribe, err := s.sessionBus.SubscribeSessionRowUpdates(r.Context(), owner, sessionScope)
	if err != nil {
		recordSessionListStreamError()
		slog.Warn("session row updates subscribe failed", "caller", user.Email, "owner", owner, "scope", sessionScope, "error", err)
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
		hasMore, written, err := s.writeSessionRowUpdatesPage(r.Context(), w, owner, sessionScope, &cursor)
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
			s.emitSessionRowPayload(w, &cursor, sessionScope, payload)
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

// handleReorderSessions persists the caller's complete visible sidebar
// order. The backend owns the durable order; browser-local ordering is
// not a source of truth.
func (s *appServer) handleReorderSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	owner := user.OwnerEmail()
	if err := s.mgr.ReorderSessions(r.Context(), owner, body.SessionIDs); err != nil {
		if errors.Is(err, sessionmodel.ErrSessionOrderConflict) {
			writeError(w, http.StatusConflict, "session order is stale; refresh and retry")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		Active         bool    `json:"active"`
		SlotIndex      *int    `json:"slot_index"`
		URL            *string `json:"url"`
		PullRequestURL *string `json:"pull_request_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	owner := user.OwnerEmail()
	info, err := s.mgr.SetTestState(r.Context(), owner, sessionID, body.Active, body.SlotIndex, body.URL, body.PullRequestURL)
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
		AuthorKind:     authorKindForUser(user),
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
		Model                 string   `json:"model,omitempty"`
		Effort                string   `json:"effort,omitempty"`
		Name                  *string  `json:"name,omitempty"`
		Repos                 []string `json:"repos"`
		Capabilities          []string `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	email := user.OwnerEmail()
	if body.CallerEmail != "" {
		email = body.CallerEmail
	}
	mode := sessionmodel.NormalizeSessionMode(body.Mode)

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
	if len(repos) > 0 && !sessionModeSupportsRepos(mode) {
		writeError(w, http.StatusBadRequest, errReposUnsupportedForMode.Error())
		return
	}
	capabilities, status, detail := validateCreateSessionCapabilities(mode, body.Capabilities)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	runConfig, status, detail := validateCreateRunConfig(mode, body.Model, body.Effort)
	if status != 0 {
		writeError(w, status, detail)
		return
	}

	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner:           email,
		Mode:            mode,
		GlimmungContext: glimmungContext,
		Name:            body.Name,
		Repos:           repos,
		Capabilities:    capabilities,
		Model:           runConfig.Model,
		Effort:          runConfig.Effort,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.backfillProviderHealthBanner(r.Context(), email, info)
	sessionReposSelectedTotal.WithLabelValues(repoSelectionBucket(len(repos))).Inc()
	writeJSON(w, http.StatusCreated, info)
}
