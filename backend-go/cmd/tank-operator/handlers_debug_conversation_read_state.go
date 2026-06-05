// Admin-only debug surface for the durable conversation read-state
// cursor. Returns the per-session, per-owner read cursor alongside the
// session's durable activity_summary so an operator can answer "is
// this user's read cursor stuck behind the live tail" without
// browser devtools or a one-off psql pod.
//
// This is the per-entity diagnostic surface for the navigation-mode
// observability story. The aggregate signal is
// `tank_chat_scroll_client_events_total{event="navigation-mode-entered-
// historical-anchor"}` plus the
// `TankChatScrollUserAtBottomLatched` alert; this endpoint resolves
// "which sessions, who do they belong to, by how many events is the
// cursor behind" once the alert fires.
//
// The retired bug class (session 269, 2026-05-27) latched the
// frontend's userScrolledUp boolean during react-virtuoso's
// followOutput smooth-scroll catch-up window, leaving the durable
// `conversation_read_state.last_read_order_key` frozen at the
// user_message.created event that opened the turn. The new
// NavigationMode state machine (frontend/src/navigationMode.ts)
// eliminated the latch path; this endpoint is how a future operator
// confirms recurrence is durable rather than transient.
//
// Auth: Tank admin power required. Counts as an admin cross-user
// read; emits a structured slog audit line per call.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func (s *appServer) handleDebugConversationReadState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordDebugConversationReadStateRead("forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		recordDebugConversationReadStateRead("bad_request")
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		recordDebugConversationReadStateRead("forbidden")
		writeError(w, status, scopeErr.Error())
		return
	}

	// Owner defaults to the caller, but an admin can target another
	// user's cursor — that's the cross-user diagnostic the contract
	// names. Counts toward the admin cross-user read counter.
	owner := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("owner")))
	if owner == "" {
		owner = strings.ToLower(strings.TrimSpace(user.Email))
	}
	if owner != strings.ToLower(strings.TrimSpace(user.Email)) {
		recordAdminCrossUserRead()
	}

	if s.pgPool == nil {
		recordDebugConversationReadStateRead("not_configured")
		writeError(w, http.StatusServiceUnavailable, "Postgres pool not wired")
		return
	}

	sessionRow, err := fetchSessionRowByID(r.Context(), s.pgPool, owner, scope, sessionID)
	if err != nil {
		recordDebugConversationReadStateRead("store_error")
		slog.Warn("debug conversation-read-state: session row read failed",
			"caller_email", user.Email,
			"owner", owner,
			"session_scope", scope,
			"session_id", sessionID,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "session row read failed: "+err.Error())
		return
	}
	if sessionRow == nil {
		recordDebugConversationReadStateRead("empty")
		slog.Info("debug conversation-read-state: session not found",
			"caller_email", user.Email,
			"owner", owner,
			"session_scope", scope,
			"session_id", sessionID,
		)
		writeJSON(w, http.StatusNotFound, map[string]any{
			"description": debugConversationReadStateDescription,
			"session_id":  sessionID,
			"owner":       owner,
			"scope":       scope,
			"reason":      "session_not_found",
		})
		return
	}

	readStateStore := s.readStateStoreForScope(scope)
	readState, err := readStateStore.Get(r.Context(), owner, sessionID)
	if err != nil {
		recordDebugConversationReadStateRead("store_error")
		slog.Warn("debug conversation-read-state: read-state lookup failed",
			"caller_email", user.Email,
			"owner", owner,
			"session_scope", scope,
			"session_id", sessionID,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "read-state lookup failed: "+err.Error())
		return
	}

	activity := decodeActivitySummary(sessionRow.ActivitySummary)
	lastReadOrderKey := ""
	readStateUpdatedAt := ""
	if readState != nil {
		lastReadOrderKey = readState.LastReadOrderKey
		readStateUpdatedAt = readState.UpdatedAt
	}
	lastDurableOrderKey := activity.LastOrderKey
	lag := lastDurableOrderKey != "" && lastReadOrderKey != "" &&
		lastDurableOrderKey > lastReadOrderKey

	slog.Info("debug conversation-read-state: ok",
		"caller_email", user.Email,
		"owner", owner,
		"session_scope", scope,
		"session_id", sessionID,
		"session_status", sessionRow.Status,
		"active_turn_id", activity.ActiveTurnID,
		"last_durable_order_key", lastDurableOrderKey,
		"last_read_order_key", lastReadOrderKey,
		"cursor_lags", lag,
	)
	recordDebugConversationReadStateRead("ok")

	writeJSON(w, http.StatusOK, map[string]any{
		"description":            debugConversationReadStateDescription,
		"session_id":             sessionID,
		"scope":                  scope,
		"owner":                  owner,
		"session_status":         sessionRow.Status,
		"session_visible":        sessionRow.Visible,
		"active_turn_id":         activity.ActiveTurnID,
		"activity_status":        activity.Status,
		"unread_count":           activity.UnreadCount,
		"needs_input":            activity.NeedsInput,
		"last_durable_order_key": lastDurableOrderKey,
		"last_read_order_key":    lastReadOrderKey,
		"read_state_updated_at":  readStateUpdatedAt,
		"cursor_lags":            lag,
	})
}

// fetchSessionRowByID returns the single registry row for one
// (owner, scope, session_id). nil + nil for "no such row" so the
// handler can distinguish missing from store error. Returns the
// invisible row too — soft-deleted sessions still have durable
// read-state cursors and an admin may legitimately want to diagnose
// post-delete.
func fetchSessionRowByID(ctx context.Context, pool pgxQuerier, owner, scope, sessionID string) (*sessionmodel.SessionRecord, error) {
	const q = `
		SELECT session_id, mode, pod_name, name, visible,
			status,
			COALESCE(to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at,
			activity_summary
		FROM sessions
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	var (
		mode, podName, recStatus, recUpdatedAt string
		recName                                string
		recVisible                             bool
		activity                               []byte
		recSessionID                           string
	)
	if err := pool.QueryRow(ctx, q, owner, scope, sessionID).Scan(
		&recSessionID, &mode, &podName, &recName, &recVisible,
		&recStatus, &recUpdatedAt,
		&activity,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &sessionmodel.SessionRecord{
		ID:              recSessionID,
		Email:           owner,
		Mode:            mode,
		Scope:           scope,
		PodName:         podName,
		Name:            recName,
		Visible:         recVisible,
		Status:          recStatus,
		UpdatedAt:       recUpdatedAt,
		ActivitySummary: activity,
	}, nil
}

// pgxQuerier is the minimal interface fetchSessionRowByID needs.
// Lets tests inject a stub without standing up a real pgxpool.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// activitySummaryView is the subset of sessions.activity_summary the
// debug surface returns. The JSONB blob carries more fields (e.g.
// `failed`); this struct only names the ones the navigation-mode
// diagnosis directly consults.
type activitySummaryView struct {
	Status       string `json:"status,omitempty"`
	ActiveTurnID string `json:"active_turn_id,omitempty"`
	LastOrderKey string `json:"last_order_key,omitempty"`
	UnreadCount  int    `json:"unread_count,omitempty"`
	NeedsInput   bool   `json:"needs_input,omitempty"`
}

func decodeActivitySummary(raw []byte) activitySummaryView {
	view := activitySummaryView{}
	if len(raw) == 0 {
		return view
	}
	// Decode into a permissive map first so an extra/missing field on
	// either side does not error out the whole endpoint. The wire
	// shape is owned by sessions/manager.go's writer; we mirror the
	// fields we care about and tolerate evolution elsewhere.
	var payload struct {
		Status       string `json:"status"`
		ActiveTurnID any    `json:"active_turn_id"`
		LastOrderKey string `json:"last_order_key"`
		UnreadCount  int    `json:"unread_count"`
		NeedsInput   bool   `json:"needs_input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return view
	}
	view.Status = payload.Status
	view.LastOrderKey = payload.LastOrderKey
	view.UnreadCount = payload.UnreadCount
	view.NeedsInput = payload.NeedsInput
	if s, ok := payload.ActiveTurnID.(string); ok {
		view.ActiveTurnID = s
	}
	return view
}

// debugConversationReadStateDescription rides in the JSON payload so
// an operator running `curl | jq` understands the meaning of each
// field without leaving the terminal.
const debugConversationReadStateDescription = `Per-session, per-owner read-state diagnostic for transcript navigation.

When the TankChatScrollUserAtBottomLatched alert fires, the runbook
directs you here for a specific session_id you suspect is affected.
The two values to compare:

  - last_durable_order_key: the session's most recent durable
    event order key (sessions.activity_summary.last_order_key).
  - last_read_order_key: the per-(owner, session) read cursor
    (conversation_read_state.last_read_order_key).

If cursor_lags is true AND active_turn_id is empty AND session_status
is "ready", the user's browser thinks it is not at the live tail
even though the session has been idle. That is the durable footprint
of the retired bug class (session 269, 2026-05-27) and the new
navigation-mode telemetry should show
navigation-mode-entered-historical-anchor transitions that no user
gesture triggered.

The endpoint never mutates state. Soft-deleted sessions are
returned (session_visible=false) because their read cursors are
still durable and operators may legitimately need to diagnose
post-delete.`
