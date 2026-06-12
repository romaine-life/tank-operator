package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/kubeexec"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const (
	// launchDispatchInterval is how often the reconciler scans for dispatchable
	// launches. Shorter than the stranded-launch sweep because this is the
	// happy path — a user is waiting for their launch turn to start once the
	// pod is ready.
	launchDispatchInterval = 3 * time.Second
	// launchDispatchBatchLimit caps launches claimed per tick.
	launchDispatchBatchLimit = 20
	// launchDispatchClaimStaleAfter is how long a claimed-but-undispatched
	// launch waits before another replica may re-claim it (covers a reconciler
	// that crashed mid-dispatch).
	launchDispatchClaimStaleAfter = 2 * time.Minute
	// maxLaunchDispatchAttempts bounds futile retries. A transient failure
	// (pod write hiccup, NATS blip) is retried; once attempts reach the cap the
	// launch is failed durably so it stops being reclaimed and the user sees a
	// terminal instead of an endless retry.
	maxLaunchDispatchAttempts = 5
	// launchAttachmentsWorkspaceDir is the workspace-relative directory the
	// reconciler materializes staged launch attachments into. Deterministic per
	// (turn, ordinal) so a retry overwrites the same path idempotently.
	launchAttachmentsWorkspaceDir = ".attachments"
	// launchStaleDeadline is how old a still-non-terminal launch must be before
	// the sweep fails it. A healthy launch dispatches within seconds of the pod
	// going Active, so a launch that is still awaiting_bytes / ready / claiming
	// this long after create is stuck: the browser never finished staging the
	// bytes, the session died before going Active, or dispatch retries were
	// exhausted. Generous so it never races a healthy in-flight launch.
	launchStaleDeadline = 20 * time.Minute
	// launchAlreadyTerminalReason prefixes the durable failure recorded on a
	// pending-launch row whose turn already carries a terminal event in the
	// ledger. The dispatch is skipped — publishing would append turn.submitted
	// AFTER the terminal, the runner's already-terminal guard would drop the
	// command, and the ledger would end on turn.submitted: a session durably
	// 'submitted' forever plus a false stuck-turn alert (#1079 item 3).
	launchAlreadyTerminalReason = "terminal_already_present"
)

// runLaunchDispatchLoop drives backend-owned dispatch of durable attachment
// launches (#865). The browser registers the launch and durably stages its
// attachment bytes (Postgres); this loop, once the pod is Active, materializes
// the bytes into the workspace, composes the runnable prompt with the written
// paths, and publishes submit_turn through the normal SDK boundary — then drops
// the staged bytes. Because the launch and its bytes are durable, the turn is
// delivered even if the browser tab is long gone, which is the whole point of
// moving phase two off the client.
func runLaunchDispatchLoop(ctx context.Context, app *appServer, interval time.Duration) error {
	if app == nil || app.pendingLaunch == nil {
		return nil
	}
	if interval <= 0 {
		interval = launchDispatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		if err := app.processPendingLaunches(ctx, now); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("launch dispatch scan failed", "error", err)
		}
		if err := app.processStaleLaunches(ctx, now); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("stale launch scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// processStaleLaunches fails launches that are still non-terminal long past the
// point a healthy one would have dispatched — the durable-model counterpart to
// the stranded-launch ledger sweep. Where the reconciler delivers a ready
// launch, this gives a terminal to one that can never become ready (bytes never
// finished staging) or can never be claimed (its session is gone), so the row
// self-cleans and the SPA stops showing it as pending.
func (s *appServer) processStaleLaunches(ctx context.Context, now time.Time) error {
	if s == nil || s.pendingLaunch == nil {
		return nil
	}
	rows, err := s.pendingLaunch.FindStale(ctx, now.Add(-launchStaleDeadline), launchDispatchBatchLimit)
	if err != nil {
		return err
	}
	for _, launch := range rows {
		reason := fmt.Sprintf(
			"launch_never_completed: stuck in %s for >%s (attachment bytes never finished staging, or the session went away before dispatch)",
			launch.Status, launchStaleDeadline,
		)
		if err := s.failPendingLaunch(ctx, launch, now, reason); err != nil {
			slog.Warn("stale launch fail-mark failed",
				"session_id", launch.SessionID, "turn_id", launch.TurnID, "error", err)
		}
	}
	return nil
}

func (s *appServer) processPendingLaunches(ctx context.Context, now time.Time) error {
	if s == nil || s.pendingLaunch == nil {
		return nil
	}
	rows, err := s.pendingLaunch.ClaimReady(ctx, now, launchDispatchBatchLimit, launchDispatchClaimStaleAfter)
	if err != nil {
		return err
	}
	for _, launch := range rows {
		permanent, derr := s.dispatchPendingLaunch(ctx, launch, now)
		if derr == nil {
			continue
		}
		if permanent || launch.AttemptCount >= maxLaunchDispatchAttempts {
			reason := fmt.Sprintf("launch_dispatch_failed (attempt %d): %s", launch.AttemptCount, derr.Error())
			if ferr := s.failPendingLaunch(ctx, launch, now, reason); ferr != nil {
				slog.Warn("launch dispatch terminal-fail mark failed",
					"session_id", launch.SessionID, "turn_id", launch.TurnID, "error", ferr)
			}
			continue
		}
		// Transient: leave the lease to expire and reclaim on a later tick.
		slog.Warn("launch dispatch transient failure; will retry",
			"session_id", launch.SessionID, "turn_id", launch.TurnID,
			"attempt", launch.AttemptCount, "error", derr)
		recordLaunchDispatch("retry")
	}
	return nil
}

// dispatchPendingLaunch materializes one launch's staged bytes into the pod and
// publishes its submit_turn. Returns (permanent, err): permanent=true means the
// failure won't fix itself on retry (the SDK boundary rejected the turn), so
// the caller should fail it now rather than retry to the attempt cap.
func (s *appServer) dispatchPendingLaunch(ctx context.Context, launch pgstore.PendingLaunchTurn, now time.Time) (bool, error) {
	// Already-terminal guard (#1079 item 3). The launch turn id is durable and
	// deterministic, so ANY prior writer can have terminaled it before this
	// claim dispatches: the stranded-launch sweep's pre-guard 15-minute race,
	// a sibling replica's stale-launch scan, a prior attempt whose publish
	// landed but whose MarkDispatched write was lost and whose turn already
	// finished. Dispatching after a terminal appends turn.submitted AFTER the
	// terminal — the runner drops the command via its own already-terminal
	// check, the ledger ends on turn.submitted, and the session reads as
	// durably 'submitted' forever. Check before any boundary write, pod
	// write, or publish; on a hit, fail the pending row directly (NOT via
	// failPendingLaunch — the turn already has its terminal, and writing a
	// second turn.command_failed onto it is exactly the false-terminal class
	// this guard exists to prevent) and skip the dispatch. This also keeps
	// every downstream failure path of this attempt from double-terminaling
	// the turn.
	terminal, err := s.sessionEvents.FindTurnTerminal(ctx, launch.SessionID, launch.TurnID)
	if err != nil {
		// Transient read failure: leave the claim lease to expire and retry.
		return false, fmt.Errorf("check turn terminal: %w", err)
	}
	if terminal != nil {
		terminalType := stringMapField(terminal, "type")
		reason := fmt.Sprintf(
			"%s: launch turn already carries a durable %s; dispatch skipped so turn.submitted is never written after a terminal",
			launchAlreadyTerminalReason, terminalType,
		)
		if err := s.pendingLaunch.MarkFailed(ctx, launch.TankSessionID, launch.TurnID, reason); err != nil {
			recordLaunchDispatch("fail_mark_error")
			return false, fmt.Errorf("mark already-terminal launch failed: %w", err)
		}
		slog.Info("launch dispatch skipped: turn already terminal",
			"session_id", launch.SessionID,
			"turn_id", launch.TurnID,
			"terminal_type", terminalType)
		recordLaunchDispatch("skipped_already_terminal")
		return false, nil
	}

	info, err := s.mgr.GetByOwner(ctx, launch.OwnerEmail, launch.SessionID)
	if err != nil {
		return false, fmt.Errorf("resolve session: %w", err)
	}
	if info.PodName == nil {
		return false, errors.New("session pod not ready")
	}
	podName := *info.PodName

	blobs, err := s.pendingLaunch.LoadAttachments(ctx, launch.TankSessionID, launch.TurnID)
	if err != nil {
		return false, fmt.Errorf("load attachments: %w", err)
	}

	absPaths := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		absPath, perr := launchAttachmentAbsPath(launch.TurnID, blob)
		if perr != nil {
			// A path we can't safely build won't become buildable on retry.
			return true, fmt.Errorf("attachment %d path: %w", blob.Ordinal, perr)
		}
		if err := kubeexec.WriteFile(ctx, s.k8s, s.restCfg, s.namespace, podName, absPath, blob.Bytes); err != nil {
			return false, fmt.Errorf("materialize attachment %d: %w", blob.Ordinal, err)
		}
		absPaths = append(absPaths, absPath)
	}

	runtime := strings.TrimSpace(launch.Runtime)
	if strings.TrimSpace(launch.SkillName) != "" && !isLaunchSkillRuntime(runtime) {
		return true, fmt.Errorf("skill launch runtime is invalid")
	}
	prompt := composeLaunchDispatchPrompt(runtime, launch.SkillName, launch.BasePrompt, absPaths)
	resp, status, detail := s.enqueueSDKTurn(ctx, launch.OwnerEmail, launch.SessionID, sdkTurnRequest{
		ClientNonce:                launch.ClientNonce,
		RequireNonce:               true,
		Prompt:                     prompt,
		Model:                      launch.Model,
		Effort:                     launch.Effort,
		SkillName:                  launch.SkillName,
		OmitUserMessage:            true, // the launch user_message.created already exists
		RequireExistingUserMessage: true,
		Source:                     "launch-dispatch",
		SessionMode:                info.Mode,
		AllowBeforeReady:           true, // claim already gated on session status = Active
		CreatedAt:                  now,
	})
	if status != 0 {
		// 4xx is a permanent rejection (bad prompt/skill, missing launch user
		// message, unsupported mode); 5xx is transient (NATS/persistence blip).
		permanent := status >= 400 && status < 500
		return permanent, fmt.Errorf("enqueue turn (%d): %s", status, strings.TrimSpace(detail))
	}

	dispatchedTurnID := turnIDFromEnqueueResponse(resp)
	if err := s.pendingLaunch.MarkDispatched(ctx, launch.TankSessionID, launch.TurnID, dispatchedTurnID); err != nil {
		// The turn is already published; a failed mark just gets reclaimed and
		// re-published (idempotent at the deterministic turn id / runner
		// already-terminal guard), same as the scheduled-wakeup MarkFired path.
		return false, fmt.Errorf("mark dispatched: %w", err)
	}
	recordLaunchDispatch("dispatched")
	return false, nil
}

// failPendingLaunch marks the launch failed and emits a durable
// turn.command_failed so the SPA renders it as failed (not perpetually
// pending). Mirrors the stranded-launch sweep's terminal.
func (s *appServer) failPendingLaunch(ctx context.Context, launch pgstore.PendingLaunchTurn, now time.Time, reason string) error {
	if err := s.pendingLaunch.MarkFailed(ctx, launch.TankSessionID, launch.TurnID, reason); err != nil {
		recordLaunchDispatch("fail_mark_error")
		return err
	}
	runtime := strings.TrimSpace(launch.Runtime)
	if runtime == "" {
		runtime = "claude"
	}
	failed := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
		SessionID:         launch.SessionID,
		SessionStorageKey: launch.TankSessionID,
		Email:             launch.OwnerEmail,
		TurnID:            launch.TurnID,
		ClientNonce:       launch.ClientNonce,
		Runtime:           runtime,
		Reason:            reason,
		Now:               now,
	})
	if err := s.persistBackendEvent(ctx, launch.TankSessionID, failed); err != nil {
		recordLaunchDispatch("fail_event_error")
		return err
	}
	recordLaunchDispatch("failed")
	return nil
}

// composeLaunchDispatchPrompt builds the runnable prompt the agent receives:
// the user's base prompt, the materialized attachment paths under an
// "Attachments:" list (matching the protocol doc's prompt shape), and the skill
// trigger prefix when the launch invoked a skill. The result satisfies
// promptMatchesSkillTrigger so enqueueSDKTurn accepts it.
func composeLaunchDispatchPrompt(runtime, skillName, basePrompt string, absPaths []string) string {
	body := strings.TrimSpace(basePrompt)
	if len(absPaths) > 0 {
		var b strings.Builder
		b.WriteString(body)
		if body != "" {
			b.WriteString("\n\n")
		}
		b.WriteString("Attachments:")
		for _, p := range absPaths {
			b.WriteString("\n- ")
			b.WriteString(p)
		}
		body = b.String()
	}
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return body
	}
	trigger := skillPromptTrigger(strings.TrimSpace(runtime), skillName)
	if promptMatchesSkillTrigger(strings.TrimSpace(runtime), skillName, body) {
		return body
	}
	if body == "" {
		return trigger
	}
	return trigger + "\n\n" + body
}

func isLaunchSkillRuntime(runtime string) bool {
	switch strings.TrimSpace(runtime) {
	case "claude", "codex", "antigravity":
		return true
	default:
		return false
	}
}

// launchAttachmentAbsPath builds the deterministic, workspace-safe absolute
// path one staged attachment is materialized to: .attachments/<turn>-<ordinal>-
// <sanitized-name>. Deterministic so a retry overwrites the same file.
func launchAttachmentAbsPath(turnID string, blob pgstore.LaunchAttachmentBlob) (string, error) {
	safeTurn := attachmentNameSanitizer.ReplaceAllString(strings.TrimSpace(turnID), "_")
	name := strings.TrimSpace(blob.Name)
	if name == "" {
		name = "file"
	}
	safeName := attachmentNameSanitizer.ReplaceAllString(name, "_")
	if len(safeName) > 100 {
		safeName = safeName[:100]
	}
	rel := fmt.Sprintf("%s/%s-%d-%s", launchAttachmentsWorkspaceDir, safeTurn, blob.Ordinal, safeName)
	return safeWorkspacePath(rel)
}
