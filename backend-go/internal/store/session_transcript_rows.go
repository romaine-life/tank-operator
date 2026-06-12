package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const transcriptRowCursorSeparator = "\x1f"

// Bumped 9 -> 10 so every session re-backfills transcript rows after the
// projection stopped annihilating parked turns: continuation (parked-origin)
// turns keep their turn_activity shells — restoring their stamped turn
// numbers and giving their compacted content (the acks session 161 lost) a
// durable home — and folded wake bodies merge chronologically into the
// originating turn's shell.
const transcriptRowBackfillVersion = 10

type SessionTranscriptRowStore interface {
	ReplaceForTurn(ctx context.Context, tankSessionID, turnID string, entries []map[string]any) error
	ReplaceForSession(ctx context.Context, tankSessionID string, entries []map[string]any) error
	UpsertRows(ctx context.Context, tankSessionID string, entries []map[string]any) error
	ListChangedAfterOrderKey(ctx context.Context, tankSessionID, afterOrderKey string, rows int) (TranscriptRowDeltaPage, error)
	ListLatest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error)
	ListOldest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error)
	ListBefore(ctx context.Context, tankSessionID, beforeCursor string, rows int) (TranscriptRowPage, error)
	ListAround(ctx context.Context, tankSessionID, rowCursor string, rowsBefore, rowsAfter int) (TranscriptRowPage, error)
	ResolveCursorForTimelineID(ctx context.Context, tankSessionID, timelineID string) (string, error)
	NeedsBackfill(ctx context.Context, tankSessionID string) (bool, error)
	// MaxEndOrderKey returns the projection's high-water mark: the largest
	// end_order_key across the session's materialized rows ('' when the
	// session has no rows). It is what /timeline responses mint as
	// live_order_key — the SSE resume cursor — instead of the raw event
	// ledger's tail: projection is async (#1056), so a cursor minted from
	// the ledger can sit AHEAD of the rows, and the SSE delta's strict
	// end_order_key > cursor filter would then make the not-yet-projected
	// rows permanently undeliverable to that stream (a turn terminal caught
	// in the upsert→refresh window rendered the turn perpetually active
	// until manual reload — issue #1077 item 2). Minting from the rows
	// flips the error direction to harmless duplicates: rows replayed on
	// the stream are idempotent replace-by-id upserts in the SPA.
	MaxEndOrderKey(ctx context.Context, tankSessionID string) (string, error)
	// RewriteEpoch returns the session's wholesale-rewrite epoch (bumped by
	// every ReplaceForSession-class rewrite; 0 before the first). Open SSE
	// streams watch it to detect row deletions they cannot otherwise see
	// (issue #1077 item 4).
	RewriteEpoch(ctx context.Context, tankSessionID string) (int64, error)
}

type TranscriptRowPage struct {
	Rows        []map[string]any
	PrevCursor  string
	NextCursor  string
	FoundOldest bool
	FoundNewest bool
}

type TranscriptRowDelta struct {
	Row       map[string]any
	OrderKey  string
	UpdatedAt time.Time
}

type TranscriptRowDeltaPage struct {
	Rows         []TranscriptRowDelta
	NextOrderKey string
	HasMore      bool
}

type transcriptRowStore struct {
	pool  *pgxpool.Pool
	scope string
}

type transcriptRowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewPostgresSessionTranscriptRowStore(pool *pgxpool.Pool, scope string) SessionTranscriptRowStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &transcriptRowStore{pool: pool, scope: scope}
}

func (s *transcriptRowStore) ReplaceForTurn(ctx context.Context, tankSessionID, turnID string, entries []map[string]any) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.replaceForTurnTx(ctx, tx, storageKey, turnID, entries); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *transcriptRowStore) ReplaceForSession(ctx context.Context, tankSessionID string, entries []map[string]any) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.replaceForSessionTx(ctx, tx, storageKey, entries); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *transcriptRowStore) UpsertRows(ctx context.Context, tankSessionID string, entries []map[string]any) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.upsertRowsTx(ctx, tx, storageKey, entries); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MaxEndOrderKey rides the session_transcript_rows_end_order index
// ((tank_session_id, end_order_key, row_cursor) — DESC LIMIT 1).
func (s *transcriptRowStore) MaxEndOrderKey(ctx context.Context, tankSessionID string) (string, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	var key string
	err := s.pool.QueryRow(ctx, `
		SELECT end_order_key FROM session_transcript_rows
		WHERE tank_session_id = $1
		ORDER BY end_order_key DESC
		LIMIT 1
	`, storageKey).Scan(&key)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *transcriptRowStore) ListChangedAfterOrderKey(ctx context.Context, tankSessionID, afterOrderKey string, rows int) (TranscriptRowDeltaPage, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rows = normalizeTranscriptRowLimit(rows)
	q := `
		WITH changed_order_keys AS (
			SELECT DISTINCT end_order_key
			FROM session_transcript_rows
			WHERE tank_session_id = $1
				AND end_order_key > $2
			ORDER BY end_order_key ASC
			LIMIT $3
		)
		SELECT r.payload, r.end_order_key, r.updated_at
		FROM session_transcript_rows r
		JOIN changed_order_keys k ON k.end_order_key = r.end_order_key
		WHERE r.tank_session_id = $1
		ORDER BY r.end_order_key ASC, r.row_cursor ASC
	`
	return s.fetchRowDeltas(ctx, q, []any{storageKey, strings.TrimSpace(afterOrderKey), rows + 1}, rows)
}

func (s *transcriptRowStore) WithTranscriptMaterializationTx(ctx context.Context, tankSessionID string, fn func(context.Context, pgx.Tx) error) error {
	if fn == nil {
		return nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.lockTranscriptMaterializationTx(ctx, tx, storageKey); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *transcriptRowStore) ReplaceForTurnTx(ctx context.Context, tx pgx.Tx, tankSessionID, turnID string, entries []map[string]any) error {
	return s.replaceForTurnTx(ctx, tx, sessionmodel.SessionStorageKey(s.scope, tankSessionID), turnID, entries)
}

func (s *transcriptRowStore) ReplaceForSessionTx(ctx context.Context, tx pgx.Tx, tankSessionID string, entries []map[string]any) error {
	return s.replaceForSessionTx(ctx, tx, sessionmodel.SessionStorageKey(s.scope, tankSessionID), entries)
}

func (s *transcriptRowStore) UpsertRowsTx(ctx context.Context, tx pgx.Tx, tankSessionID string, entries []map[string]any) error {
	return s.upsertRowsTx(ctx, tx, sessionmodel.SessionStorageKey(s.scope, tankSessionID), entries)
}

func (s *transcriptRowStore) NeedsBackfill(ctx context.Context, tankSessionID string) (bool, error) {
	return s.needsBackfill(ctx, s.pool, tankSessionID)
}

func (s *transcriptRowStore) NeedsBackfillTx(ctx context.Context, tx pgx.Tx, tankSessionID string) (bool, error) {
	return s.needsBackfill(ctx, tx, tankSessionID)
}

func (s *transcriptRowStore) needsBackfill(ctx context.Context, q transcriptRowQueryer, tankSessionID string) (bool, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	var version int
	err := q.QueryRow(ctx, `
		SELECT projection_version
		FROM session_transcript_row_backfills
		WHERE tank_session_id = $1
	`, storageKey).Scan(&version)
	if err == pgx.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return version != transcriptRowBackfillVersion, nil
}

// LoadFoldStateTx reads the session's checkpointed fold memo (the shared
// session part only — per-turn entry sets load separately via
// LoadFoldTurnsTx) and whether the fold is durably disabled for this session.
// A missing row is (nil, false): the fold seeds on the next session-scope
// re-projection.
func (s *transcriptRowStore) LoadFoldStateTx(ctx context.Context, tx pgx.Tx, tankSessionID string) ([]byte, bool, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	var memo []byte
	var disabled bool
	err := tx.QueryRow(ctx, `
		SELECT memo, disabled
		FROM session_transcript_fold_state
		WHERE tank_session_id = $1
	`, storageKey).Scan(&memo, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return memo, disabled, nil
}

// LoadFoldTurnsTx reads the per-turn entry-set blobs for exactly the turns a
// fold batch touches. A turn with no row simply has no entries yet.
func (s *transcriptRowStore) LoadFoldTurnsTx(ctx context.Context, tx pgx.Tx, tankSessionID string, turnIDs []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(turnIDs) == 0 {
		return out, nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rows, err := tx.Query(ctx, `
		SELECT turn_id, entries
		FROM session_transcript_fold_turns
		WHERE tank_session_id = $1 AND turn_id = ANY($2::text[])
	`, storageKey, turnIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var turnID string
		var entries []byte
		if err := rows.Scan(&turnID, &entries); err != nil {
			return nil, err
		}
		out[turnID] = entries
	}
	return out, rows.Err()
}

// SaveFoldStateTx upserts the session part and exactly the touched turn
// blobs — the per-batch write cost is O(touched turns), never O(session).
func (s *transcriptRowStore) SaveFoldStateTx(ctx context.Context, tx pgx.Tx, tankSessionID string, memo []byte, turns map[string][]byte) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_transcript_fold_state (tank_session_id, memo, disabled, updated_at)
		VALUES ($1, $2, false, now())
		ON CONFLICT (tank_session_id) DO UPDATE
		SET memo = EXCLUDED.memo, disabled = false, updated_at = now()
	`, storageKey, memo); err != nil {
		return err
	}
	for turnID, entries := range turns {
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_transcript_fold_turns (tank_session_id, turn_id, entries, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (tank_session_id, turn_id) DO UPDATE
			SET entries = EXCLUDED.entries, updated_at = now()
		`, storageKey, turnID, entries); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceFoldStateTx is the reseed write: the session part plus the COMPLETE
// turn-blob set, with stale turn rows removed.
func (s *transcriptRowStore) ReplaceFoldStateTx(ctx context.Context, tx pgx.Tx, tankSessionID string, memo []byte, turns map[string][]byte) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	if _, err := tx.Exec(ctx, `
		DELETE FROM session_transcript_fold_turns WHERE tank_session_id = $1
	`, storageKey); err != nil {
		return err
	}
	return s.SaveFoldStateTx(ctx, tx, tankSessionID, memo, turns)
}

// DeleteFoldStateTx invalidates the memo: a turn-scope reference projection
// makes it stale, and the next session-scope projection reseeds it. A
// durably disabled session-part row is preserved.
func (s *transcriptRowStore) DeleteFoldStateTx(ctx context.Context, tx pgx.Tx, tankSessionID string) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	if _, err := tx.Exec(ctx, `
		DELETE FROM session_transcript_fold_turns WHERE tank_session_id = $1
	`, storageKey); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM session_transcript_fold_state
		WHERE tank_session_id = $1 AND disabled = false
	`, storageKey)
	return err
}

// DisableFoldTx durably opts the session out of the checkpointed fold (memo
// over the size cap). The session batch-projects from then on — exactly the
// pre-fold behavior — and the row pins the decision.
func (s *transcriptRowStore) DisableFoldTx(ctx context.Context, tx pgx.Tx, tankSessionID string) error {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	_, err := tx.Exec(ctx, `
		INSERT INTO session_transcript_fold_state (tank_session_id, memo, disabled, updated_at)
		VALUES ($1, NULL, true, now())
		ON CONFLICT (tank_session_id) DO UPDATE
		SET memo = NULL, disabled = true, updated_at = now()
	`, storageKey)
	return err
}

func (s *transcriptRowStore) lockTranscriptMaterializationTx(ctx context.Context, tx pgx.Tx, storageKey string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_transcript_materialization_locks (tank_session_id)
		VALUES ($1)
		ON CONFLICT (tank_session_id) DO NOTHING
	`, storageKey); err != nil {
		return err
	}
	var locked string
	return tx.QueryRow(ctx, `
		SELECT tank_session_id
		FROM session_transcript_materialization_locks
		WHERE tank_session_id = $1
		FOR UPDATE
	`, storageKey).Scan(&locked)
}

func (s *transcriptRowStore) replaceForTurnTx(ctx context.Context, tx pgx.Tx, storageKey, turnID string, entries []map[string]any) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM session_transcript_rows WHERE tank_session_id = $1 AND turn_id = $2`,
		storageKey,
		strings.TrimSpace(turnID),
	); err != nil {
		return err
	}
	return insertTranscriptRows(ctx, tx, storageKey, entries)
}

func (s *transcriptRowStore) replaceForSessionTx(ctx context.Context, tx pgx.Tx, storageKey string, entries []map[string]any) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM session_transcript_rows WHERE tank_session_id = $1`,
		storageKey,
	); err != nil {
		return err
	}
	if err := insertTranscriptRows(ctx, tx, storageKey, entries); err != nil {
		return err
	}
	// rewrite_epoch advances on every wholesale rewrite (issue #1077 item
	// 4): the row-delta SSE stream cannot express deletions, so open
	// streams watch this epoch and resync when it moves.
	_, err := tx.Exec(ctx, `
		INSERT INTO session_transcript_row_backfills (
			tank_session_id, projection_version, completed_at, rewrite_epoch
		) VALUES ($1, $2, now(), 1)
		ON CONFLICT (tank_session_id) DO UPDATE
		SET projection_version = EXCLUDED.projection_version,
			completed_at = now(),
			rewrite_epoch = session_transcript_row_backfills.rewrite_epoch + 1
	`, storageKey, transcriptRowBackfillVersion)
	return err
}

// RewriteEpoch returns the session's wholesale-rewrite epoch (0 when the
// session has never been backfilled/rewritten). One PK lookup; the SSE
// stream's ghost-row guard.
func (s *transcriptRowStore) RewriteEpoch(ctx context.Context, tankSessionID string) (int64, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	var epoch int64
	err := s.pool.QueryRow(ctx, `
		SELECT rewrite_epoch FROM session_transcript_row_backfills
		WHERE tank_session_id = $1
	`, storageKey).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

func (s *transcriptRowStore) upsertRowsTx(ctx context.Context, tx pgx.Tx, storageKey string, entries []map[string]any) error {
	return insertTranscriptRows(ctx, tx, storageKey, entries)
}

func (s *transcriptRowStore) ListLatest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rows = normalizeTranscriptRowLimit(rows)
	return s.listRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1
		ORDER BY row_cursor DESC
		LIMIT $2
	`, []any{storageKey, rows + 1}, rows, "desc", false, true)
}

func (s *transcriptRowStore) ListOldest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rows = normalizeTranscriptRowLimit(rows)
	return s.listRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1
		ORDER BY row_cursor ASC
		LIMIT $2
	`, []any{storageKey, rows + 1}, rows, "asc", true, false)
}

func (s *transcriptRowStore) ListBefore(ctx context.Context, tankSessionID, beforeCursor string, rows int) (TranscriptRowPage, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rawCursor, err := DecodeTranscriptRowCursor(beforeCursor)
	if err != nil {
		return TranscriptRowPage{}, err
	}
	rows = normalizeTranscriptRowLimit(rows)
	return s.listRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1 AND row_cursor < $2
		ORDER BY row_cursor DESC
		LIMIT $3
	`, []any{storageKey, rawCursor, rows + 1}, rows, "desc", false, false)
}

func (s *transcriptRowStore) ListAround(ctx context.Context, tankSessionID, rowCursor string, rowsBefore, rowsAfter int) (TranscriptRowPage, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	rawCursor, err := DecodeTranscriptRowCursor(rowCursor)
	if err != nil {
		return TranscriptRowPage{}, err
	}
	rowsBefore = normalizeTranscriptRowAroundHalf(rowsBefore)
	rowsAfter = normalizeTranscriptRowAroundHalf(rowsAfter)

	before, beforeCursors, foundOldest, err := s.fetchRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1 AND row_cursor < $2
		ORDER BY row_cursor DESC
		LIMIT $3
	`, storageKey, rawCursor, rowsBefore)
	if err != nil {
		return TranscriptRowPage{}, err
	}
	reverseRows(before, beforeCursors)

	target, targetCursors, _, err := s.fetchRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1 AND row_cursor = $2
		ORDER BY row_cursor ASC
		LIMIT $3
	`, storageKey, rawCursor, 1)
	if err != nil {
		return TranscriptRowPage{}, err
	}

	after, afterCursors, foundNewest, err := s.fetchRows(ctx, `
		SELECT payload, row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1 AND row_cursor > $2
		ORDER BY row_cursor ASC
		LIMIT $3
	`, storageKey, rawCursor, rowsAfter)
	if err != nil {
		return TranscriptRowPage{}, err
	}

	rows := append(append(before, target...), after...)
	cursors := append(append(beforeCursors, targetCursors...), afterCursors...)
	return transcriptRowPage(rows, cursors, foundOldest, foundNewest), nil
}

func (s *transcriptRowStore) ResolveCursorForTimelineID(ctx context.Context, tankSessionID, timelineID string) (string, error) {
	timelineID = strings.TrimSpace(timelineID)
	if timelineID == "" {
		return "", nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT row_cursor
		FROM session_transcript_rows
		WHERE tank_session_id = $1
			AND (row_id = $2 OR payload -> 'activityIds' ? $2)
		ORDER BY CASE WHEN row_id = $2 THEN 0 ELSE 1 END, row_cursor DESC
		LIMIT 1
	`
	var rawCursor string
	if err := s.pool.QueryRow(ctx, q, storageKey, timelineID).Scan(&rawCursor); err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return EncodeTranscriptRowCursor(rawCursor), nil
}

func (s *transcriptRowStore) listRows(ctx context.Context, query string, args []any, limit int, direction string, foundOldestAtStart, foundNewestAtStart bool) (TranscriptRowPage, error) {
	rows, cursors, foundEdge, err := s.fetchRowsRaw(ctx, query, args, limit)
	if err != nil {
		return TranscriptRowPage{}, err
	}
	if direction == "desc" {
		reverseRows(rows, cursors)
	}
	foundOldest := foundOldestAtStart
	foundNewest := foundNewestAtStart
	if foundOldestAtStart {
		foundNewest = foundEdge
	} else if foundNewestAtStart {
		foundOldest = foundEdge
	} else {
		foundOldest = foundEdge
	}
	return transcriptRowPage(rows, cursors, foundOldest, foundNewest), nil
}

func (s *transcriptRowStore) fetchRows(ctx context.Context, query, storageKey, cursor string, limit int) ([]map[string]any, []string, bool, error) {
	if limit < 1 {
		return []map[string]any{}, []string{}, false, nil
	}
	limit = normalizeTranscriptRowLimit(limit)
	return s.fetchRowsRaw(ctx, query, []any{storageKey, cursor, limit + 1}, limit)
}

func (s *transcriptRowStore) fetchRowsRaw(ctx context.Context, query string, args []any, limit int) ([]map[string]any, []string, bool, error) {
	dbRows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, false, err
	}
	defer dbRows.Close()
	out := make([]map[string]any, 0, limit+1)
	cursors := make([]string, 0, limit+1)
	for dbRows.Next() {
		var payload []byte
		var cursor string
		if err := dbRows.Scan(&payload, &cursor); err != nil {
			return nil, nil, false, err
		}
		var entry map[string]any
		if err := json.Unmarshal(payload, &entry); err != nil {
			return nil, nil, false, fmt.Errorf("transcript row payload is not JSON: %w", err)
		}
		out = append(out, entry)
		cursors = append(cursors, cursor)
	}
	if err := dbRows.Err(); err != nil {
		return nil, nil, false, err
	}
	foundEdge := len(out) <= limit
	if len(out) > limit {
		out = out[:limit]
		cursors = cursors[:limit]
	}
	return out, cursors, foundEdge, nil
}

func (s *transcriptRowStore) fetchRowDeltas(ctx context.Context, query string, args []any, limit int) (TranscriptRowDeltaPage, error) {
	if limit < 1 {
		return TranscriptRowDeltaPage{}, nil
	}
	dbRows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return TranscriptRowDeltaPage{}, err
	}
	defer dbRows.Close()
	out := make([]TranscriptRowDelta, 0, limit+1)
	orderKeys := make([]string, 0, limit+1)
	lastOrderKey := ""
	for dbRows.Next() {
		var payload []byte
		var orderKey string
		var updatedAt time.Time
		if err := dbRows.Scan(&payload, &orderKey, &updatedAt); err != nil {
			return TranscriptRowDeltaPage{}, err
		}
		var entry map[string]any
		if err := json.Unmarshal(payload, &entry); err != nil {
			return TranscriptRowDeltaPage{}, fmt.Errorf("transcript row payload is not JSON: %w", err)
		}
		out = append(out, TranscriptRowDelta{Row: entry, OrderKey: orderKey, UpdatedAt: updatedAt})
		if orderKey != lastOrderKey {
			orderKeys = append(orderKeys, orderKey)
			lastOrderKey = orderKey
		}
	}
	if err := dbRows.Err(); err != nil {
		return TranscriptRowDeltaPage{}, err
	}
	hasMore := len(orderKeys) > limit
	if hasMore {
		cutoff := orderKeys[limit-1]
		kept := out[:0]
		for _, delta := range out {
			if delta.OrderKey > cutoff {
				break
			}
			kept = append(kept, delta)
		}
		out = kept
	}
	nextOrderKey := ""
	if len(out) > 0 {
		nextOrderKey = out[len(out)-1].OrderKey
	}
	return TranscriptRowDeltaPage{
		Rows:         out,
		NextOrderKey: nextOrderKey,
		HasMore:      hasMore,
	}, nil
}

func insertTranscriptRows(ctx context.Context, tx pgx.Tx, storageKey string, entries []map[string]any) error {
	for _, entry := range entries {
		row, ok := transcriptRowFromEntry(entry)
		if !ok {
			continue
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_transcript_rows (
				tank_session_id, row_cursor, row_id, row_kind, turn_id,
				start_order_key, end_order_key, source_event_id, payload, updated_at
			) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,NULLIF($8,''),$9,now())
			ON CONFLICT (tank_session_id, row_id) DO UPDATE
			SET row_cursor = EXCLUDED.row_cursor,
				row_kind = EXCLUDED.row_kind,
				turn_id = EXCLUDED.turn_id,
				start_order_key = EXCLUDED.start_order_key,
				end_order_key = EXCLUDED.end_order_key,
				source_event_id = EXCLUDED.source_event_id,
				payload = EXCLUDED.payload,
				updated_at = now()
		`, storageKey, row.Cursor, row.ID, row.Kind, row.TurnID, row.StartOrderKey, row.EndOrderKey, row.SourceEventID, payload); err != nil {
			return err
		}
	}
	return nil
}

type transcriptRowRecord struct {
	ID            string
	Cursor        string
	Kind          string
	TurnID        string
	StartOrderKey string
	EndOrderKey   string
	SourceEventID string
}

func transcriptRowFromEntry(entry map[string]any) (transcriptRowRecord, bool) {
	id := transcriptRowString(entry, "id")
	kind := transcriptRowString(entry, "kind")
	orderKey := transcriptRowString(entry, "orderKey")
	if id == "" || kind == "" || orderKey == "" {
		return transcriptRowRecord{}, false
	}
	if isFoldableStartupSessionStatusRow(entry) {
		return transcriptRowRecord{}, false
	}
	startOrderKey := orderKey
	endOrderKey := orderKey
	if kind == "turn_activity" {
		if activity, ok := entry["activity"].(map[string]any); ok {
			if v := transcriptRowString(activity, "startOrderKey"); v != "" {
				startOrderKey = v
			}
			if v := transcriptRowString(activity, "endOrderKey"); v != "" {
				endOrderKey = v
			}
		}
	}
	if terminalOrderKey := transcriptRowString(entry, "turnTerminalOrderKey"); terminalOrderKey != "" && terminalOrderKey > endOrderKey {
		endOrderKey = terminalOrderKey
	}
	// contentOrderKey marks an in-place payload mutation (answered/dismissed
	// flips on awaiting cards — issue #1077 item 4). Lifting it here moves
	// the row past open SSE cursors so the flip actually DELIVERS; row
	// identity (row_id) and transcript position (start_order_key/row_cursor)
	// deliberately stay untouched.
	if contentOrderKey := transcriptRowString(entry, "contentOrderKey"); contentOrderKey != "" && contentOrderKey > endOrderKey {
		endOrderKey = contentOrderKey
	}
	return transcriptRowRecord{
		ID:            id,
		Cursor:        startOrderKey + transcriptRowCursorSeparator + id,
		Kind:          kind,
		TurnID:        transcriptRowString(entry, "turnId"),
		StartOrderKey: startOrderKey,
		EndOrderKey:   endOrderKey,
		SourceEventID: transcriptRowString(entry, "sourceEventId"),
	}, true
}

func isFoldableStartupSessionStatusRow(entry map[string]any) bool {
	if transcriptRowString(entry, "kind") != "message" ||
		transcriptRowString(entry, "role") != "system" {
		return false
	}
	status := transcriptRowString(entry, "sessionStatus")
	if status == "" {
		text := transcriptRowString(entry, "text")
		if text == "Session is loading." {
			status = "loading"
		} else if text == "Session is ready." {
			status = "ready"
		}
	}
	if status != "loading" && status != "ready" {
		return false
	}
	id := transcriptRowString(entry, "id")
	sourceEventID := transcriptRowString(entry, "sourceEventId")
	return !strings.Contains(id, ":provider:") && !strings.Contains(sourceEventID, ":provider:")
}

func transcriptRowString(entry map[string]any, key string) string {
	value, _ := entry[key].(string)
	return strings.TrimSpace(value)
}

func transcriptRowPage(rows []map[string]any, cursors []string, foundOldest, foundNewest bool) TranscriptRowPage {
	page := TranscriptRowPage{
		Rows:        rows,
		FoundOldest: foundOldest,
		FoundNewest: foundNewest,
	}
	if len(cursors) > 0 {
		page.PrevCursor = EncodeTranscriptRowCursor(cursors[0])
		page.NextCursor = EncodeTranscriptRowCursor(cursors[len(cursors)-1])
	}
	return page
}

func EncodeTranscriptRowCursor(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeTranscriptRowCursor(encoded string) (string, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return "", fmt.Errorf("transcript row cursor is required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid transcript row cursor")
	}
	if !strings.Contains(string(raw), transcriptRowCursorSeparator) {
		return "", fmt.Errorf("invalid transcript row cursor")
	}
	return string(raw), nil
}

func normalizeTranscriptRowLimit(limit int) int {
	if limit < 1 {
		return 24
	}
	if limit > 80 {
		return 80
	}
	return limit
}

func normalizeTranscriptRowAroundHalf(limit int) int {
	if limit < 0 {
		return 0
	}
	if limit > 40 {
		return 40
	}
	return limit
}

func reverseRows(rows []map[string]any, cursors []string) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
		cursors[i], cursors[j] = cursors[j], cursors[i]
	}
}

type StubSessionTranscriptRowStore struct{}

func (StubSessionTranscriptRowStore) RewriteEpoch(context.Context, string) (int64, error) {
	return 0, nil
}

func (StubSessionTranscriptRowStore) MaxEndOrderKey(context.Context, string) (string, error) {
	return "", nil
}

func (StubSessionTranscriptRowStore) ReplaceForTurn(context.Context, string, string, []map[string]any) error {
	return nil
}
func (StubSessionTranscriptRowStore) ReplaceForSession(context.Context, string, []map[string]any) error {
	return nil
}
func (StubSessionTranscriptRowStore) UpsertRows(context.Context, string, []map[string]any) error {
	return nil
}
func (StubSessionTranscriptRowStore) ListChangedAfterOrderKey(context.Context, string, string, int) (TranscriptRowDeltaPage, error) {
	return TranscriptRowDeltaPage{}, nil
}
func (StubSessionTranscriptRowStore) ListLatest(context.Context, string, int) (TranscriptRowPage, error) {
	return TranscriptRowPage{Rows: []map[string]any{}, FoundOldest: true, FoundNewest: true}, nil
}
func (StubSessionTranscriptRowStore) ListOldest(context.Context, string, int) (TranscriptRowPage, error) {
	return TranscriptRowPage{Rows: []map[string]any{}, FoundOldest: true, FoundNewest: true}, nil
}
func (StubSessionTranscriptRowStore) ListBefore(context.Context, string, string, int) (TranscriptRowPage, error) {
	return TranscriptRowPage{Rows: []map[string]any{}, FoundOldest: true, FoundNewest: false}, nil
}
func (StubSessionTranscriptRowStore) ListAround(context.Context, string, string, int, int) (TranscriptRowPage, error) {
	return TranscriptRowPage{Rows: []map[string]any{}, FoundOldest: true, FoundNewest: true}, nil
}
func (StubSessionTranscriptRowStore) ResolveCursorForTimelineID(context.Context, string, string) (string, error) {
	return "", nil
}
func (StubSessionTranscriptRowStore) NeedsBackfill(context.Context, string) (bool, error) {
	return false, nil
}
