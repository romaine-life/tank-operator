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
func (s *appServer) writeSessionRowUpdatesPage(ctx context.Context, w http.ResponseWriter, owner, scope string, cursor *int64, delivered map[string]int64) (bool, int, error) {
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
		// The drain cursor advances over every row the drain PROCESSED,
		// whether or not it re-emits: rows the live NATS path already
		// delivered (per the per-session high-water map) are skipped so a
		// heartbeat re-drain doesn't systematically double-send the whole
		// recent window, but they still move the floor forward. Rows the
		// live path MISSED (dropped wake channel, NATS blip,
		// cross-replica reordering of the global row_version sequence)
		// are emitted here — this is the convergence path that makes an
		// open sidebar consistent within one heartbeat instead of only on
		// browser reconnect.
		*cursor = record.RowVersion
		if delivered != nil && record.RowVersion <= delivered[record.ID] {
			continue
		}
		payload, err := sessioncontroller.MarshalRowUpdate(record)
		if err != nil {
			slog.Warn("session row updates page: marshal failed",
				"owner", owner, "scope", scope,
				"session_id", record.ID, "error", err)
			continue
		}
		writeRawSSEEvent(w, "session-row", fmt.Sprintf("%d", record.RowVersion), payload)
		if delivered != nil {
			delivered[record.ID] = record.RowVersion
		}
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
			sessions.runtime_provider_session_id,
			COALESCE(to_char(sessions.runtime_provider_session_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_provider_session_observed_at,
			sessions.provider_rate_limit_info,
			COALESCE(to_char(sessions.provider_rate_limit_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS provider_rate_limit_observed_at,
			sessions.sidebar_position,
			sessions.row_version,
			bug_labels.id,
			bug_labels.name,
			bug_labels.slug,
			COALESCE(bug_labels.labels_json, '[]'::jsonb)
		FROM sessions
		LEFT JOIN LATERAL (
			SELECT
				(array_agg(bug_labels.id ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS id,
				(array_agg(bug_labels.name ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS name,
				(array_agg(bug_labels.slug ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS slug,
				jsonb_agg(
					jsonb_build_object(
						'id', bug_labels.id,
						'name', bug_labels.name,
						'slug', bug_labels.slug,
						'display_name', 'bug: ' || bug_labels.name
					)
					ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC
				) AS labels_json
			FROM session_bug_labels
			JOIN bug_labels
				ON bug_labels.id = session_bug_labels.bug_label_id
			WHERE session_bug_labels.owner_email = sessions.email
			  AND session_bug_labels.session_scope = sessions.session_scope
			  AND session_bug_labels.session_id = sessions.session_id
		) bug_labels ON true
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
			name                                                        string
			visible                                                     bool
			activitySummary, testState, rolloutState, cloneState        []byte
			providerRateLimitInfo                                       []byte
			repos, capabilities                                         []string
			agentAvatarID, systemAvatarID                               string
			model, effort                                               string
			runtimeModel, runtimeEffort, runtimeAt                      string
			runtimeContextWindowTokens                                  int64
			runtimeContextWindowSource, runtimeContextWindowAt          string
			runtimeProviderSessionID, runtimeProviderSessionObservedAt  string
			providerRateLimitObservedAt                                 string
			sidebarPosition, rowVersion                                 int64
			bugLabelID                                                  sql.NullInt64
			bugLabelName, bugLabelSlug                                  sql.NullString
			bugLabelsRaw                                                []byte
		)
		if err := rows.Scan(
			&sessionID, &mode, &podName, &name, &visible,
			&requestedAt, &createdAt, &updatedAt,
			&status, &readyAt, &terminatingAt,
			&activitySummary, &testState, &rolloutState,
			&repos, &cloneState, &capabilities, &agentAvatarID, &systemAvatarID,
			&model, &effort, &runtimeModel, &runtimeEffort, &runtimeAt,
			&runtimeContextWindowTokens, &runtimeContextWindowSource, &runtimeContextWindowAt,
			&runtimeProviderSessionID, &runtimeProviderSessionObservedAt,
			&providerRateLimitInfo, &providerRateLimitObservedAt,
			&sidebarPosition,
			&rowVersion,
			&bugLabelID,
			&bugLabelName,
			&bugLabelSlug,
			&bugLabelsRaw,
		); err != nil {
			return nil, err
		}
		if mode == "" {
			mode = sessionmodel.DefaultSessionMode
		}
		out = append(out, sessionmodel.SessionRecord{
			ID:                               sessionID,
			Email:                            strings.ToLower(strings.TrimSpace(owner)),
			Mode:                             mode,
			Scope:                            scope,
			PodName:                          podName,
			Name:                             name,
			Visible:                          visible,
			RequestedAt:                      requestedAt,
			CreatedAt:                        createdAt,
			UpdatedAt:                        updatedAt,
			Status:                           status,
			ReadyAt:                          readyAt,
			TerminatingAt:                    terminatingAt,
			ActivitySummary:                  activitySummary,
			TestState:                        unmarshalJSONBField(testState),
			RolloutState:                     unmarshalJSONBField(rolloutState),
			Repos:                            repos,
			CloneState:                       unmarshalJSONBField(cloneState),
			Capabilities:                     capabilities,
			AgentAvatarID:                    agentAvatarID,
			SystemAvatarID:                   systemAvatarID,
			Model:                            model,
			Effort:                           effort,
			RuntimeModel:                     runtimeModel,
			RuntimeEffort:                    runtimeEffort,
			RuntimeConfiguredAt:              runtimeAt,
			RuntimeContextWindowTokens:       runtimeContextWindowTokens,
			RuntimeContextWindowSource:       runtimeContextWindowSource,
			RuntimeContextWindowObservedAt:   runtimeContextWindowAt,
			RuntimeProviderSessionID:         runtimeProviderSessionID,
			RuntimeProviderSessionObservedAt: runtimeProviderSessionObservedAt,
			ProviderRateLimitInfo:            unmarshalJSONBField(providerRateLimitInfo),
			ProviderRateLimitObservedAt:      providerRateLimitObservedAt,
			SidebarPosition:                  sidebarPosition,
			RowVersion:                       rowVersion,
			BugLabel:                         bugLabelFromSessionListScan(bugLabelID, bugLabelName, bugLabelSlug),
			BugLabels:                        bugLabelsFromSessionListJSON(bugLabelsRaw),
		})
	}
	return out, rows.Err()
}

func bugLabelsFromSessionListJSON(raw []byte) []*sessionmodel.SessionBugLabel {
	if len(raw) == 0 {
		return nil
	}
	var labels []*sessionmodel.SessionBugLabel
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil
	}
	out := labels[:0]
	for _, label := range labels {
		if label == nil || strings.TrimSpace(label.Name) == "" || strings.TrimSpace(label.Slug) == "" {
			continue
		}
		if strings.TrimSpace(label.DisplayName) == "" {
			label.DisplayName = "bug: " + strings.TrimSpace(label.Name)
		}
		out = append(out, label)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
// Duplicate suppression is PER SESSION, not stream-global: row_version
// is one global sequence published post-commit by two replicas, so a
// late-arriving lower-version payload for a DIFFERENT session is not a
// duplicate — the old stream-global cursor dropped it though it was
// never delivered (issue #1077 item 3), and worse, advancing the drain
// floor here let the heartbeat re-drain skip rows the live path missed.
// The drain cursor is therefore owned exclusively by the catch-up
// query; live emissions only update the per-session high-water map.
func (s *appServer) emitSessionRowPayload(w http.ResponseWriter, delivered map[string]int64, expectedScope string, payload []byte) {
	var probe struct {
		Cursor string `json:"cursor"`
		Row    struct {
			ID           string `json:"id"`
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
	sessionID := strings.TrimSpace(probe.Row.ID)
	if sessionID != "" && rowVersion <= delivered[sessionID] {
		return
	}
	writeRawSSEEvent(w, "session-row", probe.Cursor, payload)
	if sessionID != "" {
		delivered[sessionID] = rowVersion
	}
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
