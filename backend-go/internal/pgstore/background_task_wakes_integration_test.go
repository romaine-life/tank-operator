package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newBackgroundTaskWakeTestPool stands up an isolated schema, runs migrations
// into it, and returns a pool bound to that schema. Skips when
// TANK_TEST_POSTGRES_DSN is unset (local runs without a database); CI's Go
// backend job sets it against a postgres:16 service.
func newBackgroundTaskWakeTestPool(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("tank_bgwake_%d", time.Now().UnixNano())
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

func TestPostgresBackgroundTaskWakeRegisterIsIdempotent(t *testing.T) {
	pool := newBackgroundTaskWakeTestPool(t)
	ctx := context.Background()
	store := NewBackgroundTaskWakeStore(pool, "default")

	reg := RegisterBackgroundTaskWakeRequest{
		SessionScope: "default",
		SessionID:    "63",
		OwnerEmail:   "user@example.com",
		Provider:     "claude",
		TaskID:       "task-1",
		TaskStatus:   "completed",
		Prompt:       "wake and continue",
		RegisteredAt: time.Now().Add(-time.Minute),
	}
	row, err := store.Register(ctx, reg)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if row.Status != BackgroundTaskWakeScheduled {
		t.Fatalf("status = %q, want scheduled", row.Status)
	}
	if row.ClientNonce != "bgtask-task-1" {
		t.Fatalf("client_nonce = %q, want bgtask-task-1", row.ClientNonce)
	}
	if row.WakeID == "" {
		t.Fatal("wake_id empty")
	}

	// Re-registering the same finished task must NOT create a second wake.
	if _, err := store.Register(ctx, reg); err != nil {
		t.Fatalf("re-register: %v", err)
	}

	claimed, err := store.ClaimDue(ctx, time.Now(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d rows, want 1 (idempotent register)", len(claimed))
	}
	got := claimed[0]
	if got.WakeID != row.WakeID || got.Status != BackgroundTaskWakeClaiming || got.AttemptCount != 1 {
		t.Fatalf("claimed row = %+v", got)
	}
	// No sessions row exists in this schema, so the liveness join COALESCEs to
	// "missing" defaults — fireBackgroundTaskWake treats that as session_not_found.
	if got.SessionStatus != "" || !got.SessionTerminated {
		t.Fatalf("session liveness defaults = (%q, %v), want (\"\", true)", got.SessionStatus, got.SessionTerminated)
	}

	if err := store.MarkFired(ctx, row.WakeID, "turn_bgtask-task-1"); err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	after, err := store.ClaimDue(ctx, time.Now(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("claim after fire: %v", err)
	}
	for _, r := range after {
		if r.WakeID == row.WakeID {
			t.Fatalf("fired wake was re-claimed: %+v", r)
		}
	}
}

func TestPostgresBackgroundTaskWakeReleaseRequeues(t *testing.T) {
	pool := newBackgroundTaskWakeTestPool(t)
	ctx := context.Background()
	store := NewBackgroundTaskWakeStore(pool, "default")

	row, err := store.Register(ctx, RegisterBackgroundTaskWakeRequest{
		SessionScope: "default",
		SessionID:    "63",
		OwnerEmail:   "user@example.com",
		Provider:     "claude",
		TaskID:       "task-2",
		Prompt:       "wake and continue",
		RegisteredAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := store.ClaimDue(ctx, time.Now(), 10, 2*time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Release returns the claimed wake to scheduled and undoes the claim's
	// attempt bump, so the soft-defer (awaiting-input) path can retry cleanly.
	if err := store.Release(ctx, row.WakeID); err != nil {
		t.Fatalf("release: %v", err)
	}
	reclaimed, err := store.ClaimDue(ctx, time.Now(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	var found *BackgroundTaskWake
	for i := range reclaimed {
		if reclaimed[i].WakeID == row.WakeID {
			found = &reclaimed[i]
		}
	}
	if found == nil {
		t.Fatal("released wake was not claimable again")
	}
	if found.AttemptCount != 1 {
		t.Fatalf("attempt_count after release+reclaim = %d, want 1", found.AttemptCount)
	}
}
