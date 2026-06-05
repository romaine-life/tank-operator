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
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

const (
	scheduledWakeupBatchLimit       = 25
	scheduledWakeupDefaultInterval  = 5 * time.Second
	scheduledWakeupClaimStaleAfter  = 2 * time.Minute
	scheduledWakeupMaxProviderIDLen = 256
)

func (s *appServer) handleInternalRegisterScheduledWakeup(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		recordScheduledWakeupRegister("unknown", "forbidden")
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.scheduledWakeups == nil {
		recordScheduledWakeupRegister("unknown", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "scheduled wakeup store unavailable")
		return
	}
	if s.mgr == nil {
		recordScheduledWakeupRegister("unknown", "manager_unavailable")
		writeError(w, http.StatusServiceUnavailable, "session manager unavailable")
		return
	}

	var body struct {
		DelayMs         int64  `json:"delay_ms"`
		Prompt          string `json:"prompt"`
		ProviderItemID  string `json:"provider_item_id"`
		ScheduledTurnID string `json:"scheduled_turn_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.DelayMs < 0 {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "delay_ms must be non-negative")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "missing prompt")
		return
	}
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "prompt too large")
		return
	}
	providerItemID := strings.TrimSpace(body.ProviderItemID)
	if providerItemID == "" || len([]byte(providerItemID)) > scheduledWakeupMaxProviderIDLen {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "provider_item_id is required")
		return
	}
	scheduledTurnID := strings.TrimSpace(body.ScheduledTurnID)
	if scheduledTurnID != "" && !turnIDPattern.MatchString(scheduledTurnID) {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "scheduled_turn_id is invalid")
		return
	}

	info, err := s.mgr.GetRegisteredByOwner(r.Context(), caller.Email, sessionID)
	if err != nil {
		if !errors.Is(err, sessions.ErrNotFound) {
			recordScheduledWakeupRegister("unknown", "lookup_error")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recordScheduledWakeupRegister("unknown", "not_found")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok || provider != "claude" {
		recordScheduledWakeupRegister("unknown", "bad_request")
		writeError(w, http.StatusBadRequest, "scheduled wakeups are only supported for Claude SDK sessions")
		return
	}
	now := time.Now().UTC()
	row, err := s.scheduledWakeups.Register(r.Context(), pgstore.RegisterScheduledWakeupRequest{
		SessionScope:    s.sessionScope,
		SessionID:       sessionID,
		OwnerEmail:      caller.Email,
		Provider:        provider,
		Prompt:          prompt,
		ScheduledTurnID: scheduledTurnID,
		ProviderItemID:  providerItemID,
		ScheduledAt:     now,
		DueAt:           now.Add(time.Duration(body.DelayMs) * time.Millisecond),
	})
	if err != nil {
		recordScheduledWakeupRegister(provider, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordScheduledWakeupRegister(provider, "ok")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":       "scheduled",
		"wakeup_id":    row.WakeupID,
		"client_nonce": row.ClientNonce,
		"due_at":       row.DueAt.Format(time.RFC3339Nano),
	})
}

func (s *appServer) handleListScheduledWakeups(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if s.scheduledWakeups == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduled wakeup store unavailable")
		return
	}
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if _, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
		writeError(w, status, err.Error())
		return
	}
	rows, err := s.scheduledWakeups.ListBySession(r.Context(), sessionScope, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"wakeup_id":         row.WakeupID,
			"status":            string(row.Status),
			"provider":          row.Provider,
			"prompt":            row.Prompt,
			"client_nonce":      row.ClientNonce,
			"scheduled_turn_id": row.ScheduledTurnID,
			"provider_item_id":  row.ProviderItemID,
			"scheduled_at":      row.ScheduledAt.Format(time.RFC3339Nano),
			"due_at":            row.DueAt.Format(time.RFC3339Nano),
			"attempt_count":     row.AttemptCount,
			"fired_turn_id":     row.FiredTurnID,
			"last_error":        row.LastError,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scheduled_wakeups": out})
}

// handleCancelScheduledWakeups cancels every pending self-scheduled wake for the
// session — the explicit "cancel the timer" control for a parked session. The
// prompt-mid-sleep take-over uses the same cancelPendingWakesForSession path
// from the submit handler. Cancelling marks the wakes 'cancelled' (non-pending,
// no error) and recomputes activity so the session leaves "scheduled" without
// ringing.
func (s *appServer) handleCancelScheduledWakeups(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	owner := user.OwnerEmail()
	if _, err := s.mgr.GetByOwner(r.Context(), owner, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	cancelled := s.cancelPendingWakesForSession(r.Context(), sessionID)
	if s.activityRefresher != nil {
		if err := s.activityRefresher.RefreshSessionActivity(r.Context(), owner, s.sessionScope, sessionID); err != nil {
			slog.Warn("cancel scheduled wakeups: activity refresh failed", "session_id", sessionID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": cancelled})
}

// cancelPendingWakesForSession cancels pending scheduled-wakeup and
// background-task wakes for a session and returns the total cancelled. Cancel
// failures are logged, not surfaced — a best-effort take-over must not fail the
// user's turn submission.
func (s *appServer) cancelPendingWakesForSession(ctx context.Context, sessionID string) int64 {
	var total int64
	if s.scheduledWakeups != nil {
		if n, err := s.scheduledWakeups.CancelPendingForSession(ctx, s.sessionScope, sessionID); err != nil {
			slog.Warn("cancel pending scheduled wakeups failed", "session_id", sessionID, "error", err)
		} else {
			total += n
		}
	}
	if s.backgroundTaskWakes != nil {
		if n, err := s.backgroundTaskWakes.CancelPendingForSession(ctx, s.sessionScope, sessionID); err != nil {
			slog.Warn("cancel pending background task wakes failed", "session_id", sessionID, "error", err)
		} else {
			total += n
		}
	}
	return total
}

func runScheduledWakeupLoop(ctx context.Context, app *appServer, interval time.Duration) error {
	if app == nil || app.scheduledWakeups == nil {
		return nil
	}
	if interval <= 0 {
		interval = scheduledWakeupDefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.processScheduledWakeups(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("scheduled wakeup scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) processScheduledWakeups(ctx context.Context, now time.Time) error {
	if s == nil || s.scheduledWakeups == nil {
		return nil
	}
	if count, err := s.scheduledWakeups.ScheduledDueCount(ctx, now); err == nil {
		setScheduledWakeupsDue(count)
	} else {
		slog.Warn("scheduled wakeup due count failed", "error", err)
	}
	rows, err := s.scheduledWakeups.ClaimDue(ctx, now, scheduledWakeupBatchLimit, scheduledWakeupClaimStaleAfter)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.fireScheduledWakeup(ctx, row, now); err != nil {
			slog.Warn("scheduled wakeup fire failed",
				"wakeup_id", row.WakeupID,
				"session_id", row.SessionID,
				"provider", row.Provider,
				"error", err)
		}
	}
	return nil
}

func (s *appServer) fireScheduledWakeup(ctx context.Context, row pgstore.ScheduledWakeup, now time.Time) error {
	provider := strings.TrimSpace(row.Provider)
	if row.SessionStatus == "" {
		return s.failScheduledWakeup(ctx, row, provider, "session_not_found")
	}
	if row.SessionStatus != "Active" || row.SessionTerminated {
		return s.failScheduledWakeup(ctx, row, provider, "session_not_active")
	}
	resp, status, detail := s.enqueueSDKTurn(ctx, row.OwnerEmail, row.SessionID, sdkTurnRequest{
		ClientNonce:  row.ClientNonce,
		RequireNonce: true,
		Prompt:       row.Prompt,
		Source:       "schedule-wakeup",
		CreatedAt:    now,
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
	if status != 0 {
		reason := fmt.Sprintf("enqueue_failed:%d:%s", status, strings.TrimSpace(detail))
		return s.failScheduledWakeup(ctx, row, provider, reason)
	}
	turnID := strings.TrimSpace(resp["turn_id"])
	if err := s.scheduledWakeups.MarkFired(ctx, row.WakeupID, turnID); err != nil {
		recordScheduledWakeupFire(provider, "store_error")
		return err
	}
	recordScheduledWakeupFire(provider, "ok")
	return nil
}

func (s *appServer) failScheduledWakeup(ctx context.Context, row pgstore.ScheduledWakeup, provider, reason string) error {
	if err := s.scheduledWakeups.MarkFailed(ctx, row.WakeupID, reason); err != nil {
		recordScheduledWakeupFire(provider, "store_error")
		return err
	}
	recordScheduledWakeupFire(provider, scheduledWakeupFireFailureLabel(reason))
	// Resolve the session out of the non-summoning "scheduled" status. A fire
	// attempt that bounced while the session was alive (enqueue_failed) is an
	// away-error: emit a durable, away-tagged terminal so the activity fold lands
	// error+away_error and the SPA rings the same summon a normal hand-off gets —
	// the agent's self-scheduled continuation broke while the user was away.
	// session_not_found / session_not_active mean the session is gone or dying,
	// so its own lifecycle owns visibility; just recompute so it leaves
	// "scheduled" without an away ring.
	s.resolveFailedWake(ctx, row.OwnerEmail, row.SessionID,
		conversation.TurnIDForClientNonce(row.ClientNonce), row.ClientNonce, provider,
		strings.HasPrefix(reason, "enqueue_failed"), sessionactivity.AwayErrorReasonScheduledWakeup)
	return errors.New(reason)
}

// resolveFailedWake takes a session out of the non-summoning "scheduled" status
// after a wake fire attempt failed. When ring is true (session alive, command
// bounced) it persists a durable, away-tagged turn.command_failed so the
// activity fold lands error+away_error and the SPA rings the turn-complete
// summon. It always recomputes the activity summary so the would-be
// "scheduled"/"ready" state resolves even when no terminal is emitted. Shared by
// the scheduled-wakeup and background-task-wake fire paths.
func (s *appServer) resolveFailedWake(ctx context.Context, owner, sessionID, turnID, clientNonce, runtime string, ring bool, awayReason string) {
	if ring {
		storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
		event := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             owner,
			TurnID:            turnID,
			ClientNonce:       clientNonce,
			Runtime:           runtime,
			Reason:            awayReason,
			Now:               time.Now().UTC(),
		})
		if err := s.persistBackendEvent(ctx, storageKey, event); err != nil {
			slog.Warn("failed wake away-error persist failed", "session_id", sessionID, "error", err)
		}
	}
	if s.activityRefresher != nil {
		if err := s.activityRefresher.RefreshSessionActivity(ctx, owner, s.sessionScope, sessionID); err != nil {
			slog.Warn("failed wake activity refresh failed", "session_id", sessionID, "error", err)
		}
	}
}

// isSelfResumeTurnSource reports whether a submitted turn is the orchestrator
// resuming the agent on its own (a ScheduleWakeup timer or a background-task
// wake) rather than a user or launch submission. For these the wake fire path
// owns the away-tagged turn.command_failed on a publish failure, so
// enqueueSDKTurn skips its generic marker to avoid a colliding deterministic
// event_id (see resolveFailedWake / enqueueSDKTurn).
func isSelfResumeTurnSource(source string) bool {
	switch strings.TrimSpace(source) {
	case "schedule-wakeup", "background-task":
		return true
	default:
		return false
	}
}

func scheduledWakeupFireFailureLabel(reason string) string {
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
