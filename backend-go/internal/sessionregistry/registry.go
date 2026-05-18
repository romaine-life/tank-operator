package sessionregistry

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// Store is the Postgres-backed session registry. Replaces the previous
// CosmosStore (which kept sessions as documents in the profiles container).
// Sessions live in the `sessions` table; the monotonic session-id counter
// lives in `session_counters`.
type Store struct {
	pool  *pgxpool.Pool
	scope string
}

func NewPostgresStore(pool *pgxpool.Pool, scope string) *Store {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &Store{pool: pool, scope: scope}
}

// List returns every session record for an owner in this scope (visible
// and invisible), ordered oldest-first by created_at. Callers that only
// want visible rows must filter on SessionRecord.Visible.
//
// Returning invisible rows is load-bearing for Reader.List: it needs to
// know which session IDs have a registry row at all (regardless of
// visibility) so its pod-fallback loop doesn't append phantom rows for
// pods whose registry row is visible=false. Pre-tank-operator#525 this
// query filtered `AND visible = true` and the Reader could not
// distinguish "no registry row" from "registry row marked deleted",
// which let Terminating pods (and any pod whose K8s delete failed)
// reappear in the sidebar via the pod-fallback path.
func (s *Store) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" {
		return nil, nil
	}
	const q = `
		SELECT session_id, mode, pod_name, name, visible,
			COALESCE(to_char(requested_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS requested_at,
			COALESCE(to_char(created_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS created_at,
			COALESCE(to_char(updated_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at,
			status,
			COALESCE(to_char(ready_at       AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS ready_at,
			COALESCE(to_char(terminating_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS terminating_at,
			activity_summary,
			test_state,
			rollout_state,
			row_version
		FROM sessions
		WHERE email = $1 AND session_scope = $2
		ORDER BY created_at ASC
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
			activitySummary, testState, rolloutState                    []byte
			rowVersion                                                  int64
		)
		if err := rows.Scan(
			&sessionID, &mode, &podName, &name, &visible,
			&requestedAt, &createdAt, &updatedAt,
			&status, &readyAt, &terminatingAt,
			&activitySummary, &testState, &rolloutState,
			&rowVersion,
		); err != nil {
			return nil, err
		}
		if mode == "" {
			mode = sessionmodel.DefaultSessionMode
		}
		records = append(records, sessionmodel.SessionRecord{
			ID:              sessionID,
			Email:           normalized,
			Mode:            mode,
			Scope:           s.scope,
			PodName:         podName,
			Name:            name,
			Visible:         visible,
			RequestedAt:     requestedAt,
			CreatedAt:       createdAt,
			UpdatedAt:       updatedAt,
			Status:          status,
			ReadyAt:         readyAt,
			TerminatingAt:   terminatingAt,
			ActivitySummary: activitySummary,
			TestState:       unmarshalJSONB(testState),
			RolloutState:    unmarshalJSONB(rolloutState),
			RowVersion:      rowVersion,
		})
	}
	return records, rows.Err()
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
