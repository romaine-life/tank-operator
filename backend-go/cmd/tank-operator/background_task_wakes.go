package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
)

const (
	backgroundTaskWakeBatchLimit      = 25
	backgroundTaskWakeDefaultInterval = 5 * time.Second
	backgroundTaskWakeClaimStaleAfter = 2 * time.Minute
)

// handleInternalRegisterBackgroundTaskWake records that a Claude background
// (run_in_background) task reached a natural terminal while its session had no
// active turn. The base Bash tool promises "re-invokes you when it exits", but
// a task-lifecycle SDK frame never starts a turn, so without this the follow-up
// is silently stranded. The runner registers the terminal; the orchestrator's
// fire loop later submits a system turn through the same backend-owned boundary
// as a user turn (source=background-task), mirroring ScheduleWakeup.
func (s *appServer) handleInternalRegisterBackgroundTaskWake(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		recordBackgroundTaskWakeRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		recordBackgroundTaskWakeRegister("unknown", "forbidden")
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.backgroundTaskWakes == nil {
		recordBackgroundTaskWakeRegister("unknown", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "background task wake store unavailable")
		return
	}
	if s.mgr == nil {
		recordBackgroundTaskWakeRegister("unknown", "manager_unavailable")
		writeError(w, http.StatusServiceUnavailable, "session manager unavailable")
		return
	}

	var body struct {
		TaskID       string `json:"task_id"`
		Status       string `json:"status"`
		Description  string `json:"description"`
		Summary      string `json:"summary"`
		LastToolName string `json:"last_tool_name"`
		Error        string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		recordBackgroundTaskWakeRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	taskID := strings.TrimSpace(body.TaskID)
	if taskID == "" || !backgroundTaskIDPattern.MatchString(taskID) {
		recordBackgroundTaskWakeRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "task_id is required and must match background task id syntax")
		return
	}

	info, err := s.mgr.GetRegisteredByOwner(r.Context(), caller.Email, sessionID)
	if err != nil {
		recordBackgroundTaskWakeRegister("unknown", "not_found")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok || provider != "claude" {
		recordBackgroundTaskWakeRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "background task wakes are only supported for Claude SDK sessions")
		return
	}

	prompt := buildBackgroundTaskWakePrompt(taskID, body.Status, body.Description, body.Summary, body.LastToolName, body.Error)
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		recordBackgroundTaskWakeRegister(provider, "bad_request")
		writeError(w, http.StatusBadRequest, "prompt too large")
		return
	}

	row, err := s.backgroundTaskWakes.Register(r.Context(), pgstore.RegisterBackgroundTaskWakeRequest{
		SessionScope: s.sessionScope,
		SessionID:    sessionID,
		OwnerEmail:   caller.Email,
		Provider:     provider,
		TaskID:       taskID,
		TaskStatus:   strings.TrimSpace(body.Status),
		Prompt:       prompt,
		RegisteredAt: time.Now().UTC(),
	})
	if err != nil {
		recordBackgroundTaskWakeRegister(provider, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordBackgroundTaskWakeRegister(provider, "ok")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":       "scheduled",
		"wake_id":      row.WakeID,
		"client_nonce": row.ClientNonce,
		"due_at":       row.DueAt.Format(time.RFC3339Nano),
	})
}

// buildBackgroundTaskWakePrompt is the self-grounding wake turn prompt. The
// Claude SDK is not guaranteed to re-surface a completed background task's
// output on a fresh turn, so the prompt carries the task id and the
// human-meaningful terminal fields and points the agent at BashOutput/TaskOutput
// to pull the buffer itself.
func buildBackgroundTaskWakePrompt(taskID, status, description, summary, lastToolName, errText string) string {
	var b strings.Builder
	b.WriteString("A background task you started earlier has finished while this session was idle, so it could not notify you mid-turn.\n\n")
	b.WriteString("Task id: " + taskID + "\n")
	if v := strings.TrimSpace(status); v != "" {
		b.WriteString("Final status: " + clipWakeField(v, 64) + "\n")
	}
	if v := strings.TrimSpace(description); v != "" {
		b.WriteString("Description: " + clipWakeField(v, 500) + "\n")
	}
	if v := strings.TrimSpace(summary); v != "" {
		b.WriteString("Summary: " + clipWakeField(v, 800) + "\n")
	}
	if v := strings.TrimSpace(lastToolName); v != "" {
		b.WriteString("Last tool: " + clipWakeField(v, 64) + "\n")
	}
	if v := strings.TrimSpace(errText); v != "" {
		b.WriteString("Error: " + clipWakeField(v, 800) + "\n")
	}
	b.WriteString("\nReview the task's output (for example with BashOutput/TaskOutput for this task id), then continue the work that was waiting on it. If nothing remains to do, end the turn without taking action.")
	return b.String()
}

func clipWakeField(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 1 {
		return value[:max]
	}
	return value[:max-1] + "…"
}

func runBackgroundTaskWakeLoop(ctx context.Context, app *appServer, interval time.Duration) error {
	if app == nil || app.backgroundTaskWakes == nil {
		return nil
	}
	if interval <= 0 {
		interval = backgroundTaskWakeDefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.processBackgroundTaskWakes(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("background task wake scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) processBackgroundTaskWakes(ctx context.Context, now time.Time) error {
	if s == nil || s.backgroundTaskWakes == nil {
		return nil
	}
	if count, err := s.backgroundTaskWakes.DueCount(ctx, now); err == nil {
		setBackgroundTaskWakesDue(count)
	} else {
		slog.Warn("background task wake due count failed", "error", err)
	}
	rows, err := s.backgroundTaskWakes.ClaimDue(ctx, now, backgroundTaskWakeBatchLimit, backgroundTaskWakeClaimStaleAfter)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.fireBackgroundTaskWake(ctx, row, now); err != nil {
			slog.Warn("background task wake fire failed",
				"wake_id", row.WakeID,
				"session_id", row.SessionID,
				"task_id", row.TaskID,
				"provider", row.Provider,
				"error", err)
		}
	}
	return nil
}

func (s *appServer) fireBackgroundTaskWake(ctx context.Context, row pgstore.BackgroundTaskWake, now time.Time) error {
	provider := strings.TrimSpace(row.Provider)
	if row.SessionStatus == "" {
		return s.failBackgroundTaskWake(ctx, row, provider, "session_not_found")
	}
	if row.SessionStatus != "Active" || row.SessionTerminated {
		return s.failBackgroundTaskWake(ctx, row, provider, "session_not_active")
	}
	// Soft-defer when the session is waiting on an AskUserQuestion answer:
	// injecting a turn now would feed the SDK a non-answer and strand the
	// pending question card. Release the claim and retry on a later tick once
	// the user has answered and needs_input clears.
	if row.SessionNeedsInput {
		recordBackgroundTaskWakeFire(provider, "deferred_needs_input")
		if err := s.backgroundTaskWakes.Release(ctx, row.WakeID); err != nil {
			return err
		}
		return nil
	}
	resp, status, detail := s.enqueueSDKTurn(ctx, row.OwnerEmail, row.SessionID, sdkTurnRequest{
		ClientNonce:  row.ClientNonce,
		RequireNonce: true,
		Prompt:       row.Prompt,
		Source:       "background-task",
		CreatedAt:    now,
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
	if status != 0 {
		reason := fmt.Sprintf("enqueue_failed:%d:%s", status, strings.TrimSpace(detail))
		return s.failBackgroundTaskWake(ctx, row, provider, reason)
	}
	turnID := strings.TrimSpace(resp["turn_id"])
	if err := s.backgroundTaskWakes.MarkFired(ctx, row.WakeID, turnID); err != nil {
		recordBackgroundTaskWakeFire(provider, "store_error")
		return err
	}
	recordBackgroundTaskWakeFire(provider, "ok")
	return nil
}

func (s *appServer) failBackgroundTaskWake(ctx context.Context, row pgstore.BackgroundTaskWake, provider, reason string) error {
	if err := s.backgroundTaskWakes.MarkFailed(ctx, row.WakeID, reason); err != nil {
		recordBackgroundTaskWakeFire(provider, "store_error")
		return err
	}
	recordBackgroundTaskWakeFire(provider, backgroundTaskWakeFireFailureLabel(reason))
	s.resolveFailedWake(ctx, row.OwnerEmail, row.SessionID,
		conversation.TurnIDForClientNonce(row.ClientNonce), row.ClientNonce, provider,
		strings.HasPrefix(reason, "enqueue_failed"), sessionactivity.AwayErrorReasonBackgroundTaskWake)
	return errors.New(reason)
}

func backgroundTaskWakeFireFailureLabel(reason string) string {
	switch {
	case strings.HasPrefix(reason, "session_not_found"):
		return "session_not_found"
	case strings.HasPrefix(reason, "session_not_active"):
		return "session_not_active"
	case strings.HasPrefix(reason, "enqueue_failed"):
		return "enqueue_failed"
	default:
		return "failed"
	}
}
