package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// SessionTurnStore reads the durable per-session turn numbering written by the
// tank_session_events_allocate_turn_number trigger (pgstore migrations
// 0086-0093). turn_id (turn_<nonce>) stays the provider-neutral identity the
// rest of the system keys on; turn_number is the human-facing,
// submission-ordered handle the public route /sessions/{id}/turns/{n} resolves
// into. Resolution is always server-side and durable: per the transcript
// navigation contract the browser never maps a number to a turn_id from render
// state, so this store — not the loaded transcript window — is the authority.
type SessionTurnStore interface {
	// ResolveTurnNumber maps a per-session turn number to its durable turn_id
	// plus an encoded transcript row cursor to anchor a cold deep link. ok is
	// false when the session has no turn with that number.
	ResolveTurnNumber(ctx context.Context, tankSessionID string, number int64) (TurnNumberResolution, bool, error)
	// TurnNumberForTurnID returns the number assigned to a turn_id, if any.
	TurnNumberForTurnID(ctx context.Context, tankSessionID, turnID string) (int64, bool, error)
	// TurnNumbersForSession returns the turn_id -> turn_number map for a
	// session, used to stamp turnNumber into the transcript projection.
	TurnNumbersForSession(ctx context.Context, tankSessionID string) (map[string]int64, error)
}

// TurnNumberResolution is the durable answer to "what does turn N point to".
type TurnNumberResolution struct {
	TurnID     string
	TurnNumber int64
	// RowCursor is the encoded transcript row cursor for the turn's activity
	// shell, suitable for an anchored /timeline read. Empty when the turn has
	// not yet been materialized into a transcript row.
	RowCursor string
}

type sessionTurnStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewPostgresSessionTurnStore(pool *pgxpool.Pool, scope string) SessionTurnStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &sessionTurnStore{pool: pool, scope: scope}
}

func (s *sessionTurnStore) ResolveTurnNumber(ctx context.Context, tankSessionID string, number int64) (TurnNumberResolution, bool, error) {
	if number < 1 {
		return TurnNumberResolution{}, false, nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT t.turn_id,
			t.turn_number,
			COALESCE((
				SELECT r.row_cursor
				FROM session_transcript_rows r
				WHERE r.tank_session_id = t.tank_session_id
					AND r.turn_id = t.turn_id
					AND r.row_kind = 'turn_activity'
				ORDER BY r.row_cursor DESC
				LIMIT 1
			), '') AS row_cursor
		FROM session_turns t
		WHERE t.tank_session_id = $1 AND t.turn_number = $2
	`
	var res TurnNumberResolution
	var rawCursor string
	if err := s.pool.QueryRow(ctx, q, storageKey, number).Scan(&res.TurnID, &res.TurnNumber, &rawCursor); err != nil {
		if err == pgx.ErrNoRows {
			return TurnNumberResolution{}, false, nil
		}
		return TurnNumberResolution{}, false, err
	}
	if strings.TrimSpace(rawCursor) != "" {
		res.RowCursor = EncodeTranscriptRowCursor(rawCursor)
	}
	return res, true, nil
}

func (s *sessionTurnStore) TurnNumberForTurnID(ctx context.Context, tankSessionID, turnID string) (int64, bool, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return 0, false, nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT turn_number
		FROM session_turns
		WHERE tank_session_id = $1 AND turn_id = $2
	`
	var number int64
	if err := s.pool.QueryRow(ctx, q, storageKey, turnID).Scan(&number); err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return number, true, nil
}

func (s *sessionTurnStore) TurnNumbersForSession(ctx context.Context, tankSessionID string) (map[string]int64, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT turn_id, turn_number
		FROM session_turns
		WHERE tank_session_id = $1
	`
	rows, err := s.pool.Query(ctx, q, storageKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var turnID string
		var number int64
		if err := rows.Scan(&turnID, &number); err != nil {
			return nil, err
		}
		out[turnID] = number
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// StubSessionTurnStore is the no-Postgres dev/degraded-mode implementation: no
// durable numbering exists, so every resolution misses and the projection
// stamps no number.
type StubSessionTurnStore struct{}

func (StubSessionTurnStore) ResolveTurnNumber(context.Context, string, int64) (TurnNumberResolution, bool, error) {
	return TurnNumberResolution{}, false, nil
}

func (StubSessionTurnStore) TurnNumberForTurnID(context.Context, string, string) (int64, bool, error) {
	return 0, false, nil
}

func (StubSessionTurnStore) TurnNumbersForSession(context.Context, string) (map[string]int64, error) {
	return map[string]int64{}, nil
}
