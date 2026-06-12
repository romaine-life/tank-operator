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

// TestRewriteEpochAdvancesOnSessionReplace pins issue #1077 item 4's
// deletion guard: every wholesale ReplaceForSession bumps the session's
// rewrite epoch (migration 0156), and a never-rewritten session reads 0 —
// the contract the SSE stream's ghost-row resync rides.
func TestRewriteEpochAdvancesOnSessionReplace(t *testing.T) {
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
	schema := fmt.Sprintf("tank_epoch_%d", time.Now().UnixNano())
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
	defer pool.Close()

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	rows := store.NewPostgresSessionTranscriptRowStore(pool, "default")
	const sessionID = "epoch-63"

	epoch, err := rows.RewriteEpoch(ctx, sessionID)
	if err != nil {
		t.Fatalf("initial epoch: %v", err)
	}
	if epoch != 0 {
		t.Fatalf("never-rewritten session epoch = %d, want 0", epoch)
	}

	entry := map[string]any{
		"id":       "msg-1",
		"kind":     "message",
		"orderKey": "001",
	}
	for want := int64(1); want <= 3; want++ {
		if err := rows.ReplaceForSession(ctx, sessionID, []map[string]any{entry}); err != nil {
			t.Fatalf("replace %d: %v", want, err)
		}
		epoch, err = rows.RewriteEpoch(ctx, sessionID)
		if err != nil {
			t.Fatalf("epoch after replace %d: %v", want, err)
		}
		if epoch != want {
			t.Fatalf("epoch after replace %d = %d, want %d", want, epoch, want)
		}
	}
}
