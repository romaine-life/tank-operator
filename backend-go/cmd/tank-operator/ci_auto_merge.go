package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
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
	readyWatch, err := s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchReady, detail)
	if err == nil {
		watch = readyWatch
	} else {
		slog.Warn("mark CI watch ready failed", "watch_id", watch.WatchID, "error", err)
	}
	s.emitCIStatusRecord(ctx, watch, "ready", "", detail)
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
