package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

const transcriptRowCursorSeparator = "\x1f"
const transcriptRowBackfillVersion = 2

type SessionTranscriptRowStore interface {
	ReplaceForTurn(ctx context.Context, tankSessionID, turnID string, entries []map[string]any) error
	ReplaceForSession(ctx context.Context, tankSessionID string, entries []map[string]any) error
	UpsertRows(ctx context.Context, tankSessionID string, entries []map[string]any) error
	ListLatest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error)
	ListOldest(ctx context.Context, tankSessionID string, rows int) (TranscriptRowPage, error)
	ListBefore(ctx context.Context, tankSessionID, beforeCursor string, rows int) (TranscriptRowPage, error)
	ListAround(ctx context.Context, tankSessionID, rowCursor string, rowsBefore, rowsAfter int) (TranscriptRowPage, error)
	ResolveCursorForTimelineID(ctx context.Context, tankSessionID, timelineID string) (string, error)
	BackfillSessionIDs(ctx context.Context) ([]string, error)
}

type TranscriptRowPage struct {
	Rows        []map[string]any
	PrevCursor  string
	NextCursor  string
	FoundOldest bool
	FoundNewest bool
}

type transcriptRowStore struct {
	pool  *pgxpool.Pool
	scope string
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
	_, err := tx.Exec(ctx, `
		INSERT INTO session_transcript_row_backfills (
			tank_session_id, projection_version, completed_at
		) VALUES ($1, $2, now())
		ON CONFLICT (tank_session_id) DO UPDATE
		SET projection_version = EXCLUDED.projection_version,
			completed_at = now()
	`, storageKey, transcriptRowBackfillVersion)
	return err
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

func (s *transcriptRowStore) BackfillSessionIDs(ctx context.Context) ([]string, error) {
	scope := strings.TrimSpace(s.scope)
	if scope == "" {
		scope = "default"
	}
	scopeClause := "AND position(':' in se.tank_session_id) = 0"
	args := []any{transcriptRowBackfillVersion}
	if scope != "default" {
		scopeClause = "AND left(se.tank_session_id, length($2)) = $2"
		args = append(args, scope+":")
	}
	q := `
		SELECT DISTINCT se.payload ->> 'session_id'
		FROM session_events se
		WHERE coalesce(se.payload ->> 'session_id', '') <> ''
			` + scopeClause + `
			AND NOT EXISTS (
				SELECT 1
				FROM session_transcript_row_backfills bf
				WHERE bf.tank_session_id = se.tank_session_id
					AND bf.projection_version = $1
			)
		ORDER BY 1
	`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, err
		}
		out = append(out, sessionID)
	}
	return out, rows.Err()
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

func (StubSessionTranscriptRowStore) ReplaceForTurn(context.Context, string, string, []map[string]any) error {
	return nil
}
func (StubSessionTranscriptRowStore) ReplaceForSession(context.Context, string, []map[string]any) error {
	return nil
}
func (StubSessionTranscriptRowStore) UpsertRows(context.Context, string, []map[string]any) error {
	return nil
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
func (StubSessionTranscriptRowStore) BackfillSessionIDs(context.Context) ([]string, error) {
	return nil, nil
}
