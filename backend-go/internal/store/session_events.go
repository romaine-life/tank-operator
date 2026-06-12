package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// SessionEventStore reads the canonical SDK events the pod-side runners write
// to the session_events Postgres table. The orchestrator owns writes through
// the session bus persister. The SPA consumes these rows through live SSE and
// Turn activity detail reads; historical transcript navigation uses the
// materialized session_transcript_rows read model.
type SessionEventStore interface {
	// Upsert writes one event row keyed by (tank_session_id, order_key).
	// The returned bool reports whether the row was newly inserted: false
	// means a row with that key already existed (an at-least-once
	// redelivery or a producer republish) and was overwritten in place.
	// The persister uses the report to skip duplicate side effects
	// (lifecycle emit, per-event counters) without skipping the
	// projection refresh a redelivered-after-failed-refresh event still
	// needs.
	Upsert(ctx context.Context, event map[string]any) (inserted bool, err error)
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
	// EventsForTurnAfter returns the next ASC page of a turn's events strictly
	// after afterOrderKey (empty reads from the start). Paged to exhaustion it
	// reads a whole turn. It replaced a bounded first-N per-turn read that
	// truncated long turns oldest-first and silently dropped the turn's
	// terminal, making a finished turn render as perpetually active; the
	// terminal-correct shell and turn-activity pagination are both built on
	// reading the complete turn, never a fixed-size prefix.
	EventsForTurnAfter(ctx context.Context, tankSessionID, turnID, afterOrderKey string, limit int) (SessionEventPage, error)
	FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error)
	// LatestLifecycleEvents returns the most recent N lifecycle events
	// (the turn.* lifecycle set, including turn.awaiting_input) for a
	// session in ascending order_key.
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
	// CountContextCompactions returns the total number of durable
	// context.compacted events recorded for a session across its whole
	// history. It backs the composer's durable compaction-count metric and is
	// recomputed by the chat-activity emitter on each context.compacted upsert.
	// Bounded and indexed by the session_events_context_compacted partial index
	// so it stays cheap regardless of total ledger size.
	CountContextCompactions(ctx context.Context, tankSessionID string) (int64, error)
	// CountUserMessages returns the total number of durable user_message.created
	// events recorded for a session across its whole history — one per human
	// back-and-forth submission. It is durable row metadata recomputed by the
	// chat-activity emitter on each
	// user_message.created upsert. Bounded and indexed by the
	// session_events_user_message_by_session partial index so it stays cheap
	// regardless of total ledger size. Background-task wake continuations do not
	// write user_message.created, so they are correctly excluded.
	CountUserMessages(ctx context.Context, tankSessionID string) (int64, error)
	// ShellTaskEvents returns every durable shell_task.* event for a session in
	// ASC order_key. It backs the session-level Background screen, which
	// projects the background (run_in_background) shell-task ledger. Bounded and
	// indexed by the session_events_shell_task partial index so it stays cheap
	// regardless of total ledger size. It is on the interface (not a concrete
	// method reached by type assertion) so it survives the materializing store
	// wrapper that fronts the local scope.
	ShellTaskEvents(ctx context.Context, tankSessionID string) ([]map[string]any, error)
	// FindStrandedLaunchTurns returns deferred-launch turns that were durably
	// recorded (a lone user_message.created) but never dispatched: their
	// turn_id carries no other event of any kind. This is the cross-session
	// read backing the stranded-launch sweep
	// (cmd/tank-operator/stranded_launch_sweep.go). The scan is bounded to
	// launches whose user_message.created lands in [notBefore, olderThan):
	// the olderThan floor keeps a just-submitted turn — whose turn.submitted
	// is written milliseconds after user_message.created in the same backend
	// call — from ever being mistaken for a strand, and the notBefore ceiling
	// keeps the scan off the deep history. Returns at most `limit` rows, ASC
	// by created_at (oldest strands first).
	FindStrandedLaunchTurns(ctx context.Context, olderThan, notBefore time.Time, limit int) ([]StrandedLaunchTurn, error)
	// FindStrandedTurns returns dispatched turns that stranded mid-lifecycle:
	// a turn.submitted in [notBefore, olderThan) with no terminal event, in a
	// session that has been completely quiet since quietSince. The quiet
	// predicate is the false-positive guard — a turn legitimately queued
	// behind a long-running turn, or itself mid-work, lives in a session that
	// keeps producing events; a session with zero events for the whole quiet
	// window cannot be making progress on anything. Progressed reports
	// whether the runner ever claimed/started the turn, so the sweep can
	// apply a longer age floor to mid-turn strands than to never-claimed
	// ones. Backs cmd/tank-operator/stranded_turn_sweep.go.
	//
	// AskUserQuestion turns are NOT strands, ever, and are excluded by the
	// pause-linkage predicate. The system creates three legitimate
	// terminal-less turn shapes (the 2026-06-12 incident: the sweep's first
	// day in production wrote false turn.command_failed terminals onto all
	// three, destroying pending questions and corrupting healthy
	// transcripts):
	//
	//   1. the synthetic question shell (turn_question-*): the runner
	//      publishes turn.submitted + turn.awaiting_input riding the
	//      question turn id, and the /answer handler later adds
	//      turn.input_answered there. It is never claimed and never
	//      receives a terminal — by design.
	//   2. the asking turn paused on the user: it stays claimed/started
	//      with no terminal until the human answers, which can
	//      legitimately take days. The awaiting_input payload links it via
	//      asking_turn_id.
	//   3. the answered asking turn: the runner rotates the live turn to
	//      the answer's nonce (turn_answer-*) and the terminal lands under
	//      the rotated id; the original asking turn's closure IS the
	//      input_answered + rotation, not a terminal of its own.
	//
	// A turn linked to any turn.awaiting_input / turn.input_answered row —
	// riding it as turn_id, or referenced by the payload's asking_turn_id /
	// question_turn_id — is therefore permanently outside the sweep's
	// model. The strandable identity after an answer is the rotated
	// continuation turn (turn_answer-*), which carries its own
	// turn.submitted: if the input_reply command is lost, THAT turn is
	// correctly found (never claimed); if the rotated turn dies mid-work
	// with no follow-up question, it is correctly found (progressed, no
	// pause linkage of its own).
	FindStrandedTurns(ctx context.Context, olderThan, quietSince, notBefore time.Time, limit int) ([]StrandedTurn, error)
	// HasRecentRunnerEvent reports whether any exclusively-runner-produced
	// event (claimed/started/usage/awaiting_input, assistant messages,
	// items, shell tasks, compactions) landed in the ledger at or after
	// `since`, across all sessions in this store's scope. It is the
	// stranded-turn sweep's
	// pipeline-liveness gate: turn.submitted rows are written by the
	// backend directly over HTTP, so during a persister or session-bus
	// outage submits keep landing while runner progress does not — every
	// active session then looks "quiet" and the sweep would mass-fail
	// healthy in-flight turns exactly when the pipeline is recovering.
	// Backend-writable types (terminals, boundary events) deliberately do
	// not count as proof of life, so the sweep's own output can never
	// satisfy its own gate. Rides the session_events_created_at index; the
	// probed window is minutes wide, so the scan is bounded regardless of
	// ledger size.
	HasRecentRunnerEvent(ctx context.Context, since time.Time) (bool, error)
}

// StrandedLaunchTurn is one never-dispatched launch turn — exactly the fields
// needed to emit a durable turn.command_failed keyed to the same turn id and
// client nonce the launch user_message.created carried. SessionID is the
// public id; TankSessionID is the storage key (scope-qualified partition key).
type StrandedLaunchTurn struct {
	TankSessionID string
	SessionID     string
	TurnID        string
	ClientNonce   string
	Email         string
	Runtime       string
	CreatedAt     time.Time
}

// StrandedTurn is one dispatched-but-stranded turn: durably submitted, no
// terminal, in a fully quiet session. Source carries the submit's
// payload.source (e.g. "background-task", "schedule-wakeup") so the sweep can
// classify continuation strands as away-errors; Progressed reports whether a
// runner ever claimed/started the turn.
type StrandedTurn struct {
	TankSessionID string
	SessionID     string
	TurnID        string
	ClientNonce   string
	Email         string
	Runtime       string
	Source        string
	Progressed    bool
	CreatedAt     time.Time
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
	"turn.claimed",
	"turn.started",
	"turn.completed",
	"turn.failed",
	"turn.command_failed",
	"turn.interrupt_requested",
	"turn.interrupted",
	"turn.awaiting_input",
	"turn.input_answered",
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
}

// UnreadOutputTurnTypes are turn-level terminal events that count as
// unread output via their turn_id (no timeline_id on these). turn.awaiting_input
// is unread output: a pending question is work the user must act on.
var UnreadOutputTurnTypes = []string{
	"turn.failed",
	"turn.command_failed",
	"turn.interrupted",
	"turn.awaiting_input",
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

func (s *postgresSessionEventStore) Upsert(ctx context.Context, event map[string]any) (bool, error) {
	if err := conversation.ValidateEventMap(event); err != nil {
		return false, err
	}
	doc := cloneSessionEventMap(event)
	storageKey := stringField(doc, "tank_session_id")
	publicSessionID := stringField(doc, "session_id")
	if storageKey == "" {
		storageKey = sessionmodel.SessionStorageKey(s.scope, publicSessionID)
	}
	if storageKey == "" {
		return false, errMissingSessionEventField("tank_session_id")
	}
	id := stringField(doc, "id")
	if id == "" {
		id = stringField(doc, "uuid")
	}
	if id == "" {
		id = stringField(doc, "event_id")
	}
	if id == "" {
		return false, errMissingSessionEventField("id")
	}
	orderKey := stringField(doc, "order_key")
	if orderKey == "" {
		return false, errMissingSessionEventField("order_key")
	}
	doc["id"] = id
	doc["tank_session_id"] = storageKey
	if _, ok := doc["tank_public_session_id"]; !ok && publicSessionID != "" {
		doc["tank_public_session_id"] = publicSessionID
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	turnID := stringField(doc, "turn_id")
	eventType := stringField(doc, "type")

	// (xmax = 0) distinguishes a fresh insert (xmax 0) from a row the
	// ON CONFLICT branch overwrote (xmax = the updating transaction).
	// The DO UPDATE branch is kept deliberately: a producer republish
	// with the same order_key wins last-write so a runner retry that
	// enriched the payload is not silently dropped.
	const q = `
		INSERT INTO session_events (
			tank_session_id, order_key, event_id, turn_id, event_type, payload
		) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6)
		ON CONFLICT (tank_session_id, order_key) DO UPDATE
		SET event_id   = EXCLUDED.event_id,
			turn_id    = EXCLUDED.turn_id,
			event_type = EXCLUDED.event_type,
			payload    = EXCLUDED.payload
		RETURNING (xmax = 0)
	`
	inserted := false
	if err := s.pool.QueryRow(ctx, q, storageKey, orderKey, id, turnID, eventType, payload).Scan(&inserted); err != nil {
		// A different order_key carrying an already-stored event identity
		// is a rebuilt duplicate (replica-raced sweep terminal, repeated
		// interrupt request, runner re-publish outside the JetStream
		// dedupe window). The first durable observation is canonical;
		// report not-inserted so callers skip duplicate side effects,
		// exactly like the same-order_key ON CONFLICT path.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "session_events_event_identity" {
			sessionEventDuplicateIdentityTotal.Inc()
			return false, nil
		}
		return false, err
	}
	return inserted, nil
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
		// Producer regressions are caught and alerted at the WRITE path;
		// a stored row the current schema rejects (a retired event type
		// from an old session) is skipped + counted rather than poisoning
		// the whole session's reads — see decodeStoredSessionEvent.
		doc, ok := decodeStoredSessionEvent(payload, tankSessionID)
		if !ok {
			continue
		}
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

// EventsForTurnAfter returns the next ASC page of one turn's events strictly
// after afterOrderKey. Paging this to exhaustion reads a whole turn regardless
// of size — the basis for turn-activity pagination, which must never truncate
// the turn's terminal the way a single bounded EventsForTurn does.
func (s *postgresSessionEventStore) EventsForTurnAfter(ctx context.Context, tankSessionID, turnID, afterOrderKey string, limit int) (SessionEventPage, error) {
	return s.eventsForTurn(ctx, s.pool, tankSessionID, turnID, afterOrderKey, limit)
}

func (s *postgresSessionEventStore) EventsForTurnAfterTx(ctx context.Context, tx pgx.Tx, tankSessionID, turnID, afterOrderKey string, limit int) (SessionEventPage, error) {
	return s.eventsForTurn(ctx, tx, tankSessionID, turnID, afterOrderKey, limit)
}

func (s *postgresSessionEventStore) eventsForTurn(ctx context.Context, qx sessionEventQueryer, tankSessionID, turnID, afterOrderKey string, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	queryLimit := limit + 1
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND turn_id = $2
			AND order_key <> ''
			AND ($3 = '' OR order_key > $3)
		ORDER BY order_key ASC
		LIMIT $4
	`
	rows, err := qx.Query(ctx, q, storageKey, strings.TrimSpace(turnID), strings.TrimSpace(afterOrderKey), queryLimit)
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
		// Producer regressions are caught and alerted at the WRITE path;
		// a stored row the current schema rejects (a retired event type
		// from an old session) is skipped + counted rather than poisoning
		// the whole session's reads — see decodeStoredSessionEvent.
		doc, ok := decodeStoredSessionEvent(payload, tankSessionID)
		if !ok {
			continue
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return SessionEventPage{}, err
	}
	return sessionEventPageFromAscendingScan(out, limit, SessionEventCursor{AfterOrderKey: afterOrderKey}), nil
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
	// A terminal row the current schema rejects (legacy shape) cannot be
	// interpreted by current code: treat as no usable terminal, counted
	// by decodeStoredSessionEvent.
	doc, ok := decodeStoredSessionEvent(payload, tankSessionID)
	if !ok {
		return nil, nil
	}
	return doc, nil
}

// FindStrandedLaunchTurns scans for user_message.created rows whose turn_id
// has no sibling event of any kind, created in [notBefore, olderThan). The
// NOT EXISTS rides the (tank_session_id, turn_id, order_key) index, and the
// created_at predicates ride the session_events_created_at index, so the
// outer pass is a bounded time-window scan rather than a whole-ledger fold.
// Scope-gated since 2026-06-12: the original "cross-session by design (no
// tank_session_id filter)" shape let ANY orchestrator sharing the database
// sweep EVERY scope's turns — and test-slot orchestrators run arbitrary
// branch code against the shared prod Postgres. The hazard was filed as
// issue #1079 item 4 and reproduced live within the hour: two pre-fix slot
// orchestrators re-wrote the #1069 false terminals onto prod sessions the
// moment migration 0150 cleaned them. Each orchestrator now sweeps only
// sessions in its own scope (default scope owns the bare-id storage keys;
// slot scopes own their 'scope:' prefix).
func (s *postgresSessionEventStore) FindStrandedLaunchTurns(ctx context.Context, olderThan, notBefore time.Time, limit int) ([]StrandedLaunchTurn, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT
			e.tank_session_id,
			COALESCE(e.payload ->> 'session_id', '')   AS session_id,
			e.turn_id,
			COALESCE(e.payload ->> 'client_nonce', '') AS client_nonce,
			COALESCE(e.payload ->> 'email', '')        AS email,
			COALESCE(e.payload ->> 'runtime', '')      AS runtime,
			e.created_at
		FROM session_events e
		WHERE e.event_type = $1
			AND e.turn_id IS NOT NULL
			AND e.created_at < $2
			AND e.created_at >= $3
			AND (
				($5 = 'default' AND strpos(e.tank_session_id, ':') = 0)
				OR ($5 <> 'default' AND e.tank_session_id LIKE $5 || ':%')
			)
			AND NOT EXISTS (
				SELECT 1
				FROM session_events o
				WHERE o.tank_session_id = e.tank_session_id
					AND o.turn_id = e.turn_id
					AND o.event_id <> e.event_id
			)
		ORDER BY e.created_at ASC
		LIMIT $4
	`
	rows, err := s.pool.Query(ctx, q,
		string(conversation.EventUserMessageCreated),
		olderThan.UTC(),
		notBefore.UTC(),
		limit,
		s.scope,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]StrandedLaunchTurn, 0, limit)
	for rows.Next() {
		var row StrandedLaunchTurn
		if err := rows.Scan(
			&row.TankSessionID,
			&row.SessionID,
			&row.TurnID,
			&row.ClientNonce,
			&row.Email,
			&row.Runtime,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FindStrandedTurns scans for turn.submitted rows in [notBefore, olderThan)
// whose turn has no terminal event of any kind, no AskUserQuestion pause
// linkage (see the interface doc for the three legitimate terminal-less turn
// shapes), and whose session has been completely silent since quietSince.
// The created_at predicates ride the session_events_created_at index, the
// per-turn / per-session NOT EXISTS subqueries ride the
// (tank_session_id, turn_id, order_key) and (tank_session_id,
// created_at-capable) paths, and the pause-linkage NOT EXISTS rides the
// session_events_input_pause partial index (a handful of rows per session at
// most), so the outer pass is a bounded time-window scan. Cross-session
// within this store's scope ONLY — the original cross-scope shape gave
// test-slot orchestrators (arbitrary branch code, shared prod Postgres)
// write authority over prod turn terminals; issue #1079 item 4 reproduced
// live on 2026-06-12 when two pre-#1069 slot orchestrators re-wrote the
// cleaned false terminals within minutes of migration 0150 deleting them.
func (s *postgresSessionEventStore) FindStrandedTurns(ctx context.Context, olderThan, quietSince, notBefore time.Time, limit int) ([]StrandedTurn, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT
			e.tank_session_id,
			COALESCE(e.payload ->> 'session_id', '')             AS session_id,
			e.turn_id,
			COALESCE(e.payload ->> 'client_nonce', '')           AS client_nonce,
			COALESCE(e.payload ->> 'email', '')                  AS email,
			COALESCE(e.payload ->> 'runtime', '')                AS runtime,
			COALESCE(e.payload -> 'payload' ->> 'source', '')    AS source,
			EXISTS (
				SELECT 1
				FROM session_events p
				WHERE p.tank_session_id = e.tank_session_id
					AND p.turn_id = e.turn_id
					AND p.event_type IN ('turn.claimed', 'turn.started')
			) AS progressed,
			e.created_at
		FROM session_events e
		WHERE e.event_type = 'turn.submitted'
			AND e.turn_id IS NOT NULL
			AND e.created_at < $1
			AND e.created_at >= $2
			AND (
				($5 = 'default' AND strpos(e.tank_session_id, ':') = 0)
				OR ($5 <> 'default' AND e.tank_session_id LIKE $5 || ':%')
			)
			AND NOT EXISTS (
				SELECT 1
				FROM session_events t
				WHERE t.tank_session_id = e.tank_session_id
					AND t.turn_id = e.turn_id
					AND t.event_type IN ('turn.completed', 'turn.failed', 'turn.command_failed', 'turn.interrupted')
			)
			AND NOT EXISTS (
				SELECT 1
				FROM session_events pause
				WHERE pause.tank_session_id = e.tank_session_id
					AND pause.event_type IN ('turn.awaiting_input', 'turn.input_answered')
					AND (
						pause.turn_id = e.turn_id
						OR pause.payload -> 'payload' ->> 'asking_turn_id' = e.turn_id
						OR pause.payload -> 'payload' ->> 'question_turn_id' = e.turn_id
					)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM session_events quiet
				WHERE quiet.tank_session_id = e.tank_session_id
					AND quiet.created_at >= $3
			)
		ORDER BY e.created_at ASC
		LIMIT $4
	`
	rows, err := s.pool.Query(ctx, q,
		olderThan.UTC(),
		notBefore.UTC(),
		quietSince.UTC(),
		limit,
		s.scope,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]StrandedTurn, 0, limit)
	for rows.Next() {
		var row StrandedTurn
		if err := rows.Scan(
			&row.TankSessionID,
			&row.SessionID,
			&row.TurnID,
			&row.ClientNonce,
			&row.Email,
			&row.Runtime,
			&row.Source,
			&row.Progressed,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// runnerProgressEventTypes is the exclusively-runner-produced event set that
// counts as proof the runner→JetStream→persister pipeline is moving. It
// deliberately omits every type the backend can write directly (turn
// terminals, turn.submitted, user_message.created, turn.input_answered,
// scheduled_wakeup.updated, session.status): during a persister outage those
// keep landing over HTTP while runner progress does not, and the sweep's own
// turn.command_failed output must never satisfy the sweep's own liveness
// gate. turn.interrupted is also omitted — it is runner-published today, but
// it is a terminal, and keeping terminals out of the liveness set keeps the
// invariant simple: progress, not closure, proves the pipeline.
var runnerProgressEventTypes = []string{
	"turn.claimed",
	"turn.started",
	"turn.usage",
	"turn.awaiting_input",
	"assistant_message.created",
	"item.started",
	"item.completed",
	"item.failed",
	"shell_task.started",
	"shell_task.updated",
	"shell_task.exited",
	"context.compacted",
}

func (s *postgresSessionEventStore) HasRecentRunnerEvent(ctx context.Context, since time.Time) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM session_events
			WHERE created_at >= $1
				AND event_type = ANY($2)
				AND (
					($3 = 'default' AND strpos(tank_session_id, ':') = 0)
					OR ($3 <> 'default' AND tank_session_id LIKE $3 || ':%')
				)
		)
	`
	var alive bool
	if err := s.pool.QueryRow(ctx, q, since.UTC(), runnerProgressEventTypes, s.scope).Scan(&alive); err != nil {
		return false, err
	}
	return alive, nil
}

// LatestLifecycleEvents returns up to `limit` recent lifecycle events
// (the turn.* lifecycle set, including turn.awaiting_input) for one session, ascending by
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
		// Producer regressions are caught and alerted at the WRITE path;
		// a stored row the current schema rejects (a retired event type
		// from an old session) is skipped + counted rather than poisoning
		// the whole session's reads — see decodeStoredSessionEvent.
		doc, ok := decodeStoredSessionEvent(payload, tankSessionID)
		if !ok {
			continue
		}
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

// CountContextCompactions counts every durable context.compacted event for a
// session. Served by the session_events_context_compacted partial index
// (event_type = 'context.compacted'), so it is an indexed range scan over only
// compaction rows — bounded regardless of how large the session's ledger has
// grown. The chat-activity emitter calls this on each compaction upsert to
// refresh the durable sessions.compaction_count projection; because it counts
// an append-only ledger, the result is monotonic and a redelivered event
// recomputes the same value.
func (s *postgresSessionEventStore) CountContextCompactions(ctx context.Context, tankSessionID string) (int64, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT count(*)
		FROM session_events
		WHERE tank_session_id = $1
			AND event_type = 'context.compacted'
	`
	var n int64
	if err := s.pool.QueryRow(ctx, q, storageKey).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ShellTaskEvents returns every durable shell_task.* event for a session in ASC
// order. Served by the session_events_shell_task partial index, so it is an
// indexed scan over only background-shell-task rows — bounded regardless of how
// large the session ledger has grown. This is the durable source the
// session-level Background screen projects, instead of re-reading the whole
// event ledger on each poll.
func (s *postgresSessionEventStore) ShellTaskEvents(ctx context.Context, tankSessionID string) ([]map[string]any, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT payload
		FROM session_events
		WHERE tank_session_id = $1
			AND event_type IN ('shell_task.started', 'shell_task.updated', 'shell_task.exited')
		ORDER BY order_key ASC
	`
	rows, err := s.pool.Query(ctx, q, storageKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		// Producer regressions are caught and alerted at the WRITE path;
		// a stored row the current schema rejects (a retired event type
		// from an old session) is skipped + counted rather than poisoning
		// the whole session's reads — see decodeStoredSessionEvent.
		doc, ok := decodeStoredSessionEvent(payload, tankSessionID)
		if !ok {
			continue
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountUserMessages counts every durable user_message.created event for a
// session — one per human back-and-forth. Served by the
// session_events_user_message_by_session partial index
// (event_type = 'user_message.created'), so it is an indexed range scan over
// only user-message rows, bounded regardless of how large the session's ledger
// has grown. The chat-activity emitter calls this on each user_message.created
// upsert to refresh the durable sessions.user_message_count projection; because
// it counts an append-only ledger, the result is monotonic and a redelivered
// event recomputes the same value. Background-task wake continuations carry
// their prompt on turn.submitted, not user_message.created, so they are excluded
// here exactly as the "user back-and-forth" semantics require.
func (s *postgresSessionEventStore) CountUserMessages(ctx context.Context, tankSessionID string) (int64, error) {
	storageKey := sessionmodel.SessionStorageKey(s.scope, tankSessionID)
	const q = `
		SELECT count(*)
		FROM session_events
		WHERE tank_session_id = $1
			AND event_type = 'user_message.created'
	`
	var n int64
	if err := s.pool.QueryRow(ctx, q, storageKey).Scan(&n); err != nil {
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

func (StubSessionEventStore) Upsert(_ context.Context, _ map[string]any) (bool, error) {
	return false, nil
}

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

func (StubSessionEventStore) EventsForTurnAfter(_ context.Context, _, _, _ string, _ int) (SessionEventPage, error) {
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

func (StubSessionEventStore) FindStrandedLaunchTurns(_ context.Context, _, _ time.Time, _ int) ([]StrandedLaunchTurn, error) {
	return nil, nil
}

func (StubSessionEventStore) FindStrandedTurns(_ context.Context, _, _, _ time.Time, _ int) ([]StrandedTurn, error) {
	return nil, nil
}

func (StubSessionEventStore) HasRecentRunnerEvent(_ context.Context, _ time.Time) (bool, error) {
	// No ledger, no proof of life. The sweep paths gate on a real store
	// before consulting this, so the stub value is never load-bearing.
	return false, nil
}

func (StubSessionEventStore) LatestLifecycleEvents(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) UnreadOutputCount(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

func (StubSessionEventStore) CountContextCompactions(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (StubSessionEventStore) ShellTaskEvents(_ context.Context, _ string) ([]map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) CountUserMessages(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// sessionEventReadRejectedTotal counts stored ledger rows the CURRENT schema
// cannot validate — overwhelmingly retired event types from old sessions
// (event types the schema has since retired). The read path skips such rows instead of
// failing the whole session's projection: before this, one retired-type row
// made a session permanently un-projectable (the session-288 resync failures
// during the tank-operator#1051 recovery). The WRITE path still hard-rejects
// invalid docs — producer regressions are caught there, counted by
// tank_session_event_persist_schema_rejected_total and alerted; this counter
// is the read-side visibility, expected nonzero only when ancient sessions
// are read.
var sessionEventReadRejectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_session_event_read_rejected_total",
		Help: "Stored session_events rows skipped on read because the current schema rejects them, labeled by bounded reason (invalid_json, schema_rejected).",
	},
	[]string{"reason"},
)

// tank_session_event_duplicate_identity_total — inserts dropped by the
// session_events_event_identity unique index (migration 0151): a writer
// rebuilt an event that already exists under the same
// (tank_session_id, event_id) with a different order_key. This is the
// at-least-once fabric working as designed — replica-concurrent sweep
// terminals, repeated interrupt requests, runner-restart re-publishes
// across the JetStream dedupe window — and the first durable observation
// stays canonical. A sustained rate from a single writer means that
// writer is rebuilding events it should be reading.
var sessionEventDuplicateIdentityTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "tank_session_event_duplicate_identity_total",
		Help: "session_events inserts dropped because the event identity (tank_session_id, event_id) already exists under a different order_key.",
	},
)

// decodeStoredSessionEvent unmarshals and validates one stored ledger row.
// ok=false means the row is unusable under the current schema and the caller
// must skip it (counted + logged); the ledger row itself stays untouched.
func decodeStoredSessionEvent(payload []byte, tankSessionID string) (map[string]any, bool) {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		sessionEventReadRejectedTotal.WithLabelValues("invalid_json").Inc()
		slog.Warn("session-events row skipped on read: not JSON",
			"tank_session_id", tankSessionID, "error", err)
		return nil, false
	}
	if err := conversation.ValidateEventMap(doc); err != nil {
		sessionEventReadRejectedTotal.WithLabelValues("schema_rejected").Inc()
		slog.Warn("session-events row skipped on read: rejected by current schema",
			"tank_session_id", tankSessionID,
			"event_id", stringField(doc, "event_id"),
			"event_type", stringField(doc, "type"),
			"error", err)
		return nil, false
	}
	doc["tank_session_id"] = tankSessionID
	return doc, true
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
