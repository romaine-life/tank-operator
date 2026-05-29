package pgstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// spyMigrationMetrics records what the engine reported so the ledger contract
// (apply once, skip thereafter) can be asserted without re-observing the
// backfilled data directly.
type spyMigrationMetrics struct {
	mu      sync.Mutex
	pending int
	applied int
	skipped int
	failed  []string
}

func (s *spyMigrationMetrics) SetMigrationsPending(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = n
}
func (s *spyMigrationMetrics) RecordMigrationApplied(float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied++
}
func (s *spyMigrationMetrics) RecordMigrationSkipped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped++
}
func (s *spyMigrationMetrics) RecordMigrationFailed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, id)
}

func newLedgerTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_migration_ledger_%d", time.Now().UnixNano())
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
	return pool
}

// TestLedgerAppliesOnceThenSkips is the load-bearing regression guard for the
// crashloop this engine replaced: the previous engine re-ran every statement —
// including the full-table session-status and session_events backfills — on
// every boot under one shared timeout. The ledger must apply each migration
// exactly once and skip all of them on the next boot.
func TestLedgerAppliesOnceThenSkips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := newLedgerTestPool(t, ctx)

	first := &spyMigrationMetrics{}
	if err := RunMigrationsWithMetrics(ctx, pool, first); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if first.applied != len(schemaMigrations) {
		t.Fatalf("first run applied %d migrations, want %d", first.applied, len(schemaMigrations))
	}
	if first.skipped != 0 {
		t.Fatalf("first run skipped %d migrations, want 0", first.skipped)
	}
	if first.pending != len(schemaMigrations) {
		t.Fatalf("first run reported %d pending, want %d", first.pending, len(schemaMigrations))
	}

	var ledgerCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerCount != len(schemaMigrations) {
		t.Fatalf("ledger has %d rows after first run, want %d", ledgerCount, len(schemaMigrations))
	}

	second := &spyMigrationMetrics{}
	if err := RunMigrationsWithMetrics(ctx, pool, second); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	if second.applied != 0 {
		t.Fatalf("second run applied %d migrations, want 0 (all should be skipped)", second.applied)
	}
	if second.skipped != len(schemaMigrations) {
		t.Fatalf("second run skipped %d migrations, want %d", second.skipped, len(schemaMigrations))
	}
	if second.pending != 0 {
		t.Fatalf("second run reported %d pending, want 0", second.pending)
	}
}

// TestLedgerRejectsEditedMigration proves the immutability guard: editing an
// already-applied migration's SQL (instead of appending a new migration) must
// abort startup rather than silently diverge code from the live schema.
func TestLedgerRejectsEditedMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := newLedgerTestPool(t, ctx)

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("initial migration run: %v", err)
	}
	// Simulate an in-place edit of an applied migration by corrupting its
	// recorded checksum.
	if _, err := pool.Exec(ctx,
		"UPDATE schema_migrations SET checksum = 'tampered' WHERE id = $1",
		schemaMigrations[0].ID,
	); err != nil {
		t.Fatalf("tamper ledger checksum: %v", err)
	}

	err := RunMigrations(ctx, pool)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), schemaMigrations[0].ID) {
		t.Fatalf("error %q does not name the checksum mismatch and migration ID", err)
	}
}
