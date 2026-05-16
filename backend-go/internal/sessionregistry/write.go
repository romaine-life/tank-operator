package sessionregistry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// NextSessionID atomically allocates the next monotonic session id for this
// scope. One UPSERT increments and returns in a single statement; no retry
// loop or advisory lock is needed because the row-level conflict resolution
// is serial inside Postgres.
//
// The returned value matches the value the Cosmos impl returned: the number
// allocated by THIS call (i.e. the row's `next_session_number` is incremented
// to N+1 for the next caller, and we return N).
func (s *Store) NextSessionID(ctx context.Context) (string, error) {
	const q = `
		INSERT INTO session_counters (session_scope, next_session_number, updated_at)
		VALUES ($1, 2, now())
		ON CONFLICT (session_scope) DO UPDATE
		SET next_session_number = session_counters.next_session_number + 1,
			updated_at = now()
		RETURNING next_session_number - 1
	`
	var allocated int64
	if err := s.pool.QueryRow(ctx, q, s.scope).Scan(&allocated); err != nil {
		return "", fmt.Errorf("allocate session id: %w", err)
	}
	return fmt.Sprintf("%d", allocated), nil
}

// Upsert writes or overwrites a session record. created_at/updated_at default
// to now() on insert; on conflict (same primary key) we keep the existing
// created_at and only advance updated_at.
func (s *Store) Upsert(ctx context.Context, record sessionmodel.SessionRecord) error {
	normalized := strings.ToLower(record.Email)
	scope := record.Scope
	if scope == "" {
		scope = s.scope
	}
	mode := record.Mode
	if mode == "" {
		mode = sessionmodel.DefaultSessionMode
	}
	now := time.Now().UTC()
	requestedAt := parseTimestamp(record.RequestedAt)
	createdAt := parseTimestamp(record.CreatedAt)
	updatedAt := parseTimestamp(record.UpdatedAt)
	if updatedAt.IsZero() {
		updatedAt = now
	}
	// Determine the effective visible value (default true if unset).
	visible := record.Visible

	const q = `
		INSERT INTO sessions (
			email, session_scope, session_id,
			mode, pod_name, name, visible,
			requested_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, COALESCE($9, now()), $10
		)
		ON CONFLICT (email, session_scope, session_id) DO UPDATE
		SET mode         = EXCLUDED.mode,
			pod_name     = EXCLUDED.pod_name,
			name         = EXCLUDED.name,
			visible      = EXCLUDED.visible,
			requested_at = COALESCE(EXCLUDED.requested_at, sessions.requested_at),
			updated_at   = EXCLUDED.updated_at
	`
	_, err := s.pool.Exec(ctx, q,
		normalized,
		scope,
		record.ID,
		mode,
		record.PodName,
		record.Name,
		visible,
		nullableTimestamp(requestedAt),
		nullableTimestamp(createdAt),
		updatedAt,
	)
	return err
}

// SetName updates the display name. Missing-session is a no-op (matches the
// previous Cosmos impl, which swallowed not-found there).
func (s *Store) SetName(ctx context.Context, email, sessionID string, name *string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	const q = `
		UPDATE sessions
		SET name = $4, updated_at = now()
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID, name)
	return err
}

// OwnerForSession returns the owner email associated with the given
// session id in this scope, or empty when no such session is registered.
// Used by the lifecycleEmitter to resolve which per-owner SSE subject a
// chat-derived activity delta should land on — the chat event payload
// itself carries only `session_id`, not the email, since `tank_session_id`
// is the durable routing key on the event bus.
func (s *Store) OwnerForSession(ctx context.Context, scope, sessionID string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	const q = `
		SELECT email
		FROM sessions
		WHERE session_scope = $1 AND session_id = $2
		LIMIT 1
	`
	var email string
	switch err := s.pool.QueryRow(ctx, q, scope, sessionID).Scan(&email); {
	case err == nil:
		return strings.ToLower(strings.TrimSpace(email)), nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	default:
		return "", err
	}
}

// MarkDeleted sets visible=false. Missing-session is a no-op.
func (s *Store) MarkDeleted(ctx context.Context, email, sessionID string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	const q = `
		UPDATE sessions
		SET visible = false, updated_at = now()
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID)
	return err
}

func parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func nullableTimestamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
