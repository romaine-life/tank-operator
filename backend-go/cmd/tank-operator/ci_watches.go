package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// handleInternalRegisterCIWatch registers (or refreshes) a GitHub PR
// CI/mergeability watch for the session. The Tank watch_current_session_pr tool
// calls this at agent hand-off, after it has performed the authoritative GitHub
// read (resolving the async mergeable_state). The durable row drives the
// webhook receiver, the idle reaper, and the human merge surface. See
// docs/event-driven-rollout.md.
func (s *appServer) handleInternalRegisterCIWatch(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/ci-watches")
	if user == nil {
		return
	}
	if s.ciWatches == nil {
		writeError(w, http.StatusServiceUnavailable, "ci watch store unavailable")
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		PROwner        string `json:"pr_owner"`
		PRName         string `json:"pr_name"`
		PRNumber       int    `json:"pr_number"`
		HeadSHA        string `json:"head_sha"`
		MergeableState string `json:"mergeable_state"`
		CheckState     string `json:"check_state"`
		Detail         string `json:"detail"`
		PRURL          string `json:"pr_url"`
		Status         string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	watch, err := s.ciWatches.Register(r.Context(), pgstore.RegisterCIWatchRequest{
		SessionID:      sessionID,
		OwnerEmail:     user.ActorEmail,
		PROwner:        body.PROwner,
		PRName:         body.PRName,
		PRNumber:       body.PRNumber,
		HeadSHA:        body.HeadSHA,
		MergeableState: body.MergeableState,
		CheckState:     body.CheckState,
		Detail:         body.Detail,
		PRURL:          body.PRURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Phase->PR linking: if this session is an orchestration phase's spoke, copy
	// the PR coordinates it just registered onto the phase, so the PR->phase
	// reverse lookup resolves when the PR merges. This is the cleanest existing
	// "this session registered a PR" hook — the spoke registers via the same
	// watch_current_session_pr handoff every governed PR uses. A no-op for
	// ordinary (non-orchestration) sessions.
	if s.orchestrations != nil {
		s.orchestrations.linkPhasePR(r.Context(), sessionID, pgstore.SetPhasePRRequest{
			PROwner:  watch.PROwner,
			PRName:   watch.PRName,
			PRNumber: watch.PRNumber,
			PRURL:    watch.PRURL,
		})
	}
	if ciWatchRegistrationReady(body.Status, body.CheckState, body.MergeableState) {
		s.handleGreenCIWatch(r.Context(), watch, body.Detail)
	}
	writeJSON(w, http.StatusOK, watch)
}

func ciWatchRegistrationReady(status, checkState, mergeableState string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "ready" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(checkState), "success") &&
		strings.EqualFold(strings.TrimSpace(mergeableState), "clean")
}
