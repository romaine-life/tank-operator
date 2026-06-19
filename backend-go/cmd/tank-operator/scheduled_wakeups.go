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
		recordScheduledWakeupRegister("unknown", "not_found")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok || !supportsScheduledWakeups(provider) {
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
	if err := s.persistScheduledWakeupEvent(r.Context(), row, row.ScheduledAt); err != nil {
		recordScheduledWakeupRegister(provider, "event_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The wake row is what parks the session: recompute the durable activity
	// summary now so the fold lands the non-summoning "scheduled" status from
	// the row write instead of flashing "ready" until the next chat event
	// (docs/scheduled-turn-continuity.md "Race"). scheduled_wakeup.updated is
	// not a lifecycle chat event, so persistScheduledWakeupEvent alone never
	// triggers this; the cancel path already refreshes for the same reason.
	if s.activityRefresher != nil {
		if err := s.activityRefresher.RefreshSessionActivity(r.Context(), caller.Email, s.sessionScope, sessionID); err != nil {
			slog.Warn("register scheduled wakeup: activity refresh failed", "session_id", sessionID, "wakeup_id", row.WakeupID, "error", err)
		}
	}
	recordScheduledWakeupRegister(provider, "ok")
	// Echo the row's ACTUAL status: Register's ON CONFLICT returns the existing
	// row untouched, which can already be fired/failed/cancelled (a runner
	// retry after the wake resolved). Reporting an unconditional "scheduled"
	// would tell the runner a dead wake is pending.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":       string(row.Status),
		"wakeup_id":    row.WakeupID,
		"client_nonce": row.ClientNonce,
		"due_at":       row.DueAt.Format(time.RFC3339Nano),
	})
}

func supportsScheduledWakeups(provider string) bool {
	switch strings.TrimSpace(provider) {
	case "claude":
		return true
	default:
		return false
	}
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
		if rows, err := s.scheduledWakeups.CancelPendingForSession(ctx, s.sessionScope, sessionID); err != nil {
			slog.Warn("cancel pending scheduled wakeups failed", "session_id", sessionID, "error", err)
		} else {
			total += int64(len(rows))
			for _, row := range rows {
				if err := s.persistScheduledWakeupEvent(ctx, row, time.Now().UTC()); err != nil {
					slog.Warn("cancel pending scheduled wakeup event failed", "session_id", sessionID, "wakeup_id", row.WakeupID, "error", err)
				}
			}
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
	s.failExceededScheduledWakeups(ctx, now)
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

// failExceededScheduledWakeups terminals wakes stuck at the fire attempt cap.
// ClaimDue refuses rows at pgstore.MaxScheduledWakeupAttempts, so without this
// pass a wake whose fire kept half-finishing (MarkFired/MarkFailed never
// landed) would sit in 'claiming' limbo forever — pending to HasPending and
// the "scheduled" activity status, invisible to the user. The store stamps the
// durable 'failed' terminal; this runs the same post-failure bookkeeping as
// the MarkFailed path in failScheduledWakeup: persist the wake's ledger event,
// count it, and ring the away-error summon — the agent's self-scheduled
// continuation broke while the user was away, exactly like any failed wake.
func (s *appServer) failExceededScheduledWakeups(ctx context.Context, now time.Time) {
	rows, err := s.scheduledWakeups.FailExceeded(ctx, now, scheduledWakeupBatchLimit, scheduledWakeupClaimStaleAfter)
	if err != nil {
		slog.Warn("scheduled wakeup attempt-cap sweep failed", "error", err)
		return
	}
	for _, row := range rows {
		provider := strings.TrimSpace(row.Provider)
		slog.Warn("scheduled wakeup failed at attempt cap",
			"wakeup_id", row.WakeupID,
			"session_id", row.SessionID,
			"provider", provider,
			"attempts", row.AttemptCount)
		if err := s.persistScheduledWakeupEvent(ctx, row, now); err != nil {
			recordScheduledWakeupFire(provider, "event_error")
			slog.Warn("capped scheduled wakeup event persist failed",
				"wakeup_id", row.WakeupID, "session_id", row.SessionID, "error", err)
			continue
		}
		recordScheduledWakeupFire(provider, "attempt_cap_exceeded")
		s.resolveFailedWake(ctx, row.OwnerEmail, row.SessionID,
			conversation.TurnIDForClientNonce(row.ClientNonce), row.ClientNonce, provider,
			sessionactivity.AwayErrorReasonScheduledWakeup)
	}
}

func (s *appServer) fireScheduledWakeup(ctx context.Context, row pgstore.ScheduledWakeup, now time.Time) error {
	provider := strings.TrimSpace(row.Provider)
	// Liveness ladder (docs/scheduled-turn-continuity.md "Failure model"):
	// missing row / terminating / Failed are durably dead — fail fast and ring.
	// Any other non-Active status (Pending) is transient by construction: the
	// K8s watch flips the row Active → Pending on ANY probe blip
	// (sessioncontroller/writer.go EventTypePodNotReady), so a 10s kubelet
	// hiccup at fire time must defer the wake, not terminal it. The defer keeps
	// the claim's attempt bump, so a session that never recovers is bounded by
	// pgstore.MaxScheduledWakeupAttempts and terminals — ringing — through the
	// FailExceeded pass.
	if row.SessionStatus == "" {
		return s.failScheduledWakeup(ctx, row, provider, "session_not_found")
	}
	if row.SessionTerminated || row.SessionStatus == "Failed" {
		return s.failScheduledWakeup(ctx, row, provider, "session_not_active")
	}
	if row.SessionStatus != "Active" {
		recordScheduledWakeupFire(provider, "deferred_session_not_active")
		return s.scheduledWakeups.ReleaseRetainingAttempt(ctx, row.WakeupID)
	}
	// Durable double-fire guard on re-claims, mirroring the background-task
	// wake session-655 fix: if this wake's deterministic turn already EXISTS in
	// the ledger (not just reached a terminal), a prior attempt already
	// persisted the boundary and published the command — the claim merely went
	// stale before MarkFired landed. Re-submitting would re-publish the same
	// CommandID, and JetStream's msg-id dedupe only covers 24h (sessionbus
	// Duplicates window): a row stuck in 'claiming' across a longer outage
	// would run the same wake turn twice. The durable ledger, not the claim
	// status, is the authority for "already fired". attempt_count == 1 is the
	// first claim ever, so nothing can pre-exist and the happy path skips the
	// read.
	if row.AttemptCount > 1 {
		wakeTurnID := conversation.TurnIDForClientNonce(row.ClientNonce)
		if eventStore := s.sessionEventStoreForScope(row.SessionScope); eventStore != nil && wakeTurnID != "" {
			if existing, err := eventStore.EventsForTurnAfter(ctx, row.SessionID, wakeTurnID, "", 1); err == nil && len(existing.Events) > 0 {
				fired, err := s.scheduledWakeups.MarkFired(ctx, row.WakeupID, wakeTurnID)
				if err != nil {
					recordScheduledWakeupFire(provider, "store_error")
					return err
				}
				if err := s.persistScheduledWakeupEvent(ctx, fired, now); err != nil {
					recordScheduledWakeupFire(provider, "event_error")
					return err
				}
				recordScheduledWakeupFire(provider, "already_fired")
				return nil
			}
		}
	}
	resp, status, detail := s.enqueueSDKTurn(ctx, row.OwnerEmail, row.SessionID, sdkTurnRequest{
		ClientNonce:  row.ClientNonce,
		RequireNonce: true,
		Prompt:       row.Prompt,
		DisplayText:  scheduledWakeupAnnouncementText(),
		Source:       "schedule-wakeup",
		CreatedAt:    now,
		AuthorKind:   string(conversation.AuthorKindSystem),
	})
	if status != 0 {
		reason := fmt.Sprintf("enqueue_failed:%d:%s", status, strings.TrimSpace(detail))
		return s.failScheduledWakeup(ctx, row, provider, reason)
	}
	turnID := turnIDFromEnqueueResponse(resp)
	fired, err := s.scheduledWakeups.MarkFired(ctx, row.WakeupID, turnID)
	if err != nil {
		recordScheduledWakeupFire(provider, "store_error")
		return err
	}
	if err := s.persistScheduledWakeupEvent(ctx, fired, now); err != nil {
		recordScheduledWakeupFire(provider, "event_error")
		return err
	}
	recordScheduledWakeupFire(provider, "ok")
	return nil
}

func scheduledWakeupAnnouncementText() string {
	return "Timer went off!"
}

func (s *appServer) persistScheduledWakeupEvent(ctx context.Context, row pgstore.ScheduledWakeup, now time.Time) error {
	if s == nil || s.sessionEvents == nil {
		return errors.New("session event store unavailable")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	storageKey := row.TankSessionID
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(row.SessionScope, row.SessionID)
	}
	event := conversation.ScheduledWakeupUpdatedEventMap(conversation.ScheduledWakeupUpdatedArgs{
		SessionID:         row.SessionID,
		SessionStorageKey: storageKey,
		Email:             row.OwnerEmail,
		Runtime:           row.Provider,
		WakeupID:          row.WakeupID,
		Status:            string(row.Status),
		Prompt:            row.Prompt,
		ClientNonce:       row.ClientNonce,
		ScheduledTurnID:   row.ScheduledTurnID,
		ProviderItemID:    row.ProviderItemID,
		ScheduledAt:       row.ScheduledAt,
		DueAt:             row.DueAt,
		AttemptCount:      row.AttemptCount,
		FiredTurnID:       row.FiredTurnID,
		LastError:         row.LastError,
		Now:               now,
	})
	return s.persistBackendEvent(ctx, storageKey, event)
}

func (s *appServer) failScheduledWakeup(ctx context.Context, row pgstore.ScheduledWakeup, provider, reason string) error {
	failed, err := s.scheduledWakeups.MarkFailed(ctx, row.WakeupID, reason)
	if err != nil {
		recordScheduledWakeupFire(provider, "store_error")
		return err
	}
	if err := s.persistScheduledWakeupEvent(ctx, failed, time.Now().UTC()); err != nil {
		recordScheduledWakeupFire(provider, "event_error")
		return err
	}
	recordScheduledWakeupFire(provider, scheduledWakeupFireFailureLabel(reason))
	// Every reason that reaches MarkFailed is a durable failure of a promised
	// continuation — the agent parked itself on this wake and the wake will
	// never come. Per docs/scheduled-turn-continuity.md "Failure model", that
	// must ring the away-error summon regardless of WHY it broke: a publish
	// bounce (enqueue_failed) and a dead session (session_not_found /
	// session_not_active) are equally invisible to a user who left on the
	// promise of a wake. Transient non-Active sessions never get here — the
	// fire ladder defers them instead.
	s.resolveFailedWake(ctx, row.OwnerEmail, row.SessionID,
		conversation.TurnIDForClientNonce(row.ClientNonce), row.ClientNonce, provider,
		sessionactivity.AwayErrorReasonScheduledWakeup)
	return errors.New(reason)
}

// resolveFailedWake takes a session out of the non-summoning "scheduled" status
// after a wake durably failed. Every durable wake failure is an away-error —
// the agent parked itself on a promised continuation the user is guaranteed
// not to be watching — so it always persists a durable, away-tagged
// turn.command_failed (the ring carrier: the activity fold lands
// error+away_error and the SPA rings the turn-complete summon) and recomputes
// the activity summary so the would-be "scheduled" state resolves. Shared by
// the scheduled-wakeup and background-task-wake MarkFailed and FailExceeded
// (attempt-cap) paths; deferrals never come here. See
// docs/scheduled-turn-continuity.md "Failure model".
func (s *appServer) resolveFailedWake(ctx context.Context, owner, sessionID, turnID, clientNonce, runtime string, awayReason string) {
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
	if s.activityRefresher != nil {
		if err := s.activityRefresher.RefreshSessionActivity(ctx, owner, s.sessionScope, sessionID); err != nil {
			slog.Warn("failed wake activity refresh failed", "session_id", sessionID, "error", err)
		}
	}
}

// isSelfResumeTurnSource reports whether a submitted turn is a Tank-owned
// continuation rather than a user or launch submission. enqueueSDKTurn skips
// its generic turn.command_failed marker on publish failure for these because
// the schedule/background-task wake fire paths own the away-tagged failure and
// re-fire from their durable rows.
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
