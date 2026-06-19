package main

import (
	"log/slog"
	"net/http"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// handleMergeSessionPR is the human-gated, in-Tank merge of a session's governed
// PR (docs/event-driven-rollout.md). The merge is the human's, not the agent's:
// it marks the draft ready, merges via mcp-github on-behalf-of the caller — and
// GitHub itself enforces the green/mergeable gate (an unmergeable PR is rejected)
// — then terminals the CI watch and emits a ci_status.updated "merged" record.
func (s *appServer) handleMergeSessionPR(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.ciWatches == nil || s.mcpGitHub == nil {
		writeError(w, http.StatusServiceUnavailable, "merge is unavailable")
		return
	}
	sessionID := r.PathValue("session_id")
	ctx := r.Context()
	watch, err := s.ciWatches.GetLatestForSession(ctx, s.sessionScope, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no PR watch for this session")
		return
	}
	if watch.PROwner == "" || watch.PRName == "" || watch.PRNumber <= 0 {
		writeError(w, http.StatusConflict, "watch has no PR coordinates")
		return
	}
	if watch.Status == pgstore.CIWatchMerged {
		writeJSON(w, http.StatusOK, map[string]any{"merged": true, "merge_commit": watch.MergeCommit})
		return
	}

	// Session PRs start as drafts, which GitHub refuses to merge; mark ready
	// first. mark_pull_request_ready_for_review is idempotent (an already-ready
	// PR is not an error), so a failure here is a real failure that the merge
	// cannot recover from -- surface it instead of marching into a confusing
	// "still a draft" merge rejection (the failure this path used to swallow).
	// Both calls pass the owning session id so mcp-github keys the governed-merge
	// control-action audit to this session's ledger, not the orchestrator's.
	if err := s.mcpGitHub.MarkPRReady(ctx, user.Email, watch.PROwner, watch.PRName, watch.PRNumber, watch.SessionID); err != nil {
		recordCITerminal("merge_rejected")
		writeError(w, http.StatusConflict, "merge failed: could not mark PR ready for review: "+err.Error())
		return
	}
	mergeCommit, err := s.mcpGitHub.MergePR(ctx, user.Email, watch.PROwner, watch.PRName, watch.PRNumber, "squash", watch.SessionID)
	if err != nil {
		recordCITerminal("merge_rejected")
		writeError(w, http.StatusConflict, "merge failed: "+err.Error())
		return
	}

	if _, err := s.ciWatches.MarkMerged(ctx, watch.WatchID, mergeCommit); err != nil {
		slog.Warn("mark watch merged failed", "session", sessionID, "error", err)
	}
	s.emitCIStatusRecord(ctx, watch, "merged", mergeCommit, "Merged from Tank by "+user.Email)
	recordCITerminal("merged")
	writeJSON(w, http.StatusOK, map[string]any{"merged": true, "merge_commit": mergeCommit})
}
