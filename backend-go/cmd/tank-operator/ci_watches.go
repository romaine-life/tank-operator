package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// handleInternalRegisterCIWatch is the older route for registering a GitHub PR
// readiness watch. Keep it as a compatibility facade over the Tank-owned
// /pr-readiness entry point so old callers use the same backend reconcile path.
func (s *appServer) handleInternalRegisterCIWatch(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/ci-watches")
	if user == nil {
		return
	}
	if s.ciWatches == nil {
		writeError(w, http.StatusServiceUnavailable, "ci watch store unavailable")
		return
	}
	var body prReadinessRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req, err := prReadinessRegistrationFromBody(r.PathValue("session_id"), user.ActorEmail, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	registered, err := s.registerAndReconcilePRReadiness(r.Context(), req, ciWatchReconcileHandoff)
	if err != nil {
		writeError(w, http.StatusBadGateway, "reconcile PR readiness: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, prReadinessResponseBody(registered.Watch, registered.Result))
}

func ciWatchRegistrationReady(status, checkState, mergeableState string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "ready" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(checkState), "success") &&
		strings.EqualFold(strings.TrimSpace(mergeableState), "clean")
}

func ciWatchToolState(status pgstore.CIWatchStatus) string {
	switch status {
	case pgstore.CIWatchReady:
		return "ready"
	case pgstore.CIWatchFailed:
		return "failed"
	case pgstore.CIWatchConflict:
		return "conflict"
	case pgstore.CIWatchMerged:
		return "merged"
	default:
		return "watching"
	}
}
