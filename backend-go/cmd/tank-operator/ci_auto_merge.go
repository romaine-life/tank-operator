package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func (s *appServer) handleGreenCIWatch(ctx context.Context, watch pgstore.CIWatch, detail string) {
	if detail = strings.TrimSpace(detail); detail == "" {
		detail = "PR is green and mergeable."
	}
	handled, err := s.autoMergeOrchestrationPhasePR(ctx, watch, detail)
	if handled {
		if err != nil {
			failureDetail := "orchestration auto-merge failed: " + err.Error()
			_, _ = s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchFailed, failureDetail)
			s.emitCIStatusRecord(ctx, watch, "failed", "", failureDetail)
			s.wakeSessionForCI(ctx, watch, "ci-failure", ciWebhookSignal{kind: "red", detail: failureDetail})
		}
		return
	}
	// Capture the pre-transition status: the user is pinged only on the
	// watching -> ready EDGE. handleGreenCIWatch is re-entered by webhook +
	// reconcile on an already-ready watch; re-pinging on every re-entry would
	// re-summon the user for a PR they already saw go green.
	wasAlreadyReady := watch.Status == pgstore.CIWatchReady
	readyWatch, err := s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchReady, detail)
	if err == nil {
		watch = readyWatch
	} else {
		slog.Warn("mark CI watch ready failed", "watch_id", watch.WatchID, "error", err)
	}
	// Ping the USER (never the agent) that the governed PR is green and
	// mergeable. This REPLACES the prior emitCIStatusRecord(ctx, watch,
	// "ready", …): that ci_status.updated "ready" record had no inline
	// projection or reducer case, so it was durable-but-invisible (see
	// docs/features/ci-watch/capabilities.md "ci-status-record"). The
	// pr_ready.notified ping renders inline AND trips the needs_input sidebar
	// attention. The orchestration auto-merge path above keeps emitting
	// ci_status.updated "merged"/"failed" — those state values stay live.
	s.emitPRReadyPing(ctx, watch, detail, wasAlreadyReady)
}

// emitPRReadyPing posts the backend-side "your governed PR is green and
// mergeable" notice that summons the USER and never wakes the AGENT. It is a
// standalone system notice (no turn, no submit_turn command), so it cannot be
// processed by the runner and cannot be swept by the stranded-turn sweeps.
//
// Idempotent on the transition: a re-entry on an already-ready watch
// short-circuits before the durable write. The deterministic head-keyed
// event_id is the durable backstop — concurrent reconcile/webhook drivers that
// both observe the watching -> ready edge collapse to one row at the
// session_events_event_identity unique index.
func (s *appServer) emitPRReadyPing(ctx context.Context, watch pgstore.CIWatch, detail string, wasAlreadyReady bool) {
	if wasAlreadyReady {
		recordCIReadyPing("already_ready")
		return
	}
	storageKey := watch.TankSessionID
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(watch.SessionScope, watch.SessionID)
	}
	repoPR := watch.PROwner + "/" + watch.PRName + " #" + strconv.Itoa(watch.PRNumber)
	text := "✅ Your governed PR " + repoPR + " is green and mergeable — ready to merge."
	if prURL := strings.TrimSpace(watch.PRURL); prURL != "" {
		text += "\n" + prURL
	}
	event := conversation.PRReadyNotifiedEventMap(conversation.PRReadyNotifiedArgs{
		SessionID:         watch.SessionID,
		SessionStorageKey: storageKey,
		Email:             watch.OwnerEmail,
		Repo:              watch.PROwner + "/" + watch.PRName,
		PRNumber:          watch.PRNumber,
		PRURL:             watch.PRURL,
		HeadSHA:           watch.HeadSHA,
		Text:              text,
		Now:               time.Now().UTC(),
	})
	if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
		recordCIReadyPing("persist_failed")
		slog.Warn("pr_ready ping persist failed", "session", watch.SessionID, "error", err)
		return
	}
	recordCIReadyPing("emitted")
}

func (s *appServer) autoMergeOrchestrationPhasePR(ctx context.Context, watch pgstore.CIWatch, detail string) (bool, error) {
	if s == nil || s.orchestrations == nil || s.orchestrations.store == nil {
		return false, nil
	}
	phase, err := s.orchestrations.store.GetPhaseBySpokeSession(ctx, watch.SessionID)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			return false, nil
		}
		return false, err
	}
	if s.mcpGitHub == nil {
		return true, errors.New("mcp-github client not configured")
	}
	if phase.Status == pgstore.PhaseMerged {
		return true, nil
	}
	if strings.TrimSpace(phase.PROwner) == "" || strings.TrimSpace(phase.PRName) == "" || phase.PRNumber <= 0 {
		s.orchestrations.linkPhasePR(ctx, watch.SessionID, pgstore.SetPhasePRRequest{
			PROwner:  watch.PROwner,
			PRName:   watch.PRName,
			PRNumber: watch.PRNumber,
			PRURL:    watch.PRURL,
		})
		phase, err = s.orchestrations.store.GetPhaseBySpokeSession(ctx, watch.SessionID)
		if err != nil {
			return true, err
		}
	}
	if phase.Status == pgstore.PhaseBlocked {
		return true, errors.New("phase is blocked")
	}
	// Confirm-before-merge: orchestration auto-merge has no human in the loop, so
	// re-read live state and merge only if it is still a fully-settled green on
	// the exact head -- guarding the partial/transient 'clean' window where
	// GitHub reports clean before every check has registered. If it raced an
	// external merge, record that; if it is not a confirmed settled green yet,
	// un-latch to 'watching' so a later webhook or the durable backstop re-drives
	// it, rather than leaving it stuck Ready. (Q3.)
	confirm, err := s.mcpGitHub.ResolvePullRequestState(ctx, watch.OwnerEmail, watch.PROwner, watch.PRName, watch.PRNumber)
	if err != nil {
		return true, err
	}
	if confirm.PR.Merged {
		mergeCommit := strings.TrimSpace(confirm.PR.MergeCommitSHA)
		if mergedWatch, mErr := s.ciWatches.MarkMerged(ctx, watch.WatchID, mergeCommit); mErr == nil {
			watch = mergedWatch
		}
		s.emitCIStatusRecord(ctx, watch, "merged", mergeCommit, detail)
		recordCITerminal("merged")
		s.orchestrations.advanceOnMerge(ctx, watch.PROwner, watch.PRName, watch.PRNumber, mergeCommit)
		return true, nil
	}
	if !confirmedSettledGreen(confirm, watch.HeadSHA) {
		_, _ = s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchWatching,
			"auto-merge deferred: CI is not a confirmed settled green on the head yet (will retry).")
		return true, nil
	}
	if err := s.mcpGitHub.MarkPRReady(ctx, watch.OwnerEmail, watch.PROwner, watch.PRName, watch.PRNumber); err != nil {
		slog.Warn("orchestration auto-merge mark PR ready failed (continuing)", "watch_id", watch.WatchID, "error", err)
	}
	mergeCommit, err := s.mcpGitHub.MergePRWithHead(ctx, watch.OwnerEmail, watch.PROwner, watch.PRName, watch.PRNumber, "squash", watch.HeadSHA)
	if err != nil {
		return true, err
	}
	mergedWatch, err := s.ciWatches.MarkMerged(ctx, watch.WatchID, mergeCommit)
	if err == nil {
		watch = mergedWatch
	} else {
		slog.Warn("orchestration auto-merge mark watch merged failed", "watch_id", watch.WatchID, "error", err)
	}
	s.emitCIStatusRecord(ctx, watch, "merged", mergeCommit, detail)
	recordCITerminal("merged")
	s.orchestrations.advanceOnMerge(ctx, watch.PROwner, watch.PRName, watch.PRNumber, mergeCommit)
	return true, nil
}

// confirmedSettledGreen is the orchestration auto-merge gate: GitHub reports the
// PR mergeable and clean, Tank has observed every check settled with none
// failing, and the live head still matches the watch head. This is the
// human-less stand-in for "all CI ran and passed" -- a true cross-workflow
// aggregator "gate" check would be airtight, but this confirm-read closes the
// partial/transient-clean window without a CI change.
func confirmedSettledGreen(state mcpgithub.PullRequestState, head string) bool {
	return state.Mergeable != nil && *state.Mergeable &&
		strings.EqualFold(strings.TrimSpace(state.MergeableState), "clean") &&
		state.AllChecksSettled && len(state.FailingChecks) == 0 &&
		strings.EqualFold(strings.TrimSpace(state.HeadSHA), strings.TrimSpace(head))
}
