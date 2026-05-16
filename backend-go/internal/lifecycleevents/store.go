package lifecycleevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the read+write surface for session_lifecycle_events.
//
// Writes are append-only and idempotent at (session_scope, session_id,
// event_id) via the unique index — pod-informer restart resync that re-
// observes existing pods skips re-emitting the prior transitions. Reads
// are per-owner (the sidebar's natural shape) ordered by the global
// per-owner BIGSERIAL order_key.
type Store interface {
	// Append writes one durable lifecycle event. Returns the assigned
	// order_key and the row's stored occurred_at on success. ON CONFLICT
	// on event_id resolves to a no-op insert and returns the existing
	// row's order_key (alreadyExists=true) so callers can tell idempotent
	// re-writes from fresh ones — useful for the producer-side "should I
	// publish on NATS" decision.
	Append(ctx context.Context, event Event) (assigned Event, alreadyExists bool, err error)

	// ListByOwner returns events for one owner strictly after cursor in
	// ascending order_key, up to limit. The page contains at most `limit`
	// rows; HasMore signals there's at least one further row. Used by both
	// the snapshot bootstrap (GET /api/sessions/timeline) and the catch-up
	// pass that runs at SSE-stream open before the NATS subscription.
	ListByOwner(ctx context.Context, owner string, cursor Cursor, limit int) (Page, error)

	// HasOrderKey is the cursor-validation hook used by the SSE handler.
	// An empty order_key counts as valid (snapshot bootstrap). A non-empty
	// order_key that no row matches forces a resync_required SSE event so
	// the client re-fetches /api/sessions instead of silently skipping a
	// gap.
	HasOrderKey(ctx context.Context, owner, orderKey string) (bool, error)

	// LatestActivity returns the most recent session.activity_changed
	// payload for one session, or nil if none exists yet. Used by both
	// GET /api/sessions (initial-state hydration) and the persister
	// (delta computation: only emit when the new summary differs).
	LatestActivity(ctx context.Context, scope, sessionID string) (*ActivitySummary, error)

	// LatestPodStatus returns the most recent pod-state event summary
	// for one session, or nil if none. Used by GET /api/sessions to
	// derive the durable Status field — replaces the live podStatus()
	// compute that ran against the pod object on every List() call.
	LatestPodStatus(ctx context.Context, scope, sessionID string) (*PodStatusSummary, error)
}

type postgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore returns a Store backed by the session_lifecycle_events
// table. Schema is owned by pgstore/migrations.go and applied at startup.
func NewPostgresStore(pool *pgxpool.Pool) Store {
	return &postgresStore{pool: pool}
}

func (s *postgresStore) Append(ctx context.Context, event Event) (Event, bool, error) {
	if err := validateAppend(event); err != nil {
		return Event{}, false, err
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return Event{}, false, fmt.Errorf("lifecycleevents: marshal payload: %w", err)
	}
	occurredAt := strings.TrimSpace(event.OccurredAt)
	if occurredAt == "" {
		occurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	occurred, err := parseEventTime(occurredAt)
	if err != nil {
		return Event{}, false, fmt.Errorf("lifecycleevents: parse occurred_at: %w", err)
	}

	// INSERT ... ON CONFLICT (session_scope, session_id, event_id) DO
	// NOTHING is the producer-side idempotency primitive: the pod-informer
	// emits an event_id stamped from (session_id, type, the truncated
	// occurred_at) so a cache resync that re-observes the same state
	// transition is a no-op insert. RETURNING fires only on the fresh
	// insert; on conflict we fall back to a SELECT to fetch the existing
	// row's order_key so the caller can decide whether to publish on NATS.
	const insertQ = `
		INSERT INTO session_lifecycle_events (
			email, session_scope, session_id, event_type, event_id, payload, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (session_scope, session_id, event_id) DO NOTHING
		RETURNING order_key, occurred_at
	`
	var (
		orderKey   int64
		storedTime time.Time
	)
	row := s.pool.QueryRow(ctx, insertQ,
		normalizeOwner(event.Email),
		strings.TrimSpace(event.SessionScope),
		strings.TrimSpace(event.SessionID),
		event.Type,
		event.EventID,
		payload,
		occurred,
	)
	switch err := row.Scan(&orderKey, &storedTime); {
	case err == nil:
		assigned := event
		assigned.OrderKey = formatOrderKey(orderKey)
		assigned.OccurredAt = storedTime.UTC().Format(time.RFC3339Nano)
		assigned.Email = normalizeOwner(event.Email)
		return assigned, false, nil
	case errors.Is(err, pgx.ErrNoRows):
		// Conflict path — fetch the existing row's order_key + occurred_at.
		const selectQ = `
			SELECT order_key, occurred_at
			FROM session_lifecycle_events
			WHERE session_scope = $1 AND session_id = $2 AND event_id = $3
		`
		if err := s.pool.QueryRow(ctx, selectQ,
			strings.TrimSpace(event.SessionScope),
			strings.TrimSpace(event.SessionID),
			event.EventID,
		).Scan(&orderKey, &storedTime); err != nil {
			return Event{}, false, fmt.Errorf("lifecycleevents: read conflict row: %w", err)
		}
		assigned := event
		assigned.OrderKey = formatOrderKey(orderKey)
		assigned.OccurredAt = storedTime.UTC().Format(time.RFC3339Nano)
		assigned.Email = normalizeOwner(event.Email)
		return assigned, true, nil
	default:
		return Event{}, false, fmt.Errorf("lifecycleevents: insert: %w", err)
	}
}

func (s *postgresStore) ListByOwner(ctx context.Context, owner string, cursor Cursor, limit int) (Page, error) {
	limit = normalizeLimit(limit)
	owner = normalizeOwner(owner)
	if owner == "" {
		return Page{}, nil
	}

	// Read limit+1 to set HasMore without a second COUNT — mirrors the
	// session_events pagination shape.
	queryLimit := limit + 1
	const baseQ = `
		SELECT order_key, email, session_scope, session_id, event_type,
		       event_id, payload, occurred_at
		FROM session_lifecycle_events
		WHERE email = $1
	`
	q := baseQ
	args := []any{owner}
	if cursor.AfterOrderKey != "" {
		after, err := parseOrderKey(cursor.AfterOrderKey)
		if err != nil {
			return Page{}, err
		}
		q += " AND order_key > $2 ORDER BY order_key ASC LIMIT $3"
		args = append(args, after, queryLimit)
	} else {
		q += " ORDER BY order_key ASC LIMIT $2"
		args = append(args, queryLimit)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()

	out := make([]Event, 0, queryLimit)
	for rows.Next() {
		var (
			orderKey   int64
			email      string
			scope      string
			sessionID  string
			eventType  string
			eventID    string
			payload    []byte
			occurredAt time.Time
		)
		if err := rows.Scan(&orderKey, &email, &scope, &sessionID, &eventType, &eventID, &payload, &occurredAt); err != nil {
			return Page{}, err
		}
		var decoded map[string]any
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &decoded); err != nil {
				return Page{}, fmt.Errorf("lifecycleevents: payload not JSON for order_key=%d: %w", orderKey, err)
			}
		}
		if decoded == nil {
			decoded = map[string]any{}
		}
		out = append(out, Event{
			OrderKey:     formatOrderKey(orderKey),
			Email:        email,
			SessionScope: scope,
			SessionID:    sessionID,
			Type:         eventType,
			EventID:      eventID,
			Payload:      decoded,
			OccurredAt:   occurredAt.UTC().Format(time.RFC3339Nano),
		})
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := ""
	if len(out) > 0 {
		next = out[len(out)-1].OrderKey
	}
	return Page{Events: out, NextOrderKey: next, HasMore: hasMore}, nil
}

func (s *postgresStore) HasOrderKey(ctx context.Context, owner, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	parsed, err := parseOrderKey(orderKey)
	if err != nil {
		return false, nil
	}
	const q = `
		SELECT 1
		FROM session_lifecycle_events
		WHERE email = $1 AND order_key = $2
		LIMIT 1
	`
	var one int
	err = s.pool.QueryRow(ctx, q, normalizeOwner(owner), parsed).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *postgresStore) LatestActivity(ctx context.Context, scope, sessionID string) (*ActivitySummary, error) {
	const q = `
		SELECT payload
		FROM session_lifecycle_events
		WHERE session_scope = $1 AND session_id = $2 AND event_type = $3
		ORDER BY order_key DESC
		LIMIT 1
	`
	var payload []byte
	err := s.pool.QueryRow(ctx, q,
		strings.TrimSpace(scope),
		strings.TrimSpace(sessionID),
		EventTypeActivityChanged,
	).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out ActivitySummary
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("lifecycleevents: latest activity unmarshal: %w", err)
	}
	return &out, nil
}

func (s *postgresStore) LatestPodStatus(ctx context.Context, scope, sessionID string) (*PodStatusSummary, error) {
	// Pull the latest pod-state event payload; status is encoded into the
	// payload by the producer so the read path doesn't have to switch on
	// event_type itself.
	const q = `
		SELECT event_type, payload, occurred_at
		FROM session_lifecycle_events
		WHERE session_scope = $1 AND session_id = $2
		  AND event_type = ANY($3::text[])
		ORDER BY order_key DESC
		LIMIT 1
	`
	var (
		eventType  string
		payload    []byte
		occurredAt time.Time
	)
	err := s.pool.QueryRow(ctx, q,
		strings.TrimSpace(scope),
		strings.TrimSpace(sessionID),
		PodEventTypes,
	).Scan(&eventType, &payload, &occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var body struct {
		Status  string  `json:"status"`
		ReadyAt *string `json:"ready_at"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, fmt.Errorf("lifecycleevents: latest pod status unmarshal: %w", err)
		}
	}
	if body.Status == "" {
		// Fall back to deriving status from the event type — the producer
		// always sets payload.status, but a forward-compat read against an
		// older row shape stays meaningful.
		body.Status = statusForPodEventType(eventType)
	}
	return &PodStatusSummary{
		Status:     body.Status,
		ReadyAt:    body.ReadyAt,
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// statusForPodEventType maps event_type to the legacy "Pending"/"Active"/
// "Failed" string the frontend renders. Kept as a fallback in
// LatestPodStatus; producers always set payload.status directly.
func statusForPodEventType(eventType string) string {
	switch eventType {
	case EventTypePodReady:
		return "Active"
	case EventTypePodFailed:
		return "Failed"
	case EventTypePodTerminating:
		return "Failed"
	case EventTypePodScheduled, EventTypePodNotReady:
		return "Pending"
	}
	return ""
}

// validateAppend enforces the shape Append requires. Strict checks here
// surface producer bugs at write time rather than turning into a silent
// "row missing required field" at read time.
func validateAppend(event Event) error {
	if strings.TrimSpace(event.Email) == "" {
		return fmt.Errorf("lifecycleevents: email is required")
	}
	if strings.TrimSpace(event.SessionScope) == "" {
		return fmt.Errorf("lifecycleevents: session_scope is required")
	}
	if strings.TrimSpace(event.SessionID) == "" {
		return fmt.Errorf("lifecycleevents: session_id is required")
	}
	if strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("lifecycleevents: type is required")
	}
	if strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("lifecycleevents: event_id is required")
	}
	return nil
}

func normalizeOwner(owner string) string {
	return strings.ToLower(strings.TrimSpace(owner))
}

func normalizeLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}

func formatOrderKey(value int64) string {
	return fmt.Sprintf("%d", value)
}

func parseOrderKey(value string) (int64, error) {
	var out int64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &out)
	if err != nil {
		return 0, fmt.Errorf("lifecycleevents: invalid order_key %q", value)
	}
	return out, nil
}

func parseEventTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05+00:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", value)
}

// StubStore is the in-memory implementation used when POSTGRES_HOST is
// unset (first-install ordering) or in unit tests that don't need real
// Postgres. Append + ListByOwner + HasOrderKey + LatestActivity +
// LatestPodStatus all behave as the real store does for one process; no
// cross-process visibility (which is fine for stub use cases).
type StubStore struct{}

func (StubStore) Append(_ context.Context, event Event) (Event, bool, error) {
	return event, false, nil
}

func (StubStore) ListByOwner(_ context.Context, _ string, _ Cursor, _ int) (Page, error) {
	return Page{}, nil
}

func (StubStore) HasOrderKey(_ context.Context, _, orderKey string) (bool, error) {
	return strings.TrimSpace(orderKey) == "", nil
}

func (StubStore) LatestActivity(_ context.Context, _, _ string) (*ActivitySummary, error) {
	return nil, nil
}

func (StubStore) LatestPodStatus(_ context.Context, _, _ string) (*PodStatusSummary, error) {
	return nil, nil
}
