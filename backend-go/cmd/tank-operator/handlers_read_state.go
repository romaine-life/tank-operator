package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type updateSessionReadStateRequest struct {
	LastReadOrderKey string `json:"last_read_order_key"`
}

type sessionReadStateResponse struct {
	SessionID string                        `json:"session_id"`
	ReadState *sessionReadStateResponseBody `json:"read_state"`
}

type sessionReadStateResponseBody struct {
	LastReadOrderKey string `json:"last_read_order_key"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

func (s *appServer) handleUpdateSessionReadState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	// The read-state cursor is per-caller, per-session: admin's marker on
	// someone else's session is admin's own scroll position and doesn't
	// affect the owner's. So this is treated as a read-side authorization
	// — admin can advance their cursor in any session, non-admin only in
	// their own. The Set() call below stores under the effective session
	// owner, so service principals share the human actor's cursor instead
	// of creating synthetic pod-email rows.
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	info, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	var req updateSessionReadStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	lastReadOrderKey := strings.TrimSpace(req.LastReadOrderKey)
	if lastReadOrderKey == "" {
		writeError(w, http.StatusBadRequest, "last_read_order_key is required")
		return
	}

	readStates := s.readStateStoreForScope(sessionScope)
	rec, err := readStates.Set(r.Context(), user.OwnerEmail(), sessionID, lastReadOrderKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.activityRefresher != nil && strings.EqualFold(info.Owner, user.OwnerEmail()) {
		if err := s.activityRefresher.RefreshSessionActivity(r.Context(), info.Owner, sessionScope, sessionID); err != nil {
			writeError(w, http.StatusInternalServerError, "refresh session activity: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, sessionReadStateResponse{
		SessionID: sessionID,
		ReadState: sessionReadStateBody(&rec),
	})
}

func (s *appServer) getSessionReadState(r *http.Request, email, sessionID, sessionScope string) (*store.ConversationReadStateRecord, error) {
	readStates := s.readStateStoreForScope(sessionScope)
	return readStates.Get(r.Context(), email, sessionID)
}

func (s *appServer) readStateStoreForScope(scope string) store.ConversationReadStateStore {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() && s.readStates != nil {
		return s.readStates
	}
	if s.pgPool != nil {
		return store.NewPostgresConversationReadStateStore(s.pgPool, scope)
	}
	return store.NewStubConversationReadStateStore()
}

func sessionReadStateBody(rec *store.ConversationReadStateRecord) *sessionReadStateResponseBody {
	if rec == nil || rec.LastReadOrderKey == "" {
		return nil
	}
	return &sessionReadStateResponseBody{
		LastReadOrderKey: rec.LastReadOrderKey,
		UpdatedAt:        rec.UpdatedAt,
	}
}
