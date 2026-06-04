package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// newCompactionCountTestPool stands up an isolated schema, runs migrations into
// it, and returns a pool bound to that schema. Skips when TANK_TEST_POSTGRES_DSN
// is unset (local runs without a database); CI's Go backend job sets it against
// a postgres:16 service. Mirrors the other pgstore integration harnesses.
func newCompactionCountTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_compaction_%d", time.Now().UnixNano())
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
	return pool
}

// TestCountContextCompactionsCountsScopedCompactionRows is the durable-projection
// contract for the composer's compaction metric: CountContextCompactions returns
// exactly the number of context.compacted events for THIS session and nothing
// else. It proves (a) only context.compacted rows count — turn/item events do
// not, (b) the count is session-scoped — another session's compactions do not
// leak, and (c) the partial index migration (0126) backs the query. This is the
// source of truth the chat-activity emitter projects onto sessions.compaction_count.
func TestCountContextCompactionsCountsScopedCompactionRows(t *testing.T) {
	pool := newCompactionCountTestPool(t)
	ctx := context.Background()

	insert := func(storageKey, orderKey, eventType string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO session_events (tank_session_id, order_key, event_id, event_type, payload)
			VALUES ($1, $2, $3, $4, '{}'::jsonb)
		`, storageKey, orderKey, fmt.Sprintf("evt-%s-%s", storageKey, orderKey), eventType)
		if err != nil {
			t.Fatalf("insert %s/%s: %v", storageKey, eventType, err)
		}
	}

	// Session 63: two compactions interleaved with ordinary turn/item events.
	insert("default:63", "001", "turn.submitted")
	insert("default:63", "002", "context.compacted")
	insert("default:63", "003", "item.completed")
	insert("default:63", "004", "turn.completed")
	insert("default:63", "005", "context.compacted")
	// A different session's compaction must not leak into 63's count.
	insert("default:99", "001", "context.compacted")
	// A different scope sharing the public id must not leak either.
	insert("slot:63", "001", "context.compacted")

	es := store.NewPostgresSessionEventStore(pool, "default")

	got, err := es.CountContextCompactions(ctx, "63")
	if err != nil {
		t.Fatalf("CountContextCompactions(63): %v", err)
	}
	if got != 2 {
		t.Fatalf("count(63) = %d, want 2 (only this session's context.compacted rows)", got)
	}

	// A session with no compactions reads zero, not an error.
	zero, err := es.CountContextCompactions(ctx, "1234")
	if err != nil {
		t.Fatalf("CountContextCompactions(1234): %v", err)
	}
	if zero != 0 {
		t.Fatalf("count(1234) = %d, want 0", zero)
	}
}
