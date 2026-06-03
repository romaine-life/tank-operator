package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// newTurnNumberTestPool provisions an isolated schema, runs all migrations, and
// returns a pool bound to it. Skips when TANK_TEST_POSTGRES_DSN is unset (local
// runs); CI's postgres:16 service sets it.
func newTurnNumberTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_turn_numbers_%d", time.Now().UnixNano())
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE")
		adminPool.Close()
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test dsn: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return ctx, pool
}

// insertTurnEvent mirrors the real EventStore.Upsert ON CONFLICT shape so the
// AFTER INSERT trigger fires on a genuine insert and is skipped on a
// re-delivery (ON CONFLICT DO UPDATE is an UPDATE, not an INSERT).
func insertTurnEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, storageKey, orderKey, turnID string) {
	t.Helper()
	eventID := turnID + ":" + orderKey
	payload := fmt.Sprintf(`{"turn_id":%q,"order_key":%q}`, turnID, orderKey)
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_events (tank_session_id, order_key, event_id, turn_id, event_type, payload)
		VALUES ($1, $2, $3, $4, 'user_message.created', $5::jsonb)
		ON CONFLICT (tank_session_id, order_key) DO UPDATE
		SET event_id   = EXCLUDED.event_id,
			turn_id    = EXCLUDED.turn_id,
			event_type = EXCLUDED.event_type,
			payload    = EXCLUDED.payload
	`, storageKey, orderKey, eventID, turnID, payload); err != nil {
		t.Fatalf("insert turn event: %v", err)
	}
}

func turnNumberFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, storageKey, turnID string) (int64, bool) {
	t.Helper()
	var n int64
	err := pool.QueryRow(ctx,
		`SELECT turn_number FROM session_turns WHERE tank_session_id = $1 AND turn_id = $2`,
		storageKey, turnID,
	).Scan(&n)
	if err == pgx.ErrNoRows {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read turn_number for %s: %v", turnID, err)
	}
	return n, true
}

func turnRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, storageKey string) int {
	t.Helper()
	var c int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_turns WHERE tank_session_id = $1`, storageKey,
	).Scan(&c); err != nil {
		t.Fatalf("count session_turns: %v", err)
	}
	return c
}

func nextTurnCounter(t *testing.T, ctx context.Context, pool *pgxpool.Pool, storageKey string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx,
		`SELECT next_turn_number FROM session_turn_counters WHERE tank_session_id = $1`, storageKey,
	).Scan(&n); err != nil {
		t.Fatalf("read next_turn_number: %v", err)
	}
	return n
}

func migrationSQLByID(t *testing.T, id string) string {
	t.Helper()
	for _, m := range schemaMigrations {
		if m.ID == id {
			return m.SQL
		}
	}
	t.Fatalf("migration %s not found", id)
	return ""
}

// TestTurnNumberTriggerAllocatesStablyAndInOrder proves the live trigger
// numbers a turn once on first sight (idempotent under re-delivery and
// additional same-turn events) and numbers distinct turns in submission
// (order_key) order.
func TestTurnNumberTriggerAllocatesStablyAndInOrder(t *testing.T) {
	ctx, pool := newTurnNumberTestPool(t)
	scope := "default"
	sessionID := "turns-trigger"
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)

	// Turn A's first event -> number 1.
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000001-00000000-turn-a:user", "turn-a")
	if n, ok := turnNumberFor(t, ctx, pool, storageKey, "turn-a"); !ok || n != 1 {
		t.Fatalf("turn-a number = %d ok=%v, want 1", n, ok)
	}

	// Re-deliver the exact same event (ON CONFLICT DO UPDATE -> no AFTER
	// INSERT) and add a later same-turn event: number must stay 1.
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000001-00000000-turn-a:user", "turn-a")
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000005-00000000-turn-a:done", "turn-a")
	if n, ok := turnNumberFor(t, ctx, pool, storageKey, "turn-a"); !ok || n != 1 {
		t.Fatalf("turn-a number after re-delivery = %d ok=%v, want stable 1", n, ok)
	}

	// Turn B's first event -> number 2 (submission order).
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000010-00000000-turn-b:user", "turn-b")
	if n, ok := turnNumberFor(t, ctx, pool, storageKey, "turn-b"); !ok || n != 2 {
		t.Fatalf("turn-b number = %d ok=%v, want 2", n, ok)
	}

	if got := turnRowCount(t, ctx, pool, storageKey); got != 2 {
		t.Fatalf("session_turns row count = %d, want 2", got)
	}
	if got := nextTurnCounter(t, ctx, pool, storageKey); got != 3 {
		t.Fatalf("next_turn_number = %d, want 3", got)
	}

	// A status event (turn_id NULL) must not be numbered.
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_events (tank_session_id, order_key, event_id, turn_id, event_type, payload)
		VALUES ($1, '0000000000002-00000000-status', 'status', NULL, 'session.status', '{}'::jsonb)
	`, storageKey); err != nil {
		t.Fatalf("insert status event: %v", err)
	}
	if got := turnRowCount(t, ctx, pool, storageKey); got != 2 {
		t.Fatalf("session_turns row count after status event = %d, want 2", got)
	}
}

// TestTurnNumberBackfillNumbersExistingTurnsByOrder proves the one-shot
// backfill (0090) numbers pre-existing turns by MIN(order_key) regardless of
// insertion order, primes the counter to max+1 (0091), is idempotent, and that
// the live trigger then continues from the primed counter.
func TestTurnNumberBackfillNumbersExistingTurnsByOrder(t *testing.T) {
	ctx, pool := newTurnNumberTestPool(t)
	scope := "default"
	sessionID := "turns-backfill"
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)

	// Simulate pre-feature data: disable the trigger and insert turns in an
	// order that does NOT match their order_key order, so the test pins
	// "numbered by MIN(order_key)" rather than "numbered by insert order".
	if _, err := pool.Exec(ctx, `ALTER TABLE session_events DISABLE TRIGGER tank_session_events_allocate_turn_number`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000003-00000000-turn-late:user", "turn-late")
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000001-00000000-turn-early:user", "turn-early")
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000002-00000000-turn-mid:user", "turn-mid")
	if got := turnRowCount(t, ctx, pool, storageKey); got != 0 {
		t.Fatalf("with trigger disabled, session_turns should be empty, got %d", got)
	}

	// Run the real backfill + counter-prime migration SQL.
	if _, err := pool.Exec(ctx, migrationSQLByID(t, "0091")); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, err := pool.Exec(ctx, migrationSQLByID(t, "0092")); err != nil {
		t.Fatalf("prime counters: %v", err)
	}

	for turnID, want := range map[string]int64{"turn-early": 1, "turn-mid": 2, "turn-late": 3} {
		if n, ok := turnNumberFor(t, ctx, pool, storageKey, turnID); !ok || n != want {
			t.Fatalf("%s number = %d ok=%v, want %d", turnID, n, ok, want)
		}
	}
	if got := nextTurnCounter(t, ctx, pool, storageKey); got != 4 {
		t.Fatalf("primed next_turn_number = %d, want 4", got)
	}

	// Backfill is idempotent: re-running changes nothing.
	if _, err := pool.Exec(ctx, migrationSQLByID(t, "0091")); err != nil {
		t.Fatalf("re-run backfill: %v", err)
	}
	if got := turnRowCount(t, ctx, pool, storageKey); got != 3 {
		t.Fatalf("session_turns row count after re-run = %d, want 3", got)
	}

	// Re-enable the trigger: a new turn continues from the primed counter.
	if _, err := pool.Exec(ctx, `ALTER TABLE session_events ENABLE TRIGGER tank_session_events_allocate_turn_number`); err != nil {
		t.Fatalf("re-enable trigger: %v", err)
	}
	insertTurnEvent(t, ctx, pool, storageKey, "0000000000009-00000000-turn-new:user", "turn-new")
	if n, ok := turnNumberFor(t, ctx, pool, storageKey, "turn-new"); !ok || n != 4 {
		t.Fatalf("turn-new number = %d ok=%v, want 4", n, ok)
	}
}
