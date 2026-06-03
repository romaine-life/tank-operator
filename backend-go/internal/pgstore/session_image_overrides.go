package pgstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionImageOverride is a durable, session-scope-keyed override of the
// container images the orchestrator stamps onto NEW session pods.
//
// It exists so a Glimmung test slot can be pointed at a branch-built session
// image without a full chart rollout: the slot's orchestrator reads the row
// for its own scope at session-create time and stamps the override instead of
// the chart-pinned SESSION_IMAGE / CODEX_SESSION_IMAGE. Production (the
// "default" scope) is never overridden — the write path refuses it and the
// resolver is only wired in test-env orchestrators.
//
// Empty ClaudeImage / CodexImage mean "no override for that runner family —
// fall back to the orchestrator's configured image for that family." A repoint
// may set one or both.
type SessionImageOverride struct {
	SessionScope string
	ClaudeImage  string
	CodexImage   string
	GitRef       string
	SetBy        string
	SetAt        time.Time
}

// ErrSessionImageOverrideNotFound is returned by Get when no row exists for the
// scope. Callers treat this as "no override" (fall back to the pinned image),
// not as an error.
var ErrSessionImageOverrideNotFound = errors.New("session image override not found")

// SessionImageOverrideStore is the durable store behind the test-slot
// session-image repoint flow. Backed by the session_image_overrides table
// (migration 0086).
type SessionImageOverrideStore struct {
	pool *pgxpool.Pool
}

func NewSessionImageOverrideStore(pool *pgxpool.Pool) *SessionImageOverrideStore {
	return &SessionImageOverrideStore{pool: pool}
}

// Get returns the override for a scope, or ErrSessionImageOverrideNotFound when
// no row exists.
func (s *SessionImageOverrideStore) Get(ctx context.Context, scope string) (SessionImageOverride, error) {
	const q = `
		SELECT session_scope,
			COALESCE(claude_image, ''),
			COALESCE(codex_image, ''),
			COALESCE(git_ref, ''),
			COALESCE(set_by, ''),
			set_at
		FROM session_image_overrides
		WHERE session_scope = $1
	`
	var o SessionImageOverride
	err := s.pool.QueryRow(ctx, q, strings.TrimSpace(scope)).Scan(
		&o.SessionScope,
		&o.ClaudeImage,
		&o.CodexImage,
		&o.GitRef,
		&o.SetBy,
		&o.SetAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SessionImageOverride{}, ErrSessionImageOverrideNotFound
		}
		return SessionImageOverride{}, err
	}
	return o, nil
}

// Upsert sets the override for a scope. Empty image / git_ref strings are
// stored as NULL so a repoint that sets only one runner family doesn't clobber
// the other.
func (s *SessionImageOverrideStore) Upsert(ctx context.Context, o SessionImageOverride) error {
	const q = `
		INSERT INTO session_image_overrides (
			session_scope, claude_image, codex_image, git_ref, set_by, set_at
		)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), now())
		ON CONFLICT (session_scope) DO UPDATE SET
			claude_image = EXCLUDED.claude_image,
			codex_image  = EXCLUDED.codex_image,
			git_ref      = EXCLUDED.git_ref,
			set_by       = EXCLUDED.set_by,
			set_at       = now()
	`
	_, err := s.pool.Exec(
		ctx,
		q,
		strings.TrimSpace(o.SessionScope),
		strings.TrimSpace(o.ClaudeImage),
		strings.TrimSpace(o.CodexImage),
		strings.TrimSpace(o.GitRef),
		strings.TrimSpace(o.SetBy),
	)
	return err
}

// Delete removes the override for a scope. Returns true when a row was removed.
func (s *SessionImageOverrideStore) Delete(ctx context.Context, scope string) (bool, error) {
	tag, err := s.pool.Exec(
		ctx,
		`DELETE FROM session_image_overrides WHERE session_scope = $1`,
		strings.TrimSpace(scope),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
