package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	adminHiddenSessionsDefaultLimit = 100
	adminHiddenSessionsMaxLimit     = 500
)

type adminHiddenSessionRow struct {
	Owner              string    `json:"owner"`
	SessionID          string    `json:"session_id"`
	Name               string    `json:"name"`
	Mode               string    `json:"mode"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	TranscriptRowCount int64     `json:"transcript_row_count"`
}

func (s *appServer) handleAdminHiddenSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
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
	if owner != "" && !strings.EqualFold(owner, user.OwnerEmail()) {
		recordAdminCrossUserList()
	}
	limit := adminHiddenSessionsDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > adminHiddenSessionsMaxLimit {
		limit = adminHiddenSessionsMaxLimit
	}

	rows, err := s.listAdminHiddenSessions(r, scope, owner, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hidden session list failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":    scope,
		"owner":    owner,
		"sessions": rows,
	})
}

func (s *appServer) handleAdminHiddenSessionTimeline(w http.ResponseWriter, r *http.Request) {
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

	body, status, err := s.sessionTimelineBody(r.Context(), r, user, sessionID, scope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	body["scope"] = scope
	body["owner"] = owner
	body["session"] = map[string]any{
		"owner":       owner,
		"session_id":  row.ID,
		"name":        row.Name,
		"mode":        row.Mode,
		"status":      row.Status,
		"visible":     row.Visible,
		"updated_at":  row.UpdatedAt,
		"activity":    row.ActivitySummary,
		"storage_key": sessionmodel.SessionStorageKey(scope, row.ID),
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) listAdminHiddenSessions(r *http.Request, scope, owner string, limit int) ([]adminHiddenSessionRow, error) {
	const q = `
		SELECT sessions.email,
			sessions.session_id,
			sessions.name,
			sessions.mode,
			sessions.status,
			sessions.created_at,
			sessions.updated_at,
			COALESCE(transcript_rows.row_count, 0)
		FROM sessions
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS row_count
			FROM session_transcript_rows
			WHERE tank_session_id = CASE
				WHEN sessions.session_scope = 'default' THEN sessions.session_id
				ELSE sessions.session_scope || ':' || sessions.session_id
			END
		) transcript_rows ON true
		WHERE sessions.session_scope = $1
		  AND sessions.visible IS NOT TRUE
		  AND ($2 = '' OR sessions.email = $2)
		ORDER BY sessions.updated_at DESC, sessions.created_at DESC, sessions.session_id DESC
		LIMIT $3
	`
	rows, err := s.pgPool.Query(r.Context(), q, normalizeSessionScope(scope), owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]adminHiddenSessionRow, 0, limit)
	for rows.Next() {
		var row adminHiddenSessionRow
		var name, mode, status sql.NullString
		if err := rows.Scan(
			&row.Owner,
			&row.SessionID,
			&name,
			&mode,
			&status,
			&row.CreatedAt,
			&row.UpdatedAt,
			&row.TranscriptRowCount,
		); err != nil {
			return nil, err
		}
		row.Owner = strings.ToLower(strings.TrimSpace(row.Owner))
		row.Name = name.String
		row.Mode = mode.String
		if row.Mode == "" {
			row.Mode = sessionmodel.DefaultSessionMode
		}
		row.Status = status.String
		if row.Name == "" {
			row.Name = sessionmodel.SessionDisplayName(nil, "", row.SessionID)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
