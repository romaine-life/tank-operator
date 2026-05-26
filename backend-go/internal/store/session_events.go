package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// SessionEventStore reads the canonical SDK events the pod-side runners write
// to the session_events Postgres table. The orchestrator owns writes through
// the session bus persister. The SPA consumes these rows through live SSE and
// Turn activity detail reads; historical transcript navigation uses the
// materialized session_transcript_rows read model.
type SessionEventStore interface {
	Upsert(ctx context.Context, event map[string]any) error
	ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error)
	HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error)
	// OrderKeyForTimelineID resolves a rendered transcript entry id
	// (`timeline_id`) to the newest durable order_key that contributed to
	// that entry. Input-reply handlers use this to locate the originating
	// user message without consulting browser state.
	OrderKeyForTimelineID(ctx context.Context, tankSessionID, timelineID string) (string, error)
	// LatestEvents returns the most recent `limit` events for a session in
	// ASC order_key. Implemented as a DESC LIMIT N indexed scan reversed in
	// Go; bounded and indexed regardless of ledger size. Powers live SSE
	// resume bootstrap after the transcript-row snapshot is loaded.
	LatestEvents(ctx context.Context, tankSessionID string, limit int) (SessionEventPage, error)
	// EventsForTurn returns a bounded ASC slice for one turn. Transcript
	// projection uses this to build primary Turn activity shells and lazy
	// expansion bodies without relying on a browser-local raw-event window.
	EventsForTurn(ctx context.Context, tankSessionID, turnID string, limit int) (SessionEventPage, error)
	FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error)
	// LatestLifecycleEvents returns the most recent N lifecycle events
	// (turn.*, tool.approval_*) for a session in ascending order_key.
	// Bounded read used by the lifecycle emitter (chat→sidebar activity-
	// delta bridge) instead of folding the full ledger. item.failed is
	// intentionally excluded — see sessionactivity.LifecycleChatEventTypes.
	LatestLifecycleEvents(ctx context.Context, tankSessionID string, limit int) ([]map[string]any, error)
	// UnreadOutputCount returns the number of distinct timeline_id /
	// turn_id markers that count as "unread output" strictly after the
	// caller's last_read_order_key cursor. Implemented as two Postgres
	// COUNT(DISTINCT ...) queries against the (tank_session_id, order_key)
	// index so it stays bounded per session regardless of history size.
	UnreadOutputCount(ctx context.Context, tankSessionID, afterOrderKey string) (int, error)
}

// LifecycleEventTypes is the set of event types that drive run-status,
// active-turn-id, and needs-input transitions in the activity summary.
// Centralized here so the Postgres query, the stub, and the activity
// handler stay in sync with sessionactivity.LifecycleChatEventTypes.
// Order_key fold semantics are: ASC, last-write-wins per field.
//
// item.failed is intentionally excluded: see
// sessionactivity.LifecycleChatEventTypes for the rationale. It is still
// counted as unread output via UnreadOutputItemTypes below — the user
// should still see "1 new" when a tool errors — it just doesn't taint
// the session-level status.
var LifecycleEventTypes = []string{
	"turn.submitted",
	"turn.started",
	"turn.completed",
	"turn.failed",
	"turn.command_failed",
	"turn.interrupt_requested",
	"turn.interrupted",
	"tool.approval_requested",
	"tool.approval_resolved",
}

// UnreadOutputItemTypes are event types whose timeline_id contributes to
// the unread-output count. Excludes user-actor events and metadata-only
// turn lifecycle markers (turn.submitted / turn.started / turn.completed
// are not "unread output" — they're lifecycle, not content).
var UnreadOutputItemTypes = []string{
	"item.started",
	"item.completed",
	"item.failed",
	"shell_task.started",
	"shell_task.updated",
	"shell_task.exited",
	"tool.approval_requested",
	"tool.approval_resolved",
}

// UnreadOutputTurnTypes are turn-level terminal events that count as
// unread output via their turn_id (no timeline_id on these).
var UnreadOutputTurnTypes = []string{
	"turn.failed",
	"turn.command_failed",
	"turn.interrupted",
}

// SessionEventCursor describes a half-open range of session events. Callers
// supply at most one of AfterOrderKey / BeforeOrderKey; ListBySession reads
// events strictly after AfterOrderKey (ASC by default) or strictly before
// BeforeOrderKey (DESC, re-reversed to ASC in Go for the caller). Direction
// is "asc" by default and "desc" when paginating backwards via BeforeOrderKey.
// The pair (anchor, direction) plus a limit gives the SPA enough surface to
// implement Slack/Discord/Zulip-style tail-fetch + back-paginate without
// every read scanning the whole ledger.
type SessionEventCursor struct {
	AfterOrderKey  string
	BeforeOrderKey string
	Direction      string
}

// SessionEventPage wraps the events the store returns plus the cursors a
// caller needs to keep paginating in either direction. NextOrderKey advances
// a forward (ASC) walk; PrevOrderKey advances a backward (DESC) walk.
// FoundOldest / FoundNewest tell the SPA when to stop fetching further
// pages — mirroring Zulip's found_oldest / found_newest semantics.
type SessionEventPage struct {
	Events       []map[string]any
	NextOrderKey string
	PrevOrderKey string
	HasMore      bool
	FoundOldest  bool
	FoundNewest  bool
}

type postgresSessionEventStore struct {
	pool  *pgxpool.Pool
	scope string
}

type sessionEventQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func NewPostgresSessionEventStore(pool *pgxpool.Pool, scope string) SessionEventStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &postgresSessionEventStore{pool: pool, scope: scope}
}

func (s *postgresSessionEventStore) Upsert(ctx context.Context, event map[string]any) error {
	if err := conversation.ValidateEventMap(event); err != nil {
		return err
	}
	doc := cloneSessionEventMap(event)
	storageKey := stringField(doc, "tank_session_id")
	publicSessionID := stringField(doc, "session_id")
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(s.scope, publicSessionID)
	}
	if storageKey == "" {
		return errMissingSessionEventField("tank_session_id")
	}
	id := stringField(doc, "id")
	if id == "" {
		id = stringField(doc, "uuid")
	}
	if id == "" {
		id = stringField(doc, "event_id")
	}
	if id == "" {
		return errMissingSessionEventField("id")
	}
	orderKey := stringField(doc, "order_key")
	if orderKey == "" {
		return errMissingSessionEventField("order_key")
	}
	doc["id"] = id
	doc["tank_session_id"] = storageKey
	if _, ok := doc["tank_public_session_id"]; !ok && publicSessionID != "" {
		doc["tank_public_session_id"] = publicSessionID
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	turnID := stringField(doc, "turn_id")
	eventType := stringField(doc, "type")

	const q = `
		INSERT INTO session_events (
			tank_session_id, order_key, event_id, turn_id, event_type, payload
		) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6)
		ON CONFLICT (tank_session_id, order_key) DO UPDATE
		SET event_id   = EXCLUDED.event_id,
			turn_id    = EXCLUDED.turn_id,
			event_type = EXCLUDED.event_type,
			payload    = EXCLUDED.payload
	`
	_, err = s.pool.Exec(ctx, q, storageKey, orderKey, id, turnID, eventType, payload)
	return err
}

// ListBySession reads a single bounded page of events for one session. The
// cursor is one of:
//
//   - AfterOrderKey set, Direction "asc" (or empty): events strictly after
//     the cursor, ASC by order_key.
//   - BeforeOrderKey set, Direction "desc": events strictly before the
//     cursor, DESC by order_key, re-reversed in Go so the caller still
//     reads them ASC.
//   - Neither set, Direction "asc" (or empty): the head of the ledger ASC.
//   - Neither set, Direction "desc": equivalent to LatestEvents — the
//     tail of the ledger.
//
// The (tank_session_id, order_key) index makes both directions indexed
// seeks; no full-session scan on any read path.
func (s *postgresSessionEventStore) ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	return s.listBySession(ctx, s.pool, tankSessionID, cursor, limit)
}

func (s *postgresSessionEventStore) ListBySessionTx(ctx context.Context, tx pgx.Tx, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	return s.listBySession(ctx, tx, tankSessionID, cursor, limit)
}

func (s *postgresSessionEventStore) listBySession(ctx context.Context, qx sessionEventQueryer, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	queryLimit := limit + 1
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	descending := strings.EqualFold(cursor.Direction, "desc") || cursor.BeforeOrderKey != ""

	const baseQuery = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1 AND order_key <> ''
	`
	q := baseQuery
	args := []any{storageKey}
	switch {
	case descending && cursor.BeforeOrderKey != "":
		q += " AND order_key < $2 ORDER BY order_key DESC LIMIT $3"
		args = append(args, cursor.BeforeOrderKey, queryLimit)
	case descending:
		q += " ORDER BY order_key DESC LIMIT $2"
		args = append(args, queryLimit)
	case cursor.AfterOrderKey != "":
		q += " AND order_key > $2 ORDER BY order_key ASC LIMIT $3"
		args = append(args, cursor.AfterOrderKey, queryLimit)
	default:
		q += " ORDER BY order_key ASC LIMIT $2"
		args = append(args, queryLimit)
	}

	rows, err := qx.Query(ctx, q, args...)
	if err != nil {
		return SessionEventPage{}, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, queryLimit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return SessionEventPage{}, err
		}
		var doc map[string]any
		if err := json.Unmarshal(payload, &doc); err != nil {
			return SessionEventPage{}, fmt.Errorf("session-events doc is not JSON: %w", err)
		}
		if err := conversation.ValidateEventMap(doc); err != nil {
			// Per docs/migration-policy.md, the read path no longer silently
			// filters malformed docs. The producer-side cutover (runner
			// dispatch contract, persister schema-terminal NAK) guarantees
			// only Tank events land in storage. A failure here means one of
			// those guarantees regressed — surface it.
			return SessionEventPage{}, fmt.Errorf("session-events doc rejected by schema: %w", err)
		}
		doc["tank_session_id"] = tankSessionID
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return SessionEventPage{}, err
	}
	if descending {
		// Caller always reads ASC; reverse the DESC slice in place.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return sessionEventPageFromDescendingScan(out, limit, cursor), nil
	}
	return sessionEventPageFromAscendingScan(out, limit, cursor), nil
}

// LatestEvents returns the most recent `limit` events ASC. Indexed DESC LIMIT
// scan reversed in Go. Powers SSE resume bootstrap after the transcript-row
// snapshot is loaded.
func (s *postgresSessionEventStore) LatestEvents(ctx context.Context, tankSessionID string, limit int) (SessionEventPage, error) {
	return s.ListBySession(ctx, tankSessionID, SessionEventCursor{Direction: "desc"}, limit)
}

func (s *postgresSessionEventStore) EventsForTurn(ctx context.Context, tankSessionID, turnID string, limit int) (SessionEventPage, error) {
	return s.eventsForTurn(ctx, s.pool, tankSessionID, turnID, limit)
}

func (s *postgresSessionEventStore) EventsForTurnTx(ctx context.Context, tx pgx.Tx, tankSessionID, turnID string, limit int) (SessionEventPage, error) {
	return s.eventsForTurn(ctx, tx, tankSessionID, turnID, limit)
}

func (s *postgresSessionEventStore) eventsForTurn(ctx context.Context, qx sessionEventQueryer, tankSessionID, turnID string, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	queryLimit := limit + 1
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND turn_id = $2
			AND order_key <> ''
		ORDER BY order_key ASC
		LIMIT $3
	`
	rows, err := qx.Query(ctx, q, storageKey, strings.TrimSpace(turnID), queryLimit)
	if err != nil {
		return SessionEventPage{}, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, queryLimit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return SessionEventPage{}, err
		}
		var doc map[string]any
		if err := json.Unmarshal(payload, &doc); err != nil {
			return SessionEventPage{}, fmt.Errorf("session-events doc is not JSON: %w", err)
		}
		if err := conversation.ValidateEventMap(doc); err != nil {
			return SessionEventPage{}, fmt.Errorf("session-events doc rejected by schema: %w", err)
		}
		doc["tank_session_id"] = tankSessionID
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return SessionEventPage{}, err
	}
	return sessionEventPageFromAscendingScan(out, limit, SessionEventCursor{}), nil
}

func (s *postgresSessionEventStore) HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT 1
		FROM session_events
		WHERE tank_session_id = $1 AND order_key = $2
		LIMIT 1
	`
	var one int
	err := s.pool.QueryRow(ctx, q, storageKey, orderKey).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *postgresSessionEventStore) OrderKeyForTimelineID(ctx context.Context, tankSessionID, timelineID string) (string, error) {
	timelineID = strings.TrimSpace(timelineID)
	if timelineID == "" {
		return "", nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT order_key
		FROM session_events
		WHERE tank_session_id = $1
			AND payload ->> 'timeline_id' = $2
		ORDER BY order_key DESC
		LIMIT 1
	`
	var orderKey string
	err := s.pool.QueryRow(ctx, q, storageKey, timelineID).Scan(&orderKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return orderKey, nil
}

func (s *postgresSessionEventStore) FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, nil
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND turn_id = $2
			AND event_type IN ($3, $4, $5, $6)
		ORDER BY order_key DESC
		LIMIT 1
	`
	var payload []byte
	err := s.pool.QueryRow(ctx, q, storageKey, turnID,
		string(conversation.EventTurnCompleted),
		string(conversation.EventTurnFailed),
		string(conversation.EventTurnCommandFailed),
		string(conversation.EventTurnInterrupted),
	).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("session-events doc is not JSON: %w", err)
	}
	if err := conversation.ValidateEventMap(doc); err != nil {
		return nil, fmt.Errorf("session-events doc rejected by schema: %w", err)
	}
	doc["tank_session_id"] = tankSessionID
	return doc, nil
}

// LatestLifecycleEvents returns up to `limit` recent lifecycle events
// (turn.*, item.failed, tool.approval_*) for one session, ascending by
// order_key. Postgres returns the slice DESC LIMIT N and we reverse in Go;
// indexed on (tank_session_id, order_key) so this is a bounded backwards
// scan, not a full ledger fold.
func (s *postgresSessionEventStore) LatestLifecycleEvents(ctx context.Context, tankSessionID string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND event_type = ANY($2::text[])
			AND order_key <> ''
		ORDER BY order_key DESC
		LIMIT $3
	`
	rows, err := s.pool.Query(ctx, q, storageKey, LifecycleEventTypes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := json.Unmarshal(payload, &doc); err != nil {
			return nil, fmt.Errorf("session-events doc is not JSON: %w", err)
		}
		if err := conversation.ValidateEventMap(doc); err != nil {
			return nil, fmt.Errorf("session-events doc rejected by schema: %w", err)
		}
		doc["tank_session_id"] = tankSessionID
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// DESC -> ASC for the activity-fold's last-write-wins semantics.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// UnreadOutputCount returns the number of distinct timeline_id markers (for
// output-producing item events) plus distinct turn_id markers (for terminal
// turn events) strictly after the caller's last_read_order_key cursor. The
// two slices are queried separately because they use different distinct-key
// columns; SUM in Go is one round-trip cheaper than a single CTE union.
func (s *postgresSessionEventStore) UnreadOutputCount(ctx context.Context, tankSessionID, afterOrderKey string) (int, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	itemCount, err := s.countDistinctField(
		ctx, storageKey, "timeline_id", UnreadOutputItemTypes, afterOrderKey,
	)
	if err != nil {
		return 0, err
	}
	turnCount, err := s.countDistinctField(
		ctx, storageKey, "turn_id", UnreadOutputTurnTypes, afterOrderKey,
	)
	if err != nil {
		return 0, err
	}
	return itemCount + turnCount, nil
}

// countDistinctField counts distinct non-empty values of a payload field
// (jsonb `->>` extractor for "timeline_id"; the dedicated `turn_id` column
// for "turn_id") across event_type membership in `types`, excluding events
// where actor='user', strictly after the optional order_key cursor.
func (s *postgresSessionEventStore) countDistinctField(
	ctx context.Context, storageKey, field string, types []string, afterOrderKey string,
) (int, error) {
	if len(types) == 0 {
		return 0, nil
	}
	// `turn_id` lives in a typed column for indexing; everything else lives
	// in the jsonb payload and we extract via `->>`.
	var selectExpr string
	if field == "turn_id" {
		selectExpr = "turn_id"
	} else {
		selectExpr = fmt.Sprintf("payload->>'%s'", field)
	}
	q := fmt.Sprintf(`
		SELECT COUNT(DISTINCT %s)
		FROM session_events
		WHERE tank_session_id = $1
			AND event_type = ANY($2::text[])
			AND COALESCE(payload->>'actor', '') <> 'user'
			AND %s IS NOT NULL
			AND %s <> ''
	`, selectExpr, selectExpr, selectExpr)
	args := []any{storageKey, types}
	if strings.TrimSpace(afterOrderKey) != "" {
		q += " AND order_key > $3"
		args = append(args, afterOrderKey)
	}
	var n int
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// sessionEventPageFromAscendingScan packages a forward-walk page. The caller
// passed an ascending slice with one extra row (`limit + 1`) when `HasMore`
// is true. FoundOldest is true when the page starts at the very head of the
// ledger (no AfterOrderKey cursor was provided). FoundNewest is true when
// the page is shorter than the limit AND no row beyond `limit` was fetched.
func sessionEventPageFromAscendingScan(events []map[string]any, limit int, cursor SessionEventCursor) SessionEventPage {
	limit = normalizeSessionEventLimit(limit)
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	page := SessionEventPage{
		Events:      append([]map[string]any(nil), events...),
		HasMore:     hasMore,
		FoundOldest: cursor.AfterOrderKey == "" && cursor.BeforeOrderKey == "",
		FoundNewest: !hasMore,
	}
	if len(events) > 0 {
		page.PrevOrderKey = eventOrderKey(events[0])
		page.NextOrderKey = eventOrderKey(events[len(events)-1])
	}
	return page
}

// sessionEventPageFromDescendingScan packages a backward-walk page. The
// caller already reversed the DESC scan into ASC order in place. FoundNewest
// is true when no BeforeOrderKey cursor was provided (tail read).
// FoundOldest is true when the underlying DESC scan returned fewer than
// `limit + 1` rows, i.e. no row beyond `limit` was available.
func sessionEventPageFromDescendingScan(events []map[string]any, limit int, cursor SessionEventCursor) SessionEventPage {
	limit = normalizeSessionEventLimit(limit)
	hasMore := len(events) > limit
	if hasMore {
		// The extra row sits at index 0 after reversal (it was the
		// (limit+1)th oldest in DESC order). Drop it.
		events = events[1:]
	}
	page := SessionEventPage{
		Events:      append([]map[string]any(nil), events...),
		HasMore:     hasMore,
		FoundOldest: !hasMore,
		FoundNewest: cursor.BeforeOrderKey == "",
	}
	if len(events) > 0 {
		page.PrevOrderKey = eventOrderKey(events[0])
		page.NextOrderKey = eventOrderKey(events[len(events)-1])
	}
	return page
}

func eventOrderKey(doc map[string]any) string {
	if value, ok := doc["order_key"].(string); ok && value != "" {
		return value
	}
	return ""
}

func normalizeSessionEventLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}

// Stub for local dev where Postgres isn't configured.
type StubSessionEventStore struct{}

func (StubSessionEventStore) Upsert(_ context.Context, _ map[string]any) error { return nil }

func (StubSessionEventStore) ListBySession(_ context.Context, _ string, _ SessionEventCursor, _ int) (SessionEventPage, error) {
	return SessionEventPage{
		Events:      []map[string]any{},
		FoundOldest: true,
		FoundNewest: true,
	}, nil
}

func (StubSessionEventStore) LatestEvents(_ context.Context, _ string, _ int) (SessionEventPage, error) {
	return SessionEventPage{
		Events:      []map[string]any{},
		FoundOldest: true,
		FoundNewest: true,
	}, nil
}

func (StubSessionEventStore) EventsForTurn(_ context.Context, _, _ string, _ int) (SessionEventPage, error) {
	return SessionEventPage{
		Events:      []map[string]any{},
		FoundOldest: true,
		FoundNewest: true,
	}, nil
}

func (StubSessionEventStore) HasOrderKey(_ context.Context, _, orderKey string) (bool, error) {
	return strings.TrimSpace(orderKey) == "", nil
}

func (StubSessionEventStore) OrderKeyForTimelineID(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (StubSessionEventStore) FindTurnTerminal(_ context.Context, _, _ string) (map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) LatestLifecycleEvents(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) UnreadOutputCount(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

func cloneSessionEventMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func stringField(doc map[string]any, key string) string {
	value, _ := doc[key].(string)
	return strings.TrimSpace(value)
}

func errMissingSessionEventField(field string) error {
	return &sessionEventFieldError{field: field}
}

type sessionEventFieldError struct {
	field string
}

func (e *sessionEventFieldError) Error() string {
	return "session event " + e.field + " is required"
}
