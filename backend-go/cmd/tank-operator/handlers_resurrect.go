package main

import (
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// handleResurrectSession creates a NEW session that resumes a dead session's
// conversation. Pod death stays terminal for the source session; this is a new
// lifecycle, not a revival — the new pod re-clones the same repos and its
// claude-runner fetches the source's captured transcript and `resume`s it.
// Resurrection is Claude-only (only the claude-runner has the resume path);
// Codex parity is a separate follow-up. See docs/session-transcript-capture.md.
func (s *appServer) handleResurrectSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("session_id"))
	if sourceID == "" {
		recordSessionResurrect("bad_request")
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	owner := user.OwnerEmail()
	if s.mgr == nil {
		recordSessionResurrect("unavailable")
		writeError(w, http.StatusServiceUnavailable, "session manager unavailable")
		return
	}
	src, _, err := s.mgr.GetRegisteredByOwnerAnyVisibility(r.Context(), owner, sourceID)
	if err != nil {
		recordSessionResurrect("not_found")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if provider, ok := sdkProviderForMode(src.Mode); !ok || provider != "claude" {
		recordSessionResurrect("unsupported_mode")
		writeError(w, http.StatusBadRequest, "resurrection is only supported for Claude GUI sessions")
		return
	}
	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner:                    owner,
		Mode:                     src.Mode,
		Repos:                    src.Repos,
		Model:                    src.Model,
		Effort:                   src.Effort,
		Capabilities:             src.Capabilities,
		Name:                     resurrectedName(src.Name),
		ResurrectSourceSessionID: sourceID,
	})
	if err != nil {
		recordSessionResurrect("create_failed")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordSessionResurrect("ok")
	writeJSON(w, http.StatusOK, map[string]any{"session_id": info.ID})
}

func resurrectedName(name string) *string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "Resurrected session"
	} else {
		base += " (resumed)"
	}
	return &base
}
