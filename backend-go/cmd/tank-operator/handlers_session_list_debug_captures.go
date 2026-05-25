package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

const (
	sessionListDebugCaptureMaxBodyBytes = 768 * 1024
	sessionListDebugCaptureMaxLimit     = 100
	sessionListDebugCaptureRetention    = 200
)

const debugSessionListCapturesDescription = `Durable browser-side session-list captures.

The SPA posts a bounded /_debug/session-list snapshot when a just-created
session row mutates its client-side name or avatar identity. Query this
admin endpoint to inspect the captured client render/store/events plus the
server registry rows recorded at ingest time.`

type sessionListDebugCaptureRequest struct {
	Reason    string          `json:"reason"`
	SessionID string          `json:"session_id"`
	Source    string          `json:"source"`
	Location  string          `json:"location"`
	ActiveID  string          `json:"active_id"`
	ClientSeq int64           `json:"client_seq"`
	Detail    json.RawMessage `json:"detail"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

type sessionListDebugCaptureRow struct {
	ID           string          `json:"id"`
	OwnerEmail   string          `json:"owner_email"`
	SessionScope string          `json:"session_scope"`
	SessionID    string          `json:"session_id"`
	Reason       string          `json:"reason"`
	Source       string          `json:"source"`
	Location     string          `json:"location"`
	ActiveID     string          `json:"active_id"`
	ClientSeq    int64           `json:"client_seq"`
	Snapshot     json.RawMessage `json:"snapshot"`
	Detail       json.RawMessage `json:"detail"`
	ServerRows   json.RawMessage `json:"server_rows"`
	CreatedAt    time.Time       `json:"created_at"`
}

func (s *appServer) handleSessionListDebugCapture(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !user.IsHuman() {
		recordSessionListDebugCapture("denied_role", "")
		writeError(w, http.StatusForbidden, "human user required")
		return
	}

	var body sessionListDebugCaptureRequest
	limited := http.MaxBytesReader(w, r.Body, sessionListDebugCaptureMaxBodyBytes)
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&body); err != nil {
		recordSessionListDebugCapture("invalid_json", "")
		writeError(w, http.StatusBadRequest, "invalid session-list debug capture payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		recordSessionListDebugCapture("invalid_json", "")
		writeError(w, http.StatusBadRequest, "invalid session-list debug capture payload")
		return
	}

	rawReason := strings.TrimSpace(body.Reason)
	reason := clampSessionListDebugCaptureReason(rawReason)
	sessionID := clampDebugCaptureString(body.SessionID, 80)
	if rawReason == "" || sessionID == "" {
		recordSessionListDebugCapture("invalid_value", reason)
		writeError(w, http.StatusBadRequest, "reason and session_id are required")
		return
	}
	if !validJSONObject(body.Snapshot) {
		recordSessionListDebugCapture("invalid_value", reason)
		writeError(w, http.StatusBadRequest, "snapshot must be a JSON object")
		return
	}
	detail := body.Detail
	if len(detail) == 0 || string(detail) == "null" {
		detail = json.RawMessage(`{}`)
	}
	if !validJSONValue(detail) {
		recordSessionListDebugCapture("invalid_value", reason)
		writeError(w, http.StatusBadRequest, "detail must be valid JSON")
		return
	}
	if s.pgPool == nil {
		recordSessionListDebugCapture("not_configured", reason)
		writeError(w, http.StatusServiceUnavailable, "session-list debug capture store not configured")
		return
	}

	owner := strings.ToLower(strings.TrimSpace(user.OwnerEmail()))
	serverRowsJSON, err := s.sessionListDebugServerRowsJSON(r, owner)
	if err != nil {
		recordSessionListDebugCapture("store_error", reason)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	captureID := "sldc_" + auth.RandomHex(12)
	row := sessionListDebugCaptureRow{
		ID:           captureID,
		OwnerEmail:   owner,
		SessionScope: s.sessionScope,
		SessionID:    sessionID,
		Reason:       reason,
		Source:       clampDebugCaptureString(body.Source, 120),
		Location:     clampDebugCaptureString(body.Location, 240),
		ActiveID:     clampDebugCaptureString(body.ActiveID, 80),
		ClientSeq:    body.ClientSeq,
		Snapshot:     body.Snapshot,
		Detail:       detail,
		ServerRows:   serverRowsJSON,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.insertSessionListDebugCapture(r, row); err != nil {
		recordSessionListDebugCapture("store_error", reason)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordSessionListDebugCapture("ok", reason)
	slog.Warn("browser session-list debug capture",
		"capture_id", row.ID,
		"owner", row.OwnerEmail,
		"scope", row.SessionScope,
		"session_id", row.SessionID,
		"reason", row.Reason,
		"source", row.Source,
		"location", row.Location,
		"active_id", row.ActiveID,
		"client_seq", row.ClientSeq,
	)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"capture_id": captureID,
		"accepted":   true,
	})
}

func (s *appServer) handleDebugSessionListCaptures(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordDebugSessionListCaptureRead("forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		owner = user.OwnerEmail()
	}
	owner = strings.ToLower(owner)
	captureID := strings.TrimSpace(r.URL.Query().Get("capture_id"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	limit := 20
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			recordDebugSessionListCaptureRead("bad_request")
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > sessionListDebugCaptureMaxLimit {
		limit = sessionListDebugCaptureMaxLimit
	}
	if s.pgPool == nil {
		recordDebugSessionListCaptureRead("not_configured")
		writeError(w, http.StatusServiceUnavailable, "session-list debug capture store not configured")
		return
	}

	rows, err := s.listSessionListDebugCaptures(r, owner, captureID, sessionID, limit)
	if err != nil {
		recordDebugSessionListCaptureRead("store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if captureID != "" && len(rows) == 0 {
		recordDebugSessionListCaptureRead("empty")
		writeError(w, http.StatusNotFound, "session-list debug capture not found")
		return
	}
	result := "ok"
	if len(rows) == 0 {
		result = "empty"
	}
	recordDebugSessionListCaptureRead(result)
	writeJSON(w, http.StatusOK, map[string]any{
		"description": debugSessionListCapturesDescription,
		"owner":       owner,
		"scope":       s.sessionScope,
		"captures":    rows,
		"count":       len(rows),
		"fetched_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *appServer) sessionListDebugServerRowsJSON(r *http.Request, owner string) (json.RawMessage, error) {
	rows, err := fetchSessionRowsAfter(r.Context(), s.pgPool, owner, s.sessionScope, 0, 1000)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(sliceMap(rows, debugRowJSON))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func (s *appServer) insertSessionListDebugCapture(r *http.Request, row sessionListDebugCaptureRow) error {
	const q = `
		INSERT INTO session_list_debug_captures (
			id, owner_email, session_scope, session_id, reason, source,
			location, active_id, client_seq, snapshot, detail, server_rows, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	_, err := s.pgPool.Exec(
		r.Context(),
		q,
		row.ID,
		row.OwnerEmail,
		row.SessionScope,
		row.SessionID,
		row.Reason,
		row.Source,
		row.Location,
		row.ActiveID,
		row.ClientSeq,
		[]byte(row.Snapshot),
		[]byte(row.Detail),
		[]byte(row.ServerRows),
		row.CreatedAt,
	)
	if err != nil {
		return err
	}
	const cleanup = `
		DELETE FROM session_list_debug_captures
		WHERE id IN (
			SELECT id
			FROM session_list_debug_captures
			WHERE owner_email = $1
			  AND session_scope = $2
			ORDER BY created_at DESC
			OFFSET $3
		)
	`
	_, err = s.pgPool.Exec(r.Context(), cleanup, row.OwnerEmail, row.SessionScope, sessionListDebugCaptureRetention)
	return err
}

func (s *appServer) listSessionListDebugCaptures(r *http.Request, owner, captureID, sessionID string, limit int) ([]sessionListDebugCaptureRow, error) {
	const q = `
		SELECT id, owner_email, session_scope, session_id, reason, source,
			location, active_id, client_seq, snapshot, detail, server_rows, created_at
		FROM session_list_debug_captures
		WHERE owner_email = $1
		  AND session_scope = $2
		  AND ($3 = '' OR id = $3)
		  AND ($4 = '' OR session_id = $4)
		ORDER BY created_at DESC
		LIMIT $5
	`
	rows, err := s.pgPool.Query(r.Context(), q, owner, s.sessionScope, captureID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessionListDebugCaptureRow{}
	for rows.Next() {
		row, err := scanSessionListDebugCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type sessionListDebugCaptureScanner interface {
	Scan(dest ...any) error
}

func scanSessionListDebugCapture(scanner sessionListDebugCaptureScanner) (sessionListDebugCaptureRow, error) {
	var row sessionListDebugCaptureRow
	var snapshot []byte
	var detail []byte
	var serverRows []byte
	if err := scanner.Scan(
		&row.ID,
		&row.OwnerEmail,
		&row.SessionScope,
		&row.SessionID,
		&row.Reason,
		&row.Source,
		&row.Location,
		&row.ActiveID,
		&row.ClientSeq,
		&snapshot,
		&detail,
		&serverRows,
		&row.CreatedAt,
	); err != nil {
		return sessionListDebugCaptureRow{}, err
	}
	row.Snapshot = json.RawMessage(snapshot)
	row.Detail = json.RawMessage(detail)
	row.ServerRows = json.RawMessage(serverRows)
	return row, nil
}

func validJSONObject(value json.RawMessage) bool {
	var obj map[string]any
	return len(value) > 0 && json.Unmarshal(value, &obj) == nil && obj != nil
}

func validJSONValue(value json.RawMessage) bool {
	var anyValue any
	return len(value) > 0 && json.Unmarshal(value, &anyValue) == nil
}

func clampDebugCaptureString(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func clampSessionListDebugCaptureReason(value string) string {
	switch strings.TrimSpace(value) {
	case "created-session-name-mutated",
		"created-session-agent-avatar-mutated",
		"created-session-system-avatar-mutated",
		"created-session-rendered-avatar-changed":
		return strings.TrimSpace(value)
	default:
		return "other"
	}
}
