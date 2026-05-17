package sessionregistry

import (
	"context"
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

// List returns all session records for an owner in this scope (both
// visible and tombstoned), ordered oldest-first by created_at. Callers
// that want only the user-facing session list must filter on the Visible
// field — the registry is also the source of truth for "this session_id
// was registered and is now tombstoned", which sessions.Reader.List needs
// so it can distinguish a still-terminating pod owned by a known-deleted
// session (drop) from a never-registered orphan pod (drop and count).
//
// Pre-#83 follow-up the SQL filter was `WHERE visible = true` and the
// pod-listing loop in sessions.Reader.List would re-add tombstoned
// session_ids from the Kubernetes pod API whenever the pod was still
// inside terminationGracePeriodSeconds — the snapshot lied about the
// just-deleted session and the sidebar got "stuck deleting" rows. Per
// docs/migration-policy.md the registry is the durable enumeration
// source; the pod is hydration data for an existing registry row, never
// the row itself.
func (s *Store) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" {
		return nil, nil
	}
	const q = `
		SELECT session_id, mode, pod_name, name, visible,
			COALESCE(to_char(requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS requested_at,
			COALESCE(to_char(created_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS created_at,
			COALESCE(to_char(updated_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at
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
			name                                                        *string
			visible                                                     bool
		)
		if err := rows.Scan(&sessionID, &mode, &podName, &name, &visible, &requestedAt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if mode == "" {
			mode = sessionmodel.DefaultSessionMode
		}
		records = append(records, sessionmodel.SessionRecord{
			ID:          sessionID,
			Email:       normalized,
			Mode:        mode,
			Scope:       s.scope,
			PodName:     podName,
			Name:        name,
			Visible:     visible,
			RequestedAt: requestedAt,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		})
	}
	return records, rows.Err()
}
