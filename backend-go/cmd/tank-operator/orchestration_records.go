package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

var (
	errOrchestrationMergeUnavailable = errors.New("orchestration merge surface unavailable")
	errOrchestrationOwnerMissing     = errors.New("orchestration has no owner email")
)

// emitOrchestrationRecord is the engine's orchestrationNotifyFunc: it surfaces a
// run's autonomous transitions to the human as a display-only ci_status.updated
// record — the same no-agent-invoked record kind the rollout work uses for green
// — on the relevant spoke session's timeline, where the owner looks for the
// run's outcome. The record persist publishes the session-event wake, so an
// owner watching that session sees the gate/escalation live; nothing invokes an
// agent. A phase with no spoke session (a run-level event before any phase ran)
// leaves only the durable state move + log, never a panic.
func (s *appServer) emitOrchestrationRecord(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase, kind orchestrationEventKind, detail string) {
	sessionID := strings.TrimSpace(phase.SpokeSessionID)
	if sessionID == "" {
		slog.Info("orchestration record: no spoke session to attach to",
			"orchestration_id", orch.OrchestrationID, "kind", string(kind))
		return
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	repo := strings.TrimSpace(orch.RepoOwner + "/" + orch.RepoName)
	event := conversation.CIStatusUpdatedEventMap(conversation.CIStatusUpdatedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             orch.OwnerEmail,
		Repo:              repo,
		PRNumber:          phase.PRNumber,
		PRURL:             phase.PRURL,
		HeadSHA:           phase.MergeSHA,
		State:             "orchestration_" + string(kind),
		Detail:            detail,
		Now:               time.Now().UTC(),
	})
	if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
		slog.Warn("orchestration record persist failed",
			"orchestration_id", orch.OrchestrationID, "session", sessionID, "kind", string(kind), "error", err)
	}
}
