package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type ciWatchReconcileSource string

const (
	ciWatchReconcileHandoff          ciWatchReconcileSource = "handoff"
	ciWatchReconcileWebhook          ciWatchReconcileSource = "webhook"
	ciWatchReconcileMergeabilityPoll ciWatchReconcileSource = "mergeability_poll"
	ciWatchReconcileBackstop         ciWatchReconcileSource = "backstop"
)

type ciWatchReconcileResult struct {
	Status              pgstore.CIWatchStatus
	HeadSHA             string
	MergeableState      string
	CheckState          string
	Detail              string
	PRURL               string
	MergeCommit         string
	FailingChecks       []string
	MergeabilityUnknown bool
}

var defaultCIWatchMergeabilityRetryDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
	60 * time.Second,
	60 * time.Second,
	60 * time.Second,
	60 * time.Second,
}

func (s *appServer) reconcileAndApplyCIWatch(ctx context.Context, watch pgstore.CIWatch, source ciWatchReconcileSource) (ciWatchReconcileResult, error) {
	return s.reconcileAndApplyCIWatchAttempt(ctx, watch, source, 0)
}

func (s *appServer) reconcileAndApplyCIWatchAttempt(ctx context.Context, watch pgstore.CIWatch, source ciWatchReconcileSource, retryAttempt int) (ciWatchReconcileResult, error) {
	if s.mcpGitHub == nil {
		return ciWatchReconcileResult{}, errCIWatchReconcileUnavailable("mcp-github client not configured")
	}
	if s.ciWatches == nil {
		return ciWatchReconcileResult{}, errCIWatchReconcileUnavailable("ci watch store unavailable")
	}
	state, err := s.mcpGitHub.ResolvePullRequestState(ctx, watch.OwnerEmail, watch.PROwner, watch.PRName, watch.PRNumber)
	if err != nil {
		return ciWatchReconcileResult{}, err
	}
	return s.applyResolvedCIWatchState(ctx, watch, state, source, retryAttempt)
}

func (s *appServer) applyResolvedCIWatchState(ctx context.Context, watch pgstore.CIWatch, state mcpgithub.PullRequestState, source ciWatchReconcileSource, retryAttempt int) (ciWatchReconcileResult, error) {
	if s.ciWatches == nil {
		return ciWatchReconcileResult{}, errCIWatchReconcileUnavailable("ci watch store unavailable")
	}
	result := classifyCIWatchState(watch, state)

	// Head-pin: the watch is pinned to the head it was registered on. Only a
	// governed publish by the same owner may move it (a legit re-publish that
	// raced its re-registration); any other live head is an out-of-band push. We
	// never silently follow it or wake the agent -- we supersede the watch and
	// emit a user-facing (not agent-facing) alert. See
	// docs/features/ci-watch/redesign-from-1295-review.md.
	if headMovedOffPin(watch, state) && !s.headMoveIsGoverned(ctx, watch, state.HeadSHA) {
		detail := watch.PROwner + "/" + watch.PRName + " #" + strconv.Itoa(watch.PRNumber) +
			" head moved to " + shortSHAForMessage(state.HeadSHA) +
			" outside Tank's governed publish path; not following. Investigate the unexpected push to this branch."
		superseded, err := s.ciWatches.UpdateObservation(ctx, pgstore.UpdateCIWatchObservationRequest{
			WatchID:         watch.WatchID,
			ExpectedHeadSHA: watch.HeadSHA,
			Status:          pgstore.CIWatchSuperseded,
			HeadSHA:         watch.HeadSHA,
			MergeableState:  watch.MergeableState,
			CheckState:      watch.CheckState,
			Detail:          detail,
			PRURL:           watch.PRURL,
		})
		if errors.Is(err, pgstore.ErrCIWatchObservationStale) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		recordCITerminal("out_of_band")
		s.emitCIStatusRecord(ctx, superseded, "out_of_band", "", detail)
		return result, nil
	}

	updated, err := s.ciWatches.UpdateObservation(ctx, pgstore.UpdateCIWatchObservationRequest{
		WatchID:         watch.WatchID,
		ExpectedHeadSHA: watch.HeadSHA,
		Status:          result.Status,
		HeadSHA:         result.HeadSHA,
		MergeableState:  result.MergeableState,
		CheckState:      result.CheckState,
		Detail:          result.Detail,
		PRURL:           result.PRURL,
	})
	if errors.Is(err, pgstore.ErrCIWatchObservationStale) {
		// A concurrent reconcile already moved this watch out of the 'watching'
		// state we read (or a re-publish re-headed it). That winner owns the
		// side effects; re-firing them would double-wake or resurrect a terminal
		// row, so we stop here without surfacing an error.
		return result, nil
	}
	if err != nil {
		return result, err
	}

	// Age-to-terminal observability. Only the winning transition reaches here
	// (losers returned on the stale sentinel above), so each terminal is counted
	// once.
	if result.Status != pgstore.CIWatchWatching && !updated.RegisteredAt.IsZero() {
		recordCIWatchAge(time.Since(updated.RegisteredAt).Seconds())
	}

	switch result.Status {
	case pgstore.CIWatchReady:
		recordCITerminal("green")
		s.cancelCIWatchMergeabilityRetry(updated)
		s.handleGreenCIWatch(ctx, updated, result.Detail)
	case pgstore.CIWatchFailed:
		recordCITerminal("red")
		s.cancelCIWatchMergeabilityRetry(updated)
		if source != ciWatchReconcileHandoff {
			s.wakeSessionForCI(ctx, updated, "ci-failure", ciWebhookSignal{kind: "red", detail: result.Detail})
		}
	case pgstore.CIWatchConflict:
		recordCITerminal("conflict")
		s.cancelCIWatchMergeabilityRetry(updated)
		if source != ciWatchReconcileHandoff {
			s.wakeSessionForCI(ctx, updated, "ci-conflict", ciWebhookSignal{kind: "conflict", detail: result.Detail})
		}
	case pgstore.CIWatchMerged:
		s.cancelCIWatchMergeabilityRetry(updated)
		_, _ = s.ciWatches.MarkMerged(ctx, updated.WatchID, result.MergeCommit)
		s.emitCIStatusRecord(ctx, updated, "merged", result.MergeCommit, result.Detail)
	case pgstore.CIWatchWatching:
		if result.MergeabilityUnknown {
			s.scheduleCIWatchMergeabilityRetry(updated, retryAttempt)
		} else {
			s.cancelCIWatchMergeabilityRetry(updated)
		}
	}
	return result, nil
}

func classifyCIWatchState(watch pgstore.CIWatch, state mcpgithub.PullRequestState) ciWatchReconcileResult {
	headSHA := strings.TrimSpace(state.HeadSHA)
	if headSHA == "" {
		headSHA = strings.TrimSpace(watch.HeadSHA)
	}
	prURL := strings.TrimSpace(state.HTMLURL)
	if prURL == "" {
		prURL = strings.TrimSpace(watch.PRURL)
	}
	mergeableState := strings.ToLower(strings.TrimSpace(state.MergeableState))
	checkState := strings.ToLower(strings.TrimSpace(state.CheckState))
	if checkState == "" {
		checkState = "pending"
	}
	result := ciWatchReconcileResult{
		Status:              pgstore.CIWatchWatching,
		HeadSHA:             headSHA,
		MergeableState:      mergeableState,
		CheckState:          checkState,
		PRURL:               prURL,
		MergeabilityUnknown: state.MergeabilityUnknown(),
	}
	repoPR := watch.PROwner + "/" + watch.PRName + " #" + strconv.Itoa(watch.PRNumber)
	if state.PR.Merged {
		result.Status = pgstore.CIWatchMerged
		result.MergeCommit = strings.TrimSpace(state.PR.MergeCommitSHA)
		result.Detail = "PR was merged."
		return result
	}
	if mergeableState == "dirty" || mergeableState == "behind" || (state.Mergeable != nil && !*state.Mergeable && mergeableState != "blocked") {
		result.Status = pgstore.CIWatchConflict
		if mergeableState == "" {
			mergeableState = "mergeable=false"
		}
		result.Detail = repoPR + " needs a rebase onto its base (mergeable_state=" + mergeableState + ")."
		return result
	}
	// Wake on failure only once the checks have settled (nothing still running),
	// so the agent gets the full red set in one wake instead of one wake per
	// failing check as they conclude. A red that appears while others are still
	// in-flight stays 'watching' until the rest settle. (Q1.)
	if state.AllChecksSettled && len(state.FailingChecks) > 0 {
		result.Status = pgstore.CIWatchFailed
		result.FailingChecks = append([]string(nil), state.FailingChecks...)
		result.Detail = "CI failed on " + repoPR + ": " + strings.Join(firstStringsMain(state.FailingChecks, 8), ", ") + "."
		return result
	}
	if state.Mergeable != nil && *state.Mergeable && mergeableState == "clean" && checkState == "success" {
		result.Status = pgstore.CIWatchReady
		result.Detail = repoPR + " is green and mergeable, awaiting human merge in Tank."
		return result
	}
	result.Detail = "CI in progress (mergeable_state=" + firstNonEmptyString(mergeableState, "unknown") + ", checks=" + checkState
	if strings.TrimSpace(state.CIError) != "" {
		result.Detail += ": " + strings.TrimSpace(state.CIError)
	}
	result.Detail += ")."
	return result
}

func (s *appServer) scheduleCIWatchMergeabilityRetry(watch pgstore.CIWatch, attempt int) {
	delays := s.ciWatchMergeabilityRetryDelays
	if delays == nil {
		delays = defaultCIWatchMergeabilityRetryDelays
	}
	if attempt < 0 || attempt >= len(delays) {
		return
	}
	key := ciWatchMergeabilityRetryKey(watch)
	if key == "" {
		return
	}
	s.ciWatchMergeabilityRetryMu.Lock()
	if s.ciWatchMergeabilityRetries == nil {
		s.ciWatchMergeabilityRetries = map[string]*time.Timer{}
	}
	if _, exists := s.ciWatchMergeabilityRetries[key]; exists {
		s.ciWatchMergeabilityRetryMu.Unlock()
		return
	}
	delay := delays[attempt]
	timer := time.AfterFunc(delay, func() {
		s.ciWatchMergeabilityRetryMu.Lock()
		delete(s.ciWatchMergeabilityRetries, key)
		s.ciWatchMergeabilityRetryMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		current, err := s.ciWatches.Get(ctx, watch.WatchID)
		if err != nil {
			return
		}
		if current.Status != pgstore.CIWatchWatching || !strings.EqualFold(strings.TrimSpace(current.HeadSHA), strings.TrimSpace(watch.HeadSHA)) {
			return
		}
		if _, err := s.reconcileAndApplyCIWatchAttempt(ctx, current, ciWatchReconcileMergeabilityPoll, attempt+1); err != nil {
			slog.Warn("ci watch mergeability retry reconcile failed", "watch_id", current.WatchID, "attempt", attempt+1, "error", err)
		}
	})
	s.ciWatchMergeabilityRetries[key] = timer
	s.ciWatchMergeabilityRetryMu.Unlock()
}

func (s *appServer) cancelCIWatchMergeabilityRetry(watch pgstore.CIWatch) {
	key := ciWatchMergeabilityRetryKey(watch)
	if key == "" {
		return
	}
	s.ciWatchMergeabilityRetryMu.Lock()
	if timer := s.ciWatchMergeabilityRetries[key]; timer != nil {
		timer.Stop()
		delete(s.ciWatchMergeabilityRetries, key)
	}
	s.ciWatchMergeabilityRetryMu.Unlock()
}

func ciWatchMergeabilityRetryKey(watch pgstore.CIWatch) string {
	if strings.TrimSpace(watch.WatchID) == "" || strings.TrimSpace(watch.HeadSHA) == "" {
		return ""
	}
	return strings.TrimSpace(watch.WatchID) + ":" + strings.ToLower(strings.TrimSpace(watch.HeadSHA))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstStringsMain(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

type ciWatchReconcileUnavailable string

func (e ciWatchReconcileUnavailable) Error() string { return string(e) }

func errCIWatchReconcileUnavailable(message string) error {
	return ciWatchReconcileUnavailable(message)
}

func headMovedOffPin(watch pgstore.CIWatch, state mcpgithub.PullRequestState) bool {
	pinned := strings.TrimSpace(watch.HeadSHA)
	live := strings.TrimSpace(state.HeadSHA)
	return pinned != "" && live != "" && !strings.EqualFold(pinned, live)
}

// headMoveIsGoverned reports whether newHead was produced by a governed publish
// (control-action ledger) for the same owner -- a legit re-publish that may move
// the pin. It fails open (true) when the ledger cannot be consulted, so a
// degraded ledger never raises a false out-of-band alarm.
func (s *appServer) headMoveIsGoverned(ctx context.Context, watch pgstore.CIWatch, newHead string) bool {
	newHead = strings.TrimSpace(newHead)
	if newHead == "" || s.controlActions == nil {
		return true
	}
	rows, err := s.controlActions.ListBySession(ctx, watch.OwnerEmail, watch.SessionScope, watch.SessionID, 200)
	if err != nil {
		return true
	}
	for _, row := range rows {
		if row.Action != "github.commit.push" && row.Action != "github.break_glass.push" {
			continue
		}
		if row.Status == "succeeded" && strings.EqualFold(strings.TrimSpace(row.ResultSHA), newHead) {
			return true
		}
	}
	return false
}
