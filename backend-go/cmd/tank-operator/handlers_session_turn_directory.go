package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// The durable turn directory is the complete, submission-ordered set of
// turn_activity shells for a session — the same rows /timeline windows over,
// each stamped with its durable turn_number. It is what lets the Turns selector
// list EVERY turn (Turn 1..N) independent of the bounded transcript window the
// chat surface pages. Per the transcript-navigation contract the selectable
// turn set is owned by the durable ledger, not by whatever transcript window
// the browser happens to have loaded; this endpoint is that durable read.
//
// It mirrors /timeline across all three transcript-read surfaces (owner, public
// share token, admin hidden-session) because the same Run pane renders the
// Turns view on each. Per-turn activity bodies still load lazily through
// /turns/{turn_id}/activity; the directory carries only the shells.

// sessionTurnDirectoryBody assembles the directory response. Authorization is
// the caller's responsibility — each surface gates before calling this, exactly
// as the timeline handlers do.
func (s *appServer) sessionTurnDirectoryBody(ctx context.Context, sessionID, sessionScope string) (map[string]any, int, error) {
	// Materialize-on-read, mirroring /timeline: a projection-version bump must
	// not leave the directory stale until some other read re-materializes the
	// session, and a public share grants the whole session read-only.
	if err := s.ensureSessionTranscriptRows(ctx, sessionID, sessionScope); err != nil {
		recordTurnDirectoryList("error")
		return nil, http.StatusServiceUnavailable, fmt.Errorf("transcript materialization failed: %w", err)
	}
	rowStore := s.sessionTranscriptRowStoreForScope(sessionScope)
	page, err := rowStore.ListTurnDirectory(ctx, sessionID, store.TurnDirectoryMaxRows)
	if err != nil {
		recordTurnDirectoryList("error")
		return nil, http.StatusInternalServerError, err
	}
	if page.Truncated {
		recordTurnDirectoryList("truncated")
	} else {
		recordTurnDirectoryList("ok")
	}
	recordTurnDirectorySize(len(page.Shells))
	return map[string]any{
		"session_id":         sessionID,
		"turns":              page.Shells,
		"count":              len(page.Shells),
		"latest_turn_number": page.LatestTurnNumber,
		"truncated":          page.Truncated,
		"projection":         "server_turn_directory_v1",
	}, http.StatusOK, nil
}

// handleSessionTurnDirectory serves GET /api/sessions/{session_id}/turns/directory
// for the session owner. The literal "directory" segment is strictly more
// specific than /turns/{number}, so Go's ServeMux routes it here.
func (s *appServer) handleSessionTurnDirectory(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if _, status, err := s.authorizeSessionTranscriptReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
		writeError(w, status, err.Error())
		return
	}
	body, status, err := s.sessionTurnDirectoryBody(r.Context(), sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handlePublicMessageLinkTurnDirectory serves
// GET /api/public/message-links/{share_token}/turns/directory. A share link
// grants whole-session read-only access, so it lists the same complete turn set
// the authenticated owner sees.
func (s *appServer) handlePublicMessageLinkTurnDirectory(w http.ResponseWriter, r *http.Request) {
	share, _, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	body, status, err := s.sessionTurnDirectoryBody(r.Context(), share.SessionID, share.SessionScope)
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	recordMessageLinkShare("resolve", "ok")
	writeJSON(w, http.StatusOK, body)
}

// handleAdminHiddenSessionTurnDirectory serves
// GET /api/admin/hidden-sessions/{session_id}/turns/directory. Gating mirrors
// the admin hidden-session timeline read exactly (admin power, hidden-only).
func (s *appServer) handleAdminHiddenSessionTurnDirectory(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if s.pgPool == nil {
		writeError(w, http.StatusServiceUnavailable, "Postgres pool not wired")
		return
	}
	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	owner := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("owner")))
	if owner == "" {
		var err error
		owner, err = s.ownerForSessionInScope(r.Context(), scope, sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session owner lookup failed: "+err.Error())
			return
		}
	}
	if owner == "" {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !strings.EqualFold(owner, user.OwnerEmail()) {
		recordAdminCrossUserRead()
	}
	row, err := fetchSessionRowByID(r.Context(), s.pgPool, owner, scope, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session row read failed: "+err.Error())
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if row.Visible {
		writeError(w, http.StatusBadRequest, "session is visible")
		return
	}
	body, status, err := s.sessionTurnDirectoryBody(r.Context(), sessionID, scope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}
