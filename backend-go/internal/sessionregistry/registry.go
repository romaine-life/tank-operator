package sessionregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// Store is the Postgres-backed session registry. Replaces the previous
// CosmosStore (which kept sessions as documents in the profiles container).
// Sessions live in the `sessions` table; the monotonic session-id counter
// lives in `session_counters`.
type Store struct {
	pool  *pgxpool.Pool
	scope string
}

type RetiredSession struct {
	Email string
	ID    string
}

func NewPostgresStore(pool *pgxpool.Pool, scope string) *Store {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &Store{pool: pool, scope: scope}
}

// List returns every session record for an owner in this scope (visible
// and invisible), ordered by durable sidebar position. Callers that only
// want visible rows must filter on SessionRecord.Visible.
//
// Returning invisible rows is load-bearing for the row-update catch-up
// path: a client that disconnects during delete needs to receive the
// visible=false row and tombstone the id when it reconnects. Reader.List
// filters invisible rows for the snapshot, but debug and catch-up paths
// consume the full owner-scoped row set.
func (s *Store) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" {
		return nil, nil
	}
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
			sessions.model,
			sessions.effort,
			sessions.runtime_model,
			sessions.runtime_effort,
			COALESCE(to_char(sessions.runtime_configured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_configured_at,
			sessions.runtime_context_window_tokens,
			sessions.runtime_context_window_source,
			COALESCE(to_char(sessions.runtime_context_window_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_context_window_observed_at,
			COALESCE(sessions.agent_avatar_id, ''),
			COALESCE(sessions.system_avatar_id, ''),
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
		WHERE sessions.email = $1 AND sessions.session_scope = $2
		ORDER BY sessions.sidebar_position DESC, sessions.created_at DESC, sessions.session_id DESC
	`
	rows, err := s.pool.Query(ctx, q, normalized, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []sessionmodel.SessionRecord
	for rows.Next() {
		var (
			sessionID, mode, podName, requestedAt, createdAt, updatedAt string
			status, readyAt, terminatingAt                              string
			name                                                        *string
			visible                                                     bool
			activitySummary, testState, rolloutState, cloneState        []byte
			repos, capabilities                                         []string
			model, effort, runtimeModel, runtimeEffort, runtimeAt       string
			runtimeContextWindowSource, runtimeContextWindowObservedAt  string
			agentAvatarID, systemAvatarID                               string
			runtimeContextWindowTokens, sidebarPosition, rowVersion     int64
			bugLabelID                                                  sql.NullInt64
			bugLabelName, bugLabelSlug                                  sql.NullString
		)
		if err := rows.Scan(
			&sessionID, &mode, &podName, &name, &visible,
			&requestedAt, &createdAt, &updatedAt,
			&status, &readyAt, &terminatingAt,
			&activitySummary, &testState, &rolloutState,
			&repos, &cloneState, &capabilities, &model, &effort,
			&runtimeModel, &runtimeEffort, &runtimeAt,
			&runtimeContextWindowTokens, &runtimeContextWindowSource, &runtimeContextWindowObservedAt,
			&agentAvatarID, &systemAvatarID,
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
		records = append(records, sessionmodel.SessionRecord{
			ID:                             sessionID,
			Email:                          normalized,
			Mode:                           mode,
			Scope:                          s.scope,
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
			TestState:                      unmarshalJSONB(testState),
			RolloutState:                   unmarshalJSONB(rolloutState),
			Repos:                          repos,
			CloneState:                     unmarshalJSONB(cloneState),
			Capabilities:                   capabilities,
			Model:                          model,
			Effort:                         effort,
			RuntimeModel:                   runtimeModel,
			RuntimeEffort:                  runtimeEffort,
			RuntimeConfiguredAt:            runtimeAt,
			RuntimeContextWindowTokens:     runtimeContextWindowTokens,
			RuntimeContextWindowSource:     runtimeContextWindowSource,
			RuntimeContextWindowObservedAt: runtimeContextWindowObservedAt,
			AgentAvatarID:                  agentAvatarID,
			SystemAvatarID:                 systemAvatarID,
			SidebarPosition:                sidebarPosition,
			RowVersion:                     rowVersion,
			BugLabel:                       bugLabelFromScan(bugLabelID, bugLabelName, bugLabelSlug),
		})
	}
	return records, rows.Err()
}

func bugLabelFromScan(id sql.NullInt64, name, slug sql.NullString) *sessionmodel.SessionBugLabel {
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

// ListAllIDsForScope returns every session_id in this orchestrator's
// scope, regardless of owner. Used by the NATS orphan-consumer sweep
// to decide which JetStream durable consumers still belong to a live
// session — the sweep's "live" predicate is just "row exists in this
// scope", visible or not. Soft-deleted (visible=false) rows still
// count as live for the sweep purpose because the pod and its
// consumers may still be terminating.
//
// Scope-wide rather than per-owner because consumer names encode
// (scope, session_id) only — there is no owner dimension on the
// consumer side.
func (s *Store) ListAllIDsForScope(ctx context.Context) (map[string]struct{}, error) {
	const q = `SELECT session_id FROM sessions WHERE session_scope = $1`
	rows, err := s.pool.Query(ctx, q, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out, rows.Err()
}

// unmarshalJSONB turns a jsonb column's raw bytes into the
// map[string]any the snapshot handler expects to render. Empty/NULL
// columns return nil, which the SPA renders as "no state set."
func unmarshalJSONB(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
