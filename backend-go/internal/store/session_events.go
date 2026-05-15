package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

// SessionEventStore reads the canonical SDK events the pod-side runners write
// to the session_events Postgres table. The orchestrator owns writes through
// the session bus persister, and the SPA consumes those same durable rows
// through timeline snapshots and the SSE stream.
type SessionEventStore interface {
	Upsert(ctx context.Context, event map[string]any) error
	ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error)
	HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error)
	FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error)
}

type SessionEventCursor struct {
	AfterOrderKey string
}

type SessionEventPage struct {
	Events       []map[string]any
	NextOrderKey string
	HasMore      bool
}

type postgresSessionEventStore struct {
	pool  *pgxpool.Pool
	scope string
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
		storageKey = compat.SessionStorageKey(s.scope, publicSessionID)
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

// ListBySession returns events for one session strictly after the canonical
// Tank order_key cursor. Indexed seek on (tank_session_id, order_key) so no
// full-session scan on every replay or stream tick.
func (s *postgresSessionEventStore) ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	queryLimit := limit + 1
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)

	const baseQuery = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1 AND order_key <> ''
	`
	q := baseQuery
	args := []any{storageKey}
	if cursor.AfterOrderKey != "" {
		q += " AND order_key > $2 ORDER BY order_key ASC LIMIT $3"
		args = append(args, cursor.AfterOrderKey, queryLimit)
	} else {
		q += " ORDER BY order_key ASC LIMIT $2"
		args = append(args, queryLimit)
	}

	rows, err := s.pool.Query(ctx, q, args...)
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
	return sessionEventPageFromOrdered(out, limit), nil
}

func (s *postgresSessionEventStore) HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
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

func (s *postgresSessionEventStore) FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, nil
	}
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND turn_id = $2
			AND event_type IN ($3, $4, $5)
		ORDER BY order_key DESC
		LIMIT 1
	`
	var payload []byte
	err := s.pool.QueryRow(ctx, q, storageKey, turnID,
		string(conversation.EventTurnCompleted),
		string(conversation.EventTurnFailed),
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

func sessionEventPageFromOrdered(events []map[string]any, limit int) SessionEventPage {
	limit = normalizeSessionEventLimit(limit)
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	nextOrderKey := ""
	if len(events) > 0 {
		nextOrderKey = eventOrderKey(events[len(events)-1])
	}
	return SessionEventPage{
		Events:       append([]map[string]any(nil), events...),
		NextOrderKey: nextOrderKey,
		HasMore:      hasMore,
	}
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
	return SessionEventPage{Events: []map[string]any{}}, nil
}

func (StubSessionEventStore) HasOrderKey(_ context.Context, _, orderKey string) (bool, error) {
	return strings.TrimSpace(orderKey) == "", nil
}

func (StubSessionEventStore) FindTurnTerminal(_ context.Context, _, _ string) (map[string]any, error) {
	return nil, nil
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
