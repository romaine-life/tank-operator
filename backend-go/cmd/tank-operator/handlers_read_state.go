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
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
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

	readStates := s.readStates
	if readStates == nil {
		readStates = store.NewStubConversationReadStateStore()
	}
	rec, err := readStates.Set(r.Context(), user.Email, sessionID, lastReadOrderKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionReadStateResponse{
		SessionID: sessionID,
		ReadState: sessionReadStateBody(&rec),
	})
}

func (s *appServer) getSessionReadState(r *http.Request, email, sessionID string) (*store.ConversationReadStateRecord, error) {
	readStates := s.readStates
	if readStates == nil {
		return nil, nil
	}
	return readStates.Get(r.Context(), email, sessionID)
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
