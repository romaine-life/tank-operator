package sessionregistry

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

type AvatarDeckSnapshot struct {
	Kind    string
	Cycle   int64
	Entries []AvatarDeckEntry
}

type AvatarDeckEntry struct {
	Position      int
	AvatarID      string
	Name          string
	Used          bool
	UsedSessionID string
	UsedAt        string
	Available     bool
}

// AssignSessionAvatars pins any missing avatar IDs on a session row by drawing
// from the owner/scope/kind shuffled deck. Existing assignments are returned
// unchanged so the operation is idempotent for retrying create paths.
func (s *Store) AssignSessionAvatars(ctx context.Context, owner, sessionID string) (sessionmodel.SessionAvatarAssignment, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return sessionmodel.SessionAvatarAssignment{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const rowQ = `
		SELECT COALESCE(agent_avatar_id, ''), COALESCE(system_avatar_id, '')
		FROM sessions
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
		FOR UPDATE
	`
	var assignment sessionmodel.SessionAvatarAssignment
	if err := tx.QueryRow(ctx, rowQ, normalized, s.scope, sessionID).Scan(
		&assignment.AgentAvatarID,
		&assignment.SystemAvatarID,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sessionmodel.SessionAvatarAssignment{}, nil
		}
		return sessionmodel.SessionAvatarAssignment{}, err
	}

	changed := false
	if assignment.AgentAvatarID == "" {
		id, err := s.drawAvatarFromDeck(ctx, tx, normalized, avatarassets.KindAgent, sessionID)
		if err != nil {
			return sessionmodel.SessionAvatarAssignment{}, err
		}
		assignment.AgentAvatarID = id
		changed = changed || id != ""
	}
	if assignment.SystemAvatarID == "" {
		id, err := s.drawAvatarFromDeck(ctx, tx, normalized, avatarassets.KindSystem, sessionID)
		if err != nil {
			return sessionmodel.SessionAvatarAssignment{}, err
		}
		assignment.SystemAvatarID = id
		changed = changed || id != ""
	}
	if changed {
		const updateQ = `
			UPDATE sessions
			SET agent_avatar_id = COALESCE(NULLIF($4, ''), agent_avatar_id),
				system_avatar_id = COALESCE(NULLIF($5, ''), system_avatar_id),
				updated_at = now(),
				row_version = nextval('sessions_row_version_seq')
			WHERE email = $1 AND session_scope = $2 AND session_id = $3
		`
		if _, err := tx.Exec(ctx, updateQ, normalized, s.scope, sessionID, assignment.AgentAvatarID, assignment.SystemAvatarID); err != nil {
			return sessionmodel.SessionAvatarAssignment{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	return assignment, nil
}

// ReserveSessionAvatars draws the avatar IDs for a session before the session
// row is first made visible. Create paths use this to avoid exposing a
// visible row with empty avatar IDs between the registry insert and the later
// idempotent AssignSessionAvatars update.
func (s *Store) ReserveSessionAvatars(ctx context.Context, owner, sessionID string) (sessionmodel.SessionAvatarAssignment, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return sessionmodel.SessionAvatarAssignment{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	agentID, err := s.drawAvatarFromDeck(ctx, tx, normalized, avatarassets.KindAgent, sessionID)
	if err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	systemID, err := s.drawAvatarFromDeck(ctx, tx, normalized, avatarassets.KindSystem, sessionID)
	if err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sessionmodel.SessionAvatarAssignment{}, err
	}
	return sessionmodel.SessionAvatarAssignment{
		AgentAvatarID:  agentID,
		SystemAvatarID: systemID,
	}, nil
}

func (s *Store) drawAvatarFromDeck(ctx context.Context, tx pgx.Tx, owner, kind, sessionID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, avatarDeckLockKey(owner, s.scope, kind)); err != nil {
		return "", err
	}

	cycle, err := currentAvatarDeckCycle(ctx, tx, owner, s.scope, kind)
	if err != nil {
		return "", err
	}
	if cycle > 0 {
		id, ok, err := useNextAvatarDeckEntry(ctx, tx, owner, s.scope, kind, cycle, sessionID)
		if err != nil || ok {
			return id, err
		}
	}

	ids, err := activeAvatarIDs(ctx, tx, kind)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	shuffleAvatarIDs(ids)
	cycle++
	const insertQ = `
		INSERT INTO avatar_deck_entries (
			email, session_scope, kind, cycle, position, avatar_id
		) VALUES ($1, $2, $3, $4, $5, $6)
	`
	for index, id := range ids {
		if _, err := tx.Exec(ctx, insertQ, owner, s.scope, kind, cycle, index+1, id); err != nil {
			return "", err
		}
	}
	id, _, err := useNextAvatarDeckEntry(ctx, tx, owner, s.scope, kind, cycle, sessionID)
	return id, err
}

func currentAvatarDeckCycle(ctx context.Context, tx pgx.Tx, owner, scope, kind string) (int64, error) {
	const q = `
		SELECT COALESCE(MAX(cycle), 0)
		FROM avatar_deck_entries
		WHERE email = $1 AND session_scope = $2 AND kind = $3
	`
	var cycle int64
	err := tx.QueryRow(ctx, q, owner, scope, kind).Scan(&cycle)
	return cycle, err
}

func useNextAvatarDeckEntry(ctx context.Context, tx pgx.Tx, owner, scope, kind string, cycle int64, sessionID string) (string, bool, error) {
	const selectQ = `
		SELECT e.position, e.avatar_id
		FROM avatar_deck_entries AS e
		JOIN avatar_assets AS a ON a.id = e.avatar_id AND a.deleted_at IS NULL
		WHERE e.email = $1
		  AND e.session_scope = $2
		  AND e.kind = $3
		  AND e.cycle = $4
		  AND e.used_session_id IS NULL
		ORDER BY e.position ASC
		LIMIT 1
		FOR UPDATE OF e
	`
	var position int
	var avatarID string
	if err := tx.QueryRow(ctx, selectQ, owner, scope, kind, cycle).Scan(&position, &avatarID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	const updateQ = `
		UPDATE avatar_deck_entries
		SET used_session_id = $6,
			used_at = now()
		WHERE email = $1 AND session_scope = $2 AND kind = $3 AND cycle = $4 AND position = $5
	`
	if _, err := tx.Exec(ctx, updateQ, owner, scope, kind, cycle, position, sessionID); err != nil {
		return "", false, err
	}
	return avatarID, true, nil
}

func activeAvatarIDs(ctx context.Context, tx pgx.Tx, kind string) ([]string, error) {
	const q = `
		SELECT id
		FROM avatar_assets
		WHERE kind = $1 AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC
	`
	rows, err := tx.Query(ctx, q, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func shuffleAvatarIDs(ids []string) {
	for i := len(ids) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			j := fallbackShuffleIndex(ids, i)
			ids[i], ids[j] = ids[j], ids[i]
			continue
		}
		j := int(n.Int64())
		ids[i], ids[j] = ids[j], ids[i]
	}
}

func fallbackShuffleIndex(ids []string, i int) int {
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(i))
	_, _ = h.Write(buf[:])
	return int(h.Sum64() % uint64(i+1))
}

func avatarDeckLockKey(owner, scope, kind string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("avatar-deck\x00"))
	_, _ = h.Write([]byte(owner))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(scope))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	return int64(h.Sum64())
}

// CurrentAvatarDecks returns the latest deck cycle for each avatar kind. It
// never creates a cycle; a deck appears after the first assignment for that
// owner/scope/kind.
func (s *Store) CurrentAvatarDecks(ctx context.Context, owner string) ([]AvatarDeckSnapshot, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" {
		return nil, nil
	}
	decks := make([]AvatarDeckSnapshot, 0, 2)
	for _, kind := range []string{avatarassets.KindAgent, avatarassets.KindSystem} {
		deck, err := s.currentAvatarDeck(ctx, normalized, kind)
		if err != nil {
			return nil, err
		}
		decks = append(decks, deck)
	}
	return decks, nil
}

func (s *Store) currentAvatarDeck(ctx context.Context, owner, kind string) (AvatarDeckSnapshot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AvatarDeckSnapshot{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	cycle, err := currentAvatarDeckCycle(ctx, tx, owner, s.scope, kind)
	if err != nil {
		return AvatarDeckSnapshot{}, err
	}
	if cycle == 0 {
		return AvatarDeckSnapshot{Kind: kind}, tx.Commit(ctx)
	}
	const q = `
		SELECT e.position,
			e.avatar_id,
			COALESCE(a.name, e.avatar_id),
			e.used_session_id IS NOT NULL,
			COALESCE(e.used_session_id, ''),
			COALESCE(to_char(e.used_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
			(a.id IS NOT NULL AND a.deleted_at IS NULL)
		FROM avatar_deck_entries AS e
		LEFT JOIN avatar_assets AS a ON a.id = e.avatar_id
		WHERE e.email = $1
		  AND e.session_scope = $2
		  AND e.kind = $3
		  AND e.cycle = $4
		ORDER BY e.position ASC
	`
	rows, err := tx.Query(ctx, q, owner, s.scope, kind, cycle)
	if err != nil {
		return AvatarDeckSnapshot{}, err
	}
	deck := AvatarDeckSnapshot{Kind: kind, Cycle: cycle}
	for rows.Next() {
		var entry AvatarDeckEntry
		if err := rows.Scan(
			&entry.Position,
			&entry.AvatarID,
			&entry.Name,
			&entry.Used,
			&entry.UsedSessionID,
			&entry.UsedAt,
			&entry.Available,
		); err != nil {
			return AvatarDeckSnapshot{}, err
		}
		deck.Entries = append(deck.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return AvatarDeckSnapshot{}, err
	}
	rows.Close()
	return deck, tx.Commit(ctx)
}
