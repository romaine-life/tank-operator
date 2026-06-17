package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const maxGitHubWebhookBytes = 2 << 20 // 2 MiB

// ciFailingConclusions are the GitHub check/workflow conclusions that mean a
// required check is red. Any of them on a watched PR's current head SHA wakes
// the agent. A spurious wake (a non-required/flaky check) is self-correcting:
// the woken agent re-runs watch_current_session_pr, sees the PR is fine, and
// resumes waiting.
var ciFailingConclusions = map[string]bool{
	"failure":         true,
	"timed_out":       true,
	"cancelled":       true,
	"action_required": true,
	"startup_failure": true,
	"stale":           true,
}

type githubPRRef struct {
	Number int `json:"number"`
}

// githubWebhookPayload captures only the fields the CI-watch receiver needs
// across check_suite / check_run / workflow_run / pull_request events.
type githubWebhookPayload struct {
	Action     string `json:"action"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
	PullRequest *struct {
		Number         int    `json:"number"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		MergeableState string `json:"mergeable_state"`
		HTMLURL        string `json:"html_url"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	CheckSuite *struct {
		Conclusion   string        `json:"conclusion"`
		HeadSHA      string        `json:"head_sha"`
		PullRequests []githubPRRef `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun *struct {
		Name         string        `json:"name"`
		Conclusion   string        `json:"conclusion"`
		HeadSHA      string        `json:"head_sha"`
		PullRequests []githubPRRef `json:"pull_requests"`
	} `json:"check_run"`
	WorkflowRun *struct {
		Name         string        `json:"name"`
		Conclusion   string        `json:"conclusion"`
		HeadSHA      string        `json:"head_sha"`
		PullRequests []githubPRRef `json:"pull_requests"`
	} `json:"workflow_run"`
}

// ciWebhookSignal is the interpreted, event-type-agnostic result.
type ciWebhookSignal struct {
	prNumber    int
	headSHA     string
	kind        string // "" (ignore) | "red" | "conflict" | "merged"
	mergeCommit string
	detail      string
}

func firstPRNumber(refs []githubPRRef) int {
	if len(refs) > 0 {
		return refs[0].Number
	}
	return 0
}

// interpretGitHubWebhook reduces a raw delivery to a single actionable signal.
func interpretGitHubWebhook(eventType string, p *githubWebhookPayload) ciWebhookSignal {
	var sig ciWebhookSignal
	switch eventType {
	case "pull_request":
		if p.PullRequest == nil {
			return sig
		}
		sig.prNumber = p.PullRequest.Number
		sig.headSHA = p.PullRequest.Head.SHA
		switch {
		case p.Action == "closed" && p.PullRequest.Merged:
			sig.kind = "merged"
			sig.mergeCommit = p.PullRequest.MergeCommitSHA
			sig.detail = "PR was merged."
		case p.PullRequest.MergeableState == "dirty" || p.PullRequest.MergeableState == "behind":
			sig.kind = "conflict"
			sig.detail = "mergeable_state=" + p.PullRequest.MergeableState
		}
	case "check_suite":
		if p.CheckSuite == nil || p.Action != "completed" {
			return sig
		}
		sig.headSHA = p.CheckSuite.HeadSHA
		sig.prNumber = firstPRNumber(p.CheckSuite.PullRequests)
		if ciFailingConclusions[p.CheckSuite.Conclusion] {
			sig.kind = "red"
			sig.detail = "a check suite concluded " + p.CheckSuite.Conclusion
		}
	case "check_run":
		if p.CheckRun == nil || p.Action != "completed" {
			return sig
		}
		sig.headSHA = p.CheckRun.HeadSHA
		sig.prNumber = firstPRNumber(p.CheckRun.PullRequests)
		if ciFailingConclusions[p.CheckRun.Conclusion] {
			sig.kind = "red"
			sig.detail = "check '" + p.CheckRun.Name + "' concluded " + p.CheckRun.Conclusion
		}
	case "workflow_run":
		if p.WorkflowRun == nil || p.Action != "completed" {
			return sig
		}
		sig.headSHA = p.WorkflowRun.HeadSHA
		sig.prNumber = firstPRNumber(p.WorkflowRun.PullRequests)
		if ciFailingConclusions[p.WorkflowRun.Conclusion] {
			sig.kind = "red"
			sig.detail = "workflow '" + p.WorkflowRun.Name + "' concluded " + p.WorkflowRun.Conclusion
		}
	}
	return sig
}

// greenishCICompletion reports whether a delivery is a non-failing CI
// completion (a check_suite/check_run/workflow_run that finished with a
// conclusion that is not red) and the PR number it carries. It is the trigger
// for the orchestration green→merge fast path: a passing check is the moment a
// phase PR might have just gone fully green. A failing conclusion is left to the
// existing red-wake path; the merge gate itself is GitHub's, so a premature
// "greenish" (other checks still pending) merely yields a refused merge.
func greenishCICompletion(eventType string, p *githubWebhookPayload) (int, bool) {
	switch eventType {
	case "check_suite":
		if p.CheckSuite == nil || p.Action != "completed" || ciFailingConclusions[p.CheckSuite.Conclusion] {
			return 0, false
		}
		return firstPRNumber(p.CheckSuite.PullRequests), true
	case "check_run":
		if p.CheckRun == nil || p.Action != "completed" || ciFailingConclusions[p.CheckRun.Conclusion] {
			return 0, false
		}
		return firstPRNumber(p.CheckRun.PullRequests), true
	case "workflow_run":
		if p.WorkflowRun == nil || p.Action != "completed" || ciFailingConclusions[p.WorkflowRun.Conclusion] {
			return 0, false
		}
		return firstPRNumber(p.WorkflowRun.PullRequests), true
	default:
		return 0, false
	}
}

func (s *appServer) verifyGitHubWebhookSignature(header string, body []byte) bool {
	secret := strings.TrimSpace(s.githubWebhookSecret)
	if secret == "" {
		return false // fail closed: an unconfigured secret rejects all deliveries
	}
	const prefix = "sha256="
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.TrimPrefix(header, prefix)))
}

// handleGitHubWebhook is the public inbound GitHub webhook for the CI-watch
// receiver. It authenticates by HMAC (no JWT), reduces the delivery to a
// signal, reverse-looks-up the owning session, guards against stale/duplicate
// deliveries, and wakes the agent on red/conflict or records an external merge.
// See docs/event-driven-rollout.md.
func (s *appServer) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	eventType := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGitHubWebhookBytes))
	if err != nil {
		recordCIWebhook(eventType, "read_error")
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if !s.verifyGitHubWebhookSignature(r.Header.Get("X-Hub-Signature-256"), body) {
		recordCIWebhook(eventType, "rejected_sig")
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}
	recordCIWebhook(eventType, "received")
	// ack ping + unconfigured-store so GitHub stops retrying.
	if eventType == "ping" || s.ciWatches == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	var p githubWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		recordCIWebhook(eventType, "parse_error")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	sig := interpretGitHubWebhook(eventType, &p)
	owner := strings.ToLower(strings.TrimSpace(p.Repository.Owner.Login))
	name := strings.ToLower(strings.TrimSpace(p.Repository.Name))
	ctx := r.Context()
	// Orchestration green→merge fast path: a non-failing CI completion for a
	// watched PR that maps to a still-open phase triggers an immediate green-
	// gated merge attempt, so a phase self-completes within seconds of going
	// green instead of waiting for the reconcile backstop. GitHub is the merge
	// gate (a not-yet-green PR is refused), so firing on every passing check is
	// safe and idempotent. This runs independently of the CI-watch coalescing
	// below — a green check carries sig.kind=="" and would otherwise be ignored.
	if s.orchestrations != nil && owner != "" && name != "" {
		if prNum, greenish := greenishCICompletion(eventType, &p); greenish && prNum > 0 {
			s.orchestrations.maybeAutoMergeOnCI(ctx, owner, name, prNum)
		}
	}
	if sig.kind == "" || owner == "" || name == "" || sig.prNumber <= 0 {
		recordCIWebhook(eventType, "ignored")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// Orchestration advance runs independently of the CI-watch subsystem below:
	// a merged phase PR must walk the DAG even when no ci_watch row exists, or
	// the row is already terminal. A Tank-governed merge marks the watch 'merged'
	// before GitHub's webhook arrives, so the not-watching coalescing guard would
	// otherwise drop the merge signal entirely. The engine is idempotent (it
	// guards on phase status), so a duplicate delivery advances the run once.
	if sig.kind == "merged" && s.orchestrations != nil {
		s.orchestrations.advanceOnMerge(ctx, owner, name, sig.prNumber, sig.mergeCommit)
	}
	watch, err := s.ciWatches.GetByPR(ctx, owner, name, sig.prNumber)
	if err != nil {
		recordCIWebhook(eventType, "no_watch")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// Only an actively-watching row is actionable. This also coalesces
	// duplicate/late deliveries: after the first transition the row is no longer
	// 'watching', so further events for the same PR no-op.
	if watch.Status != pgstore.CIWatchWatching {
		recordCIWebhook(eventType, "not_watching")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// Stale-SHA guard: a delivery for a superseded commit (the agent
	// re-published a new head) is ignored.
	if sig.headSHA != "" && watch.HeadSHA != "" && !strings.EqualFold(sig.headSHA, watch.HeadSHA) {
		recordCIWebhook(eventType, "stale_sha")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	recordCIWebhook(eventType, "acted")
	s.applyCIWebhookSignal(ctx, watch, sig)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *appServer) applyCIWebhookSignal(ctx context.Context, watch pgstore.CIWatch, sig ciWebhookSignal) {
	recordCITerminal(sig.kind)
	switch sig.kind {
	case "red":
		_, _ = s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchFailed, sig.detail)
		s.wakeSessionForCI(ctx, watch, "ci-failure", sig)
	case "conflict":
		_, _ = s.ciWatches.UpdateStatus(ctx, watch.WatchID, pgstore.CIWatchConflict, sig.detail)
		s.wakeSessionForCI(ctx, watch, "ci-conflict", sig)
	case "merged":
		_, _ = s.ciWatches.MarkMerged(ctx, watch.WatchID, sig.mergeCommit)
		s.emitCIStatusRecord(ctx, watch, "merged", sig.mergeCommit, sig.detail)
	}
}

func (s *appServer) wakeSessionForCI(ctx context.Context, watch pgstore.CIWatch, source string, sig ciWebhookSignal) {
	repoPR := watch.PROwner + "/" + watch.PRName + " #" + strconv.Itoa(watch.PRNumber)
	var prompt, verb string
	if source == "ci-conflict" {
		verb = "conflict"
		prompt = "Your governed PR " + repoPR + " has a merge conflict (" + sig.detail + "). " +
			"Rebase the session branch onto its base, resolve the conflict, re-publish with publish_current_head, " +
			"then call watch_current_session_pr again."
	} else {
		verb = "failed"
		prompt = "CI reported a failure on your governed PR " + repoPR + " (" + sig.detail + "). " +
			"Inspect the failing check's logs, fix the cause, commit, and re-publish with publish_current_head, " +
			"then call watch_current_session_pr again. If the failure is unrelated or flaky and the PR is actually " +
			"fine, just call watch_current_session_pr to re-verify and resume waiting."
	}
	_, status, detail := s.enqueueSDKTurn(ctx, watch.OwnerEmail, watch.SessionID, sdkTurnRequest{
		Prompt:      prompt,
		DisplayText: "CI " + verb + " on " + repoPR,
		Source:      source,
		CreatedAt:   time.Now().UTC(),
		AuthorKind:  string(conversation.AuthorKindSystem),
	})
	if status != 0 {
		recordCIWake(source, "enqueue_failed")
		slog.Warn("ci wake enqueue failed", "session", watch.SessionID, "source", source, "status", status, "detail", detail)
		return
	}
	recordCIWake(source, "ok")
}

func (s *appServer) emitCIStatusRecord(ctx context.Context, watch pgstore.CIWatch, state, mergeCommit, detail string) {
	storageKey := watch.TankSessionID
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(watch.SessionScope, watch.SessionID)
	}
	event := conversation.CIStatusUpdatedEventMap(conversation.CIStatusUpdatedArgs{
		SessionID:         watch.SessionID,
		SessionStorageKey: storageKey,
		Email:             watch.OwnerEmail,
		Repo:              watch.PROwner + "/" + watch.PRName,
		PRNumber:          watch.PRNumber,
		PRURL:             watch.PRURL,
		HeadSHA:           watch.HeadSHA,
		State:             state,
		MergeCommit:       mergeCommit,
		Detail:            detail,
		Now:               time.Now().UTC(),
	})
	if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
		slog.Warn("ci_status record persist failed", "session", watch.SessionID, "error", err)
	}
}
