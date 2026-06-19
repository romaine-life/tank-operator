package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// orchestrationSpokeMode is the session shape a phase spoke runs in. A phase is
// agent work — a brief, a workspace cloned off the repo's default branch, an
// agent that opens a governed PR — so it uses the Claude GUI mode, the same
// shape spawn_run_session produces for an agent-driven run session.
const orchestrationSpokeMode = sessionmodel.ClaudeGUIMode

// orchestrationPhaseTurnSource tags the first turn injected into a phase spoke,
// distinguishing it from human/browser turns in the durable ledger.
const orchestrationPhaseTurnSource = "orchestration-phase"

// spawnPhaseSpoke creates the spoke session that works a phase and injects the
// phase's stored brief as its first turn. It reuses the backend's existing
// session-create + first-turn-injection machinery — the same mgr.Create +
// enqueueSDKTurn path spawn_run_session and POST /api/internal/sessions/{id}/turns
// use — cloned off the run's repo (its default branch, i.e. main) via the repos
// selection. Returns the new session id, which the caller records with
// AttachPhaseSpoke.
//
// This is the production phaseSpokeSpawnFunc bound into the engine in main.go;
// the engine's DAG/idempotency logic is exercised in tests with a fake spawner.
func (s *appServer) spawnPhaseSpoke(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, error) {
	if s.mgr == nil {
		return "", errors.New("session manager unavailable")
	}
	brief := strings.TrimSpace(phase.Brief)
	if brief == "" {
		return "", fmt.Errorf("phase %s has an empty brief", phase.PhaseID)
	}
	owner := strings.TrimSpace(orch.OwnerEmail)
	if owner == "" {
		return "", fmt.Errorf("orchestration %s has no owner_email", orch.OrchestrationID)
	}
	repos := []string{orch.RepoOwner + "/" + orch.RepoName}
	repoBases := repoBasesForPhase(orch, phase)

	name := "Phase: " + phase.Key
	launchAt := time.Now().UTC()
	// Mirror handleInternalCreateSession: the row's requested_at trails the
	// launch turn by a hair so the initial user turn orders first.
	requestedAt := launchAt.Add(2 * time.Millisecond).Format(time.RFC3339Nano)

	info, err := s.mgr.Create(ctx, sessions.CreateOptions{
		Owner:        owner,
		Mode:         orchestrationSpokeMode,
		Repos:        repos,
		RepoBases:    repoBases,
		Capabilities: []string{sessionmodel.SessionCapabilityRestrictedGit},
		Name:         &name,
		RequestedAt:  requestedAt,
	})
	if err != nil {
		return "", fmt.Errorf("create spoke session: %w", err)
	}

	_, status, detail := s.enqueueSDKTurn(ctx, owner, info.ID, sdkTurnRequest{
		Prompt:           brief,
		DisplayText:      brief,
		Source:           orchestrationPhaseTurnSource,
		SessionMode:      info.Mode,
		FollowUp:         false,
		AllowBeforeReady: true,
		CreatedAt:        launchAt,
		OrderBase:        launchAt,
		AuthorKind:       string(conversation.AuthorKindSystem),
	})
	if status != 0 {
		// The session row + pod exist but the first turn never landed. Tear the
		// session down so the phase is re-dispatched cleanly instead of leaving a
		// promptless ghost spoke that would never open a PR.
		s.rollbackCreatedSession(ctx, owner, info.ID, "orchestration phase initial turn", detail)
		return "", fmt.Errorf("submit phase brief turn: %s", detail)
	}
	return info.ID, nil
}

func repoBasesForPhase(orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) map[string]string {
	if phase.Target != pgstore.PhaseTargetIntegration || strings.TrimSpace(orch.IntegrationBranch) == "" {
		return map[string]string{}
	}
	return map[string]string{
		orch.RepoOwner + "/" + orch.RepoName: strings.TrimSpace(orch.IntegrationBranch),
	}
}
