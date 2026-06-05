package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

type bugLabelResponse struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	DisplayName  string     `json:"display_name"`
	SessionCount int        `json:"session_count"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

func (s *appServer) handleListBugLabels(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.pgPool == nil {
		writeError(w, http.StatusServiceUnavailable, "postgres is not configured")
		return
	}
	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	labels, err := fetchBugLabels(r.Context(), s, user.OwnerEmail(), scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

func (s *appServer) handleSetSessionBugLabel(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if scope != s.localSessionScope() {
		writeError(w, http.StatusForbidden, "session scope not writable from this orchestrator")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	var body struct {
		Name  *string   `json:"name"`
		Names *[]string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var (
		info sessions.Info
		err  error
	)
	if body.Names != nil {
		info, err = s.mgr.SetBugLabels(r.Context(), user.OwnerEmail(), sessionID, *body.Names)
	} else {
		info, err = s.mgr.SetBugLabel(r.Context(), user.OwnerEmail(), sessionID, body.Name)
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, info)
	case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
		writeError(w, http.StatusNotFound, "session not found")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func fetchBugLabels(ctx context.Context, s *appServer, owner, scope string) ([]bugLabelResponse, error) {
	const q = `
		SELECT bug_labels.id,
		       bug_labels.name,
		       bug_labels.slug,
		       COUNT(session_bug_labels.session_id)::int,
		       MAX(session_bug_labels.attached_at)
		FROM bug_labels
		LEFT JOIN session_bug_labels
			ON session_bug_labels.bug_label_id = bug_labels.id
		WHERE bug_labels.owner_email = $1
		  AND bug_labels.session_scope = $2
		  AND bug_labels.archived_at IS NULL
		GROUP BY bug_labels.id, bug_labels.name, bug_labels.slug
		ORDER BY MAX(session_bug_labels.attached_at) DESC NULLS LAST, lower(bug_labels.name) ASC
		LIMIT 200
	`
	rows, err := s.pgPool.Query(ctx, q, strings.ToLower(strings.TrimSpace(owner)), normalizeSessionScope(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bugLabelResponse{}
	for rows.Next() {
		var label bugLabelResponse
		if err := rows.Scan(&label.ID, &label.Name, &label.Slug, &label.SessionCount, &label.LastUsedAt); err != nil {
			return nil, err
		}
		label.DisplayName = "bug: " + strings.TrimSpace(label.Name)
		out = append(out, label)
	}
	return out, rows.Err()
}
