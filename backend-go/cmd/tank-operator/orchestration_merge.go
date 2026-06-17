package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// Production implementations of the orchestration engine's GitHub-effecting
// hooks. The engine owns the DAG decisions; these own the mcp-github calls
// on-behalf-of the run owner. All are bound into the engine in main.go and
// faked in tests, so the engine's logic stays Postgres- and GitHub-free under
// test.

// mergePhasePR is the engine's phaseMergeFunc: the autonomous, green-gated merge
// of a phase's PR. The merge gate is delegated to GitHub — mcp-github's
// merge_pull_request refuses unless required checks are green and the PR is
// mergeable, so a not-yet-green phase is simply not merged (merged=false) and
// retried on the next CI event or reconcile pass. The head SHA CI went green on
// comes from the durable session_ci_watches row and is passed as the
// expected_head_sha guard, so a push that lands after the green signal can never
// be merged unverified. Red/conflict watches are left for the existing agent-
// wake paths (the spoke fixes its own code); this hook only lands green work.
func (s *appServer) mergePhasePR(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, bool, error) {
	if s.mcpGitHub == nil {
		return "", false, nil
	}
	if phase.PROwner == "" || phase.PRName == "" || phase.PRNumber <= 0 {
		return "", false, nil
	}
	owner := strings.TrimSpace(orch.OwnerEmail)
	if owner == "" {
		return "", false, nil
	}

	// Resolve the verified-green head SHA from the durable watch, and skip a
	// PR the existing CI-watch path has already flagged red/conflicted — that
	// is the spoke agent's to fix, not ours to merge.
	var headSHA string
	var watch pgstore.CIWatch
	haveWatch := false
	if s.ciWatches != nil {
		if w, err := s.ciWatches.GetByPR(ctx, phase.PROwner, phase.PRName, phase.PRNumber); err == nil {
			switch w.Status {
			case pgstore.CIWatchFailed, pgstore.CIWatchConflict:
				return "", false, nil
			}
			headSHA = strings.TrimSpace(w.HeadSHA)
			watch = w
			haveWatch = true
		}
	}

	// Governed phase PRs open as drafts; GitHub refuses to merge a draft, so
	// mark ready first (idempotent — tolerate "already ready").
	if err := s.mcpGitHub.MarkPRReady(ctx, owner, phase.PROwner, phase.PRName, phase.PRNumber); err != nil {
		slog.Debug("orchestration merge: mark ready (continuing)",
			"phase_id", phase.PhaseID, "error", err)
	}
	mergeSHA, err := s.mcpGitHub.MergePR(ctx, owner, phase.PROwner, phase.PRName, phase.PRNumber, "squash", headSHA)
	if err != nil {
		// The expected, frequent case: GitHub refused because the PR is not yet
		// green/mergeable (or the head moved off the green SHA). Not an error
		// worth escalating — the next CI event or reconcile pass retries.
		slog.Debug("orchestration merge: not merged yet",
			"phase_id", phase.PhaseID, "pr", phase.PRName, "detail", err.Error())
		return "", false, nil
	}

	// The phase's PR merged. Terminal its CI watch + emit the same display-only
	// merged record the human-merge surface emits, so the spoke session's
	// timeline and the idle reaper reflect the merge.
	if haveWatch && s.ciWatches != nil && watch.Status != pgstore.CIWatchMerged {
		if _, merr := s.ciWatches.MarkMerged(ctx, watch.WatchID, mergeSHA); merr != nil {
			slog.Warn("orchestration merge: mark watch merged failed",
				"watch_id", watch.WatchID, "error", merr)
		}
		s.emitCIStatusRecord(ctx, watch, "merged", mergeSHA, "Auto-merged by Tank orchestration on verified green")
	}
	return mergeSHA, true, nil
}

// syncIntegrationForward is the engine's branchSyncFunc: it merges the repo's
// default branch forward into the run's integration branch so integration-target
// phases build on the latest landed main work. Implemented as a non-draft
// PR (default-branch -> integration) that is immediately merged; an already-in-
// sync branch yields GitHub's "No commits between" which CreatePR returns as a
// clean no-op (NoDiff). Best-effort: a conflict or transient failure is returned
// to the engine, which logs + retries next pass without blocking the DAG.
func (s *appServer) syncIntegrationForward(ctx context.Context, orch pgstore.Orchestration) error {
	if s.mcpGitHub == nil || strings.TrimSpace(orch.IntegrationBranch) == "" {
		return nil
	}
	owner := strings.TrimSpace(orch.OwnerEmail)
	if owner == "" {
		return nil
	}
	base, err := s.mcpGitHub.DefaultBranch(ctx, owner, orch.RepoOwner, orch.RepoName)
	if err != nil {
		return err
	}
	if strings.EqualFold(base, orch.IntegrationBranch) {
		return nil
	}
	title := "Tank orchestration: merge " + base + " into " + orch.IntegrationBranch
	body := "Automated merge-forward by Tank so integration-target phases build on the latest " + base + "."
	pr, err := s.mcpGitHub.CreatePR(ctx, owner, orch.RepoOwner, orch.RepoName, title, base, orch.IntegrationBranch, body, false)
	if err != nil {
		return err
	}
	if pr.NoDiff || pr.Number <= 0 {
		return nil // integration already current with the default branch
	}
	// Plain merge (not squash) so the integration branch keeps a real merge of
	// main rather than a flattened duplicate of already-landed commits.
	if _, err := s.mcpGitHub.MergePR(ctx, owner, orch.RepoOwner, orch.RepoName, pr.Number, "merge", ""); err != nil {
		return err
	}
	return nil
}

// promoteIntegrationToMain merges the run's integration branch into the repo's
// default branch — the human's "go" at the terminal review gate. Idempotent: an
// already-promoted integration branch yields "No commits between" (NoDiff),
// treated as a successful (already-merged) promotion. Returns the merge commit
// SHA when a merge lands.
func (s *appServer) promoteIntegrationToMain(ctx context.Context, orch pgstore.Orchestration) (string, error) {
	if s.mcpGitHub == nil {
		return "", errOrchestrationMergeUnavailable
	}
	owner := strings.TrimSpace(orch.OwnerEmail)
	if owner == "" {
		return "", errOrchestrationOwnerMissing
	}
	base, err := s.mcpGitHub.DefaultBranch(ctx, owner, orch.RepoOwner, orch.RepoName)
	if err != nil {
		return "", err
	}
	title := "Tank orchestration: promote " + orch.IntegrationBranch + " to " + base
	body := "Human-approved promotion of the orchestration integration branch to " + base + "."
	pr, err := s.mcpGitHub.CreatePR(ctx, owner, orch.RepoOwner, orch.RepoName, title, orch.IntegrationBranch, base, body, false)
	if err != nil {
		return "", err
	}
	if pr.NoDiff || pr.Number <= 0 {
		return "", nil // already promoted
	}
	return s.mcpGitHub.MergePR(ctx, owner, orch.RepoOwner, orch.RepoName, pr.Number, "merge", "")
}
