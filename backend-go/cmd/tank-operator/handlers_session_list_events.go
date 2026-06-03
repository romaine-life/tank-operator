// Row-update SSE surface for the per-owner session list. Replaces the
// pre-Phase-3 typed-event handlers (docs/session-list-redesign.md).
// Catch-up reads from the sessions table directly: any row whose
// row_version > cursor is emitted as a row-update payload. Live
// delivery comes from the NATS row-update subject and the payloads
// are forwarded verbatim. The SPA's SessionStore is a row cache that
// replaces-by-id on each delivery — no event-type switch, no
// placeholder synthesis.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	// listEventStreamPageLimit caps the number of rows the SSE catch-up
	// loop hands back in a single round.
	listEventStreamPageLimit   = 100
	sessionListStreamHeartbeat = 15 * time.Second
)

// writeSessionRowUpdatesPage emits up to listEventStreamPageLimit
// rows from the sessions table whose row_version > *cursor, advancing
// the cursor as it goes. Returns hasMore so the SSE handler can loop
// until the catch-up is fully drained before subscribing to live NATS
// payloads.
//
// Returning rows with visible=false is load-bearing: the SPA's
// SessionStore tombstones the id on receipt, so a session deleted
// during the disconnect window is correctly removed from the cache
// on reconnect.
func (s *appServer) writeSessionRowUpdatesPage(ctx context.Context, w http.ResponseWriter, owner, scope string, cursor *int64) (bool, int, error) {
	if s.pgPool == nil {
		return false, 0, nil
	}
	records, err := fetchSessionRowsAfter(ctx, s.pgPool, owner, scope, *cursor, listEventStreamPageLimit+1)
	if err != nil {
		return false, 0, err
	}
	hasMore := len(records) > listEventStreamPageLimit
	if hasMore {
		records = records[:listEventStreamPageLimit]
	}
	count := 0
	for _, record := range records {
		payload, err := sessioncontroller.MarshalRowUpdate(record)
		if err != nil {
			slog.Warn("session row updates page: marshal failed",
				"owner", owner, "scope", scope,
				"session_id", record.ID, "error", err)
			continue
		}
		writeRawSSEEvent(w, "session-row", fmt.Sprintf("%d", record.RowVersion), payload)
		*cursor = record.RowVersion
		count++
		sessionListStreamEmittedTotal.Inc()
	}
	return hasMore, count, nil
}

// fetchSessionRowsAfter reads sessions rows whose row_version >
// cursor for one (owner, scope), ordered by row_version ascending and
// capped at limit. Includes visible=false rows — the SPA's
// SessionStore needs them to tombstone deleted sessions on reconnect.
func fetchSessionRowsAfter(ctx context.Context, pool *pgxpool.Pool, owner, scope string, cursor int64, limit int) ([]sessionmodel.SessionRecord, error) {
	const q = `
		SELECT sessions.session_id, sessions.mode, sessions.pod_name, sessions.name, sessions.visible,
			COALESCE(to_char(sessions.requested_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS requested_at,
			COALESCE(to_char(sessions.created_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS created_at,
			COALESCE(to_char(sessions.updated_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at,
			sessions.status,
			COALESCE(to_char(sessions.ready_at       AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS ready_at,
			COALESCE(to_char(sessions.terminating_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS terminating_at,
			sessions.activity_summary,
			sessions.test_state,
			sessions.rollout_state,
			COALESCE(sessions.repos, '{}'::text[]),
			sessions.clone_state,
			COALESCE(sessions.capabilities, '{}'::text[]),
			COALESCE(sessions.agent_avatar_id, ''),
			COALESCE(sessions.system_avatar_id, ''),
			sessions.model,
			sessions.effort,
			sessions.runtime_model,
			sessions.runtime_effort,
			COALESCE(to_char(sessions.runtime_configured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_configured_at,
			sessions.runtime_context_window_tokens,
			sessions.runtime_context_window_source,
			COALESCE(to_char(sessions.runtime_context_window_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_context_window_observed_at,
			sessions.sidebar_position,
			sessions.row_version,
			bug_labels.id,
			bug_labels.name,
			bug_labels.slug
		FROM sessions
		LEFT JOIN session_bug_labels
			ON session_bug_labels.owner_email = sessions.email
			AND session_bug_labels.session_scope = sessions.session_scope
			AND session_bug_labels.session_id = sessions.session_id
		LEFT JOIN bug_labels
			ON bug_labels.id = session_bug_labels.bug_label_id
		WHERE sessions.email = $1 AND sessions.session_scope = $2 AND sessions.row_version > $3
		ORDER BY sessions.row_version ASC
		LIMIT $4
	`
	rows, err := pool.Query(ctx, q, strings.ToLower(strings.TrimSpace(owner)), scope, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionmodel.SessionRecord
	for rows.Next() {
		var (
			sessionID, mode, podName, requestedAt, createdAt, updatedAt string
			status, readyAt, terminatingAt                              string
			name                                                        *string
			visible                                                     bool
			activitySummary, testState, rolloutState, cloneState        []byte
			repos, capabilities                                         []string
			agentAvatarID, systemAvatarID                               string
			model, effort                                               string
			runtimeModel, runtimeEffort, runtimeAt                      string
			runtimeContextWindowTokens                                  int64
			runtimeContextWindowSource, runtimeContextWindowAt          string
			sidebarPosition, rowVersion                                 int64
			bugLabelID                                                  sql.NullInt64
			bugLabelName, bugLabelSlug                                  sql.NullString
		)
		if err := rows.Scan(
			&sessionID, &mode, &podName, &name, &visible,
			&requestedAt, &createdAt, &updatedAt,
			&status, &readyAt, &terminatingAt,
			&activitySummary, &testState, &rolloutState,
			&repos, &cloneState, &capabilities, &agentAvatarID, &systemAvatarID,
			&model, &effort, &runtimeModel, &runtimeEffort, &runtimeAt,
			&runtimeContextWindowTokens, &runtimeContextWindowSource, &runtimeContextWindowAt,
			&sidebarPosition,
			&rowVersion,
			&bugLabelID,
			&bugLabelName,
			&bugLabelSlug,
		); err != nil {
			return nil, err
		}
		if mode == "" {
			mode = sessionmodel.DefaultSessionMode
		}
		out = append(out, sessionmodel.SessionRecord{
			ID:                             sessionID,
			Email:                          strings.ToLower(strings.TrimSpace(owner)),
			Mode:                           mode,
			Scope:                          scope,
			PodName:                        podName,
			Name:                           name,
			Visible:                        visible,
			RequestedAt:                    requestedAt,
			CreatedAt:                      createdAt,
			UpdatedAt:                      updatedAt,
			Status:                         status,
			ReadyAt:                        readyAt,
			TerminatingAt:                  terminatingAt,
			ActivitySummary:                activitySummary,
			TestState:                      unmarshalJSONBField(testState),
			RolloutState:                   unmarshalJSONBField(rolloutState),
			Repos:                          repos,
			CloneState:                     unmarshalJSONBField(cloneState),
			Capabilities:                   capabilities,
			AgentAvatarID:                  agentAvatarID,
			SystemAvatarID:                 systemAvatarID,
			Model:                          model,
			Effort:                         effort,
			RuntimeModel:                   runtimeModel,
			RuntimeEffort:                  runtimeEffort,
			RuntimeConfiguredAt:            runtimeAt,
			RuntimeContextWindowTokens:     runtimeContextWindowTokens,
			RuntimeContextWindowSource:     runtimeContextWindowSource,
			RuntimeContextWindowObservedAt: runtimeContextWindowAt,
			SidebarPosition:                sidebarPosition,
			RowVersion:                     rowVersion,
			BugLabel:                       bugLabelFromSessionListScan(bugLabelID, bugLabelName, bugLabelSlug),
		})
	}
	return out, rows.Err()
}

func bugLabelFromSessionListScan(id sql.NullInt64, name, slug sql.NullString) *sessionmodel.SessionBugLabel {
	if !id.Valid || !name.Valid || strings.TrimSpace(name.String) == "" || !slug.Valid || strings.TrimSpace(slug.String) == "" {
		return nil
	}
	labelName := strings.TrimSpace(name.String)
	return &sessionmodel.SessionBugLabel{
		ID:          id.Int64,
		Name:        labelName,
		Slug:        strings.TrimSpace(slug.String),
		DisplayName: "bug: " + labelName,
	}
}

func unmarshalJSONBField(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// emitSessionRowPayload forwards a NATS row-update payload to the
// connected client. Re-decodes just enough to extract the cursor and
// validate the scope — the (email, scope) NATS subject already makes
// cross-scope delivery unreachable in steady state, but the
// defensive check turns any producer-side scope regression into a
// counter increment instead of silently mutating the SPA's cache.
//
// A payload whose cursor is ≤ the current cursor was already streamed
// during catch-up or is a duplicate — skip rather than re-emit.
func (s *appServer) emitSessionRowPayload(w http.ResponseWriter, cursor *int64, expectedScope string, payload []byte) {
	var probe struct {
		Cursor string `json:"cursor"`
		Row    struct {
			SessionScope string `json:"session_scope"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return
	}
	if scope := strings.TrimSpace(probe.Row.SessionScope); scope != "" && scope != expectedScope {
		sessionListCrossScopeEventsDroppedTotal.Inc()
		slog.Warn("session row payload dropped: scope mismatch",
			"expected_scope", expectedScope,
			"payload_scope", scope,
			"cursor", probe.Cursor,
		)
		return
	}
	rowVersion, err := strconv.ParseInt(strings.TrimSpace(probe.Cursor), 10, 64)
	if err != nil || rowVersion <= 0 {
		return
	}
	if rowVersion <= *cursor {
		return
	}
	writeRawSSEEvent(w, "session-row", probe.Cursor, payload)
	*cursor = rowVersion
	sessionListStreamEmittedTotal.Inc()
}

// writeRawSSEEvent writes an SSE frame with a pre-marshaled JSON
// `data` payload. Used by the row-update path to forward NATS bytes
// without a marshal round-trip.
func writeRawSSEEvent(w http.ResponseWriter, eventName, id string, data []byte) {
	if id = sanitizeSSEField(id); id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	if eventName = sanitizeSSEField(eventName); eventName != "" {
		fmt.Fprintf(w, "event: %s\n", eventName)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}
