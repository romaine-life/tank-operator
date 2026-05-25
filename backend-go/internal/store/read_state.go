package store

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConversationReadStateStore persists the per-user live-event cursor for a
// Tank conversation. The cursor is a session_events order_key, not the
// transcript-row cursor used for historical /timeline pagination.
type ConversationReadStateStore interface {
	Get(ctx context.Context, email, sessionID string) (*ConversationReadStateRecord, error)
	Set(ctx context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error)
}

type ConversationReadStateRecord struct {
	Email            string `json:"email"`
	SessionID        string `json:"session_id"`
	LastReadOrderKey string `json:"last_read_order_key"`
	UpdatedAt        string `json:"updated_at"`
}

type postgresConversationReadStateStore struct {
	pool  *pgxpool.Pool
	scope string
}

// NewPostgresConversationReadStateStore returns a read-state store backed by
// the conversation_read_state table. `scope` is the session scope (e.g.
// "default" or "slot-a"); rows live in a composite primary key keyed by
// (email, session_scope, session_id) so the same email can have independent
// cursors per scope.
func NewPostgresConversationReadStateStore(pool *pgxpool.Pool, scope string) ConversationReadStateStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &postgresConversationReadStateStore{pool: pool, scope: scope}
}

func (s *postgresConversationReadStateStore) Get(ctx context.Context, email, sessionID string) (*ConversationReadStateRecord, error) {
	normalized := normalizeReadStateEmail(email)
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return nil, nil
	}
	const q = `
		SELECT
			last_read_order_key,
			COALESCE(to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at
		FROM conversation_read_state
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	var (
		lastReadOrderKey string
		updatedAt        string
	)
	err := s.pool.QueryRow(ctx, q, normalized, s.scope, sessionID).Scan(&lastReadOrderKey, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ConversationReadStateRecord{
		Email:            normalized,
		SessionID:        sessionID,
		LastReadOrderKey: lastReadOrderKey,
		UpdatedAt:        updatedAt,
	}, nil
}

// Set advances the cursor monotonically. If the supplied key is not strictly
// greater than the stored value, the row is left unchanged and the stored
// record is returned (matching the Cosmos impl's idempotent behavior). The
// comparison is done atomically inside the UPSERT — no read-then-write race.
func (s *postgresConversationReadStateStore) Set(ctx context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error) {
	normalized := normalizeReadStateEmail(email)
	sessionID = strings.TrimSpace(sessionID)
	lastReadOrderKey = strings.TrimSpace(lastReadOrderKey)
	if normalized == "" || sessionID == "" || lastReadOrderKey == "" {
		return ConversationReadStateRecord{}, nil
	}

	// GREATEST() picks the higher cursor; updated_at only advances when the
	// stored cursor was actually replaced. RETURNING gives us the post-update
	// state in one round-trip.
	const q = `
		INSERT INTO conversation_read_state (
			email, session_scope, session_id, last_read_order_key, updated_at
		) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (email, session_scope, session_id) DO UPDATE
		SET last_read_order_key = GREATEST(
				EXCLUDED.last_read_order_key,
				conversation_read_state.last_read_order_key
			),
			updated_at = CASE
				WHEN EXCLUDED.last_read_order_key > conversation_read_state.last_read_order_key
				THEN now()
				ELSE conversation_read_state.updated_at
			END
		RETURNING
			last_read_order_key,
			COALESCE(to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at
	`
	var (
		storedKey   string
		storedStamp string
	)
	if err := s.pool.QueryRow(ctx, q, normalized, s.scope, sessionID, lastReadOrderKey).
		Scan(&storedKey, &storedStamp); err != nil {
		return ConversationReadStateRecord{}, err
	}
	return ConversationReadStateRecord{
		Email:            normalized,
		SessionID:        sessionID,
		LastReadOrderKey: storedKey,
		UpdatedAt:        storedStamp,
	}, nil
}

type StubConversationReadStateStore struct {
	mu      sync.Mutex
	records map[string]ConversationReadStateRecord
}

func NewStubConversationReadStateStore() *StubConversationReadStateStore {
	return &StubConversationReadStateStore{records: map[string]ConversationReadStateRecord{}}
}

func (s *StubConversationReadStateStore) Get(_ context.Context, email, sessionID string) (*ConversationReadStateRecord, error) {
	if s == nil {
		return nil, nil
	}
	key, _, _ := readStateMemoryKey(email, sessionID)
	if key == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[key]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *StubConversationReadStateStore) Set(_ context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error) {
	if s == nil {
		return ConversationReadStateRecord{}, nil
	}
	key, normalized, trimmedSessionID := readStateMemoryKey(email, sessionID)
	lastReadOrderKey = strings.TrimSpace(lastReadOrderKey)
	if key == "" || lastReadOrderKey == "" {
		return ConversationReadStateRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok && existing.LastReadOrderKey >= lastReadOrderKey {
		return existing, nil
	}
	rec := ConversationReadStateRecord{
		Email:            normalized,
		SessionID:        trimmedSessionID,
		LastReadOrderKey: lastReadOrderKey,
		UpdatedAt:        nowISO(),
	}
	s.records[key] = rec
	return rec, nil
}

func readStateMemoryKey(email, sessionID string) (string, string, string) {
	normalized := normalizeReadStateEmail(email)
	trimmedSessionID := strings.TrimSpace(sessionID)
	if normalized == "" || trimmedSessionID == "" {
		return "", "", ""
	}
	return normalized + "\x1f" + trimmedSessionID, normalized, trimmedSessionID
}

func normalizeReadStateEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
