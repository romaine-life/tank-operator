// Admin-only debug surface for the sidebar session list. Returns the
// server's view (every registry row including visible=false, the
// current row-update cursor, recent rows) so an operator or the AI
// support agent can diagnose sidebar bugs without browser devtools —
// the user constraint that drove the redesign's observability shape
// per memory/feedback_no_devtools_build_surfaces_instead.md.
//
// Auth: Tank admin power required (cross-user reads). Cardinality bounded
// by the requested owner; no scan.
package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func (s *appServer) handleDebugSessionListState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		owner = user.Email
	}
	owner = strings.ToLower(owner)

	if s.pgPool == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"owner":  owner,
			"scope":  s.sessionScope,
			"rows":   []any{},
			"cursor": "0",
			"note":   "Postgres pool not wired; stub-mode response",
		})
		return
	}

	// Fetch every row for this (owner, scope), including
	// visible=false. The full set is what the diagnostic needs to
	// distinguish "registry says deleted" from "wire dropped it" from
	// "SPA tombstoned it locally."
	rows, err := fetchSessionRowsAfter(r.Context(), s.pgPool, owner, s.sessionScope, 0, 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cursor, err := queryRowVersionTip(r.Context(), s.pgPool, owner, s.sessionScope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := map[string]any{
		"owner":      owner,
		"scope":      s.sessionScope,
		"cursor":     cursor,
		"row_count":  len(rows),
		"rows":       sliceMap(rows, debugRowJSON),
		"fetched_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	writeJSON(w, http.StatusOK, out)
}

// debugRowJSON is a compact, recovery-relevant projection. Drops the
// activity_summary and test/rollout blobs to keep the response
// scannable, but keeps the durable create-time run configuration so
// an operator can recreate a soft-deleted session without direct
// database credentials.
func debugRowJSON(record sessionmodel.SessionRecord) map[string]any {
	return map[string]any{
		"id":                    record.ID,
		"mode":                  record.Mode,
		"pod_name":              record.PodName,
		"name":                  record.Name,
		"visible":               record.Visible,
		"status":                record.Status,
		"requested_at":          record.RequestedAt,
		"created_at":            record.CreatedAt,
		"updated_at":            record.UpdatedAt,
		"ready_at":              record.ReadyAt,
		"terminating_at":        record.TerminatingAt,
		"repos":                 nonNilStrings(record.Repos),
		"capabilities":          nonNilStrings(record.Capabilities),
		"model":                 record.Model,
		"effort":                record.Effort,
		"runtime_model":         record.RuntimeModel,
		"runtime_effort":        record.RuntimeEffort,
		"runtime_configured_at": record.RuntimeConfiguredAt,
		"agent_avatar_id":       record.AgentAvatarID,
		"system_avatar_id":      record.SystemAvatarID,
		"sidebar_position":      record.SidebarPosition,
		"row_version":           record.RowVersion,
		"has_activity":          len(record.ActivitySummary) > 0,
		"has_clone_state":       record.CloneState != nil,
		"has_test_state":        record.TestState != nil,
		"has_rollout":           record.RolloutState != nil,
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func sliceMap[T, R any](in []T, fn func(T) R) []R {
	out := make([]R, 0, len(in))
	for _, item := range in {
		out = append(out, fn(item))
	}
	return out
}
