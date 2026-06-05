package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStaticPageSnapshotNotFound is returned when there is no live (unexpired)
// snapshot for the requested session + path.
var ErrStaticPageSnapshotNotFound = errors.New("static page snapshot not found")

// StaticPageSnapshot is a durable, time-boxed copy of an agent-authored HTML
// file from a session workspace. The session pod is ephemeral; capturing the
// bytes lets the rendered page (and a future shareable link) outlive the pod
// for the snapshot's TTL. Keyed by (scope, session, path).
type StaticPageSnapshot struct {
	SessionScope string
	SessionID    string
	RelPath      string
	OwnerEmail   string
	ContentType  string
	Bytes        []byte
	ByteSize     int
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type StaticPageSnapshotStore struct {
	pool *pgxpool.Pool
}

func NewStaticPageSnapshotStore(pool *pgxpool.Pool) *StaticPageSnapshotStore {
	return &StaticPageSnapshotStore{pool: pool}
}

// Upsert captures (or recaptures) a snapshot keyed by (scope, session, path).
// Re-opening a page overwrites the bytes and resets the TTL. Expired rows are
// opportunistically swept in the same transaction so the table self-cleans
// without a background loop (same idiom as stream_auth_tickets).
func (s *StaticPageSnapshotStore) Upsert(ctx context.Context, snap StaticPageSnapshot) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("static page snapshot store not configured")
	}
	snap.SessionScope = strings.TrimSpace(snap.SessionScope)
	snap.SessionID = strings.TrimSpace(snap.SessionID)
	snap.RelPath = strings.TrimSpace(snap.RelPath)
	snap.OwnerEmail = strings.ToLower(strings.TrimSpace(snap.OwnerEmail))
	snap.ContentType = strings.TrimSpace(snap.ContentType)
	if snap.SessionScope == "" || snap.SessionID == "" || snap.RelPath == "" || snap.OwnerEmail == "" {
		return fmt.Errorf("static page snapshot: scope, session, path, and owner are required")
	}
	if snap.ContentType == "" {
		snap.ContentType = "text/html; charset=utf-8"
	}
	if snap.ExpiresAt.IsZero() {
		return fmt.Errorf("static page snapshot: expires_at is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("static page snapshot: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, `DELETE FROM static_page_snapshots WHERE expires_at < now()`)
	_, err = tx.Exec(ctx, `
		INSERT INTO static_page_snapshots (
			session_scope, session_id, rel_path, owner_email,
			content_type, bytes, byte_size, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8)
		ON CONFLICT (session_scope, session_id, rel_path) DO UPDATE SET
			owner_email  = EXCLUDED.owner_email,
			content_type = EXCLUDED.content_type,
			bytes        = EXCLUDED.bytes,
			byte_size    = EXCLUDED.byte_size,
			created_at   = now(),
			expires_at   = EXCLUDED.expires_at
	`, snap.SessionScope, snap.SessionID, snap.RelPath, snap.OwnerEmail,
		snap.ContentType, snap.Bytes, snap.ByteSize, snap.ExpiresAt)
	if err != nil {
		return fmt.Errorf("static page snapshot: upsert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("static page snapshot: commit: %w", err)
	}
	return nil
}

// Get returns the live snapshot for (scope, session, path), or
// ErrStaticPageSnapshotNotFound if none exists or it has expired. It does not
// touch the session pod — the snapshot is the durable, pod-independent copy.
func (s *StaticPageSnapshotStore) Get(ctx context.Context, scope, sessionID, relPath string) (StaticPageSnapshot, error) {
	if s == nil || s.pool == nil {
		return StaticPageSnapshot{}, fmt.Errorf("static page snapshot store not configured")
	}
	scope = strings.TrimSpace(scope)
	sessionID = strings.TrimSpace(sessionID)
	relPath = strings.TrimSpace(relPath)
	if scope == "" || sessionID == "" || relPath == "" {
		return StaticPageSnapshot{}, ErrStaticPageSnapshotNotFound
	}

	snap := StaticPageSnapshot{SessionScope: scope, SessionID: sessionID, RelPath: relPath}
	err := s.pool.QueryRow(ctx, `
		SELECT owner_email, content_type, bytes, byte_size, created_at, expires_at
		FROM static_page_snapshots
		WHERE session_scope = $1 AND session_id = $2 AND rel_path = $3
		  AND expires_at > now()
	`, scope, sessionID, relPath).Scan(
		&snap.OwnerEmail, &snap.ContentType, &snap.Bytes, &snap.ByteSize,
		&snap.CreatedAt, &snap.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaticPageSnapshot{}, ErrStaticPageSnapshotNotFound
		}
		return StaticPageSnapshot{}, fmt.Errorf("static page snapshot: get: %w", err)
	}
	return snap, nil
}
