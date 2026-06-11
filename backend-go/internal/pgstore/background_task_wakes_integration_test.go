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
		SessionScope:    "default",
		SessionID:       "63",
		OwnerEmail:      "user@example.com",
		Provider:        "claude",
		TaskID:          "task-1",
		TaskStatus:      "completed",
		Description:     "wake and continue",
		ObservedEventID: "evt-1",
		RegisteredAt:    time.Now().Add(-time.Minute),
	}
	row, outcome, err := store.Register(ctx, reg)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if outcome != BackgroundTaskWakeRegisterScheduled {
		t.Fatalf("outcome = %q, want scheduled", outcome)
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
	if row.Generation != 1 {
		t.Fatalf("generation = %d, want 1", row.Generation)
	}

	// Re-registering the same finished task must NOT create a second wake; it
	// refreshes the pending row's task facts.
	if _, outcome, err := store.Register(ctx, reg); err != nil || outcome != BackgroundTaskWakeRegisterPendingUpdated {
		t.Fatalf("re-register = (%q, %v), want pending_updated", outcome, err)
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

	row, _, err := store.Register(ctx, RegisterBackgroundTaskWakeRequest{
		SessionScope: "default",
		SessionID:    "63",
		OwnerEmail:   "user@example.com",
		Provider:     "claude",
		TaskID:       "task-2",
		TaskStatus:   "completed",
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

// TestPostgresBackgroundTaskWakeGenerationsRearmAfterPrematureFire pins the
// re-arm machinery end to end: a fired wake registered from observation A is
// re-armed by a DIFFERENT observation B (the real completion arriving after a
// premature fire), duplicates of either observation never create rows, the
// generation cap bounds a flapping observer, and failed/cancelled rows are
// never resurrected.
func TestPostgresBackgroundTaskWakeGenerationsRearmAfterPrematureFire(t *testing.T) {
	pool := newBackgroundTaskWakeTestPool(t)
	ctx := context.Background()
	store := NewBackgroundTaskWakeStore(pool, "default")

	reg := RegisterBackgroundTaskWakeRequest{
		SessionScope:    "default",
		SessionID:       "63",
		OwnerEmail:      "user@example.com",
		Provider:        "codex",
		TaskID:          "34882",
		TaskStatus:      "completed",
		Description:     "/bin/sh -lc 'sleep 60 && echo DONE'",
		ObservedEventID: "exit-premature",
		RegisteredAt:    time.Now().Add(-time.Minute),
	}
	gen1, outcome, err := store.Register(ctx, reg)
	if err != nil || outcome != BackgroundTaskWakeRegisterScheduled {
		t.Fatalf("gen1 register = (%q, %v)", outcome, err)
	}
	if err := store.MarkFired(ctx, gen1.WakeID, "turn_bgtask-34882"); err != nil {
		t.Fatalf("fire gen1: %v", err)
	}

	// Same observation again (runner retry / restart re-adoption): duplicate.
	if _, outcome, err := store.Register(ctx, reg); err != nil || outcome != BackgroundTaskWakeRegisterDuplicate {
		t.Fatalf("duplicate register = (%q, %v), want duplicate_observation", outcome, err)
	}

	// A NEW observation (the real completion) arms generation 2.
	reg.ObservedEventID = "exit-real"
	gen2, outcome, err := store.Register(ctx, reg)
	if err != nil || outcome != BackgroundTaskWakeRegisterRearmed {
		t.Fatalf("re-arm register = (%q, %v), want rearmed", outcome, err)
	}
	if gen2.Generation != 2 || gen2.WakeID == gen1.WakeID {
		t.Fatalf("gen2 = %+v, want generation 2 with fresh wake id", gen2)
	}
	if gen2.ClientNonce != "bgtask-34882-g2" {
		t.Fatalf("gen2 nonce = %q, want bgtask-34882-g2", gen2.ClientNonce)
	}
	if gen2.Status != BackgroundTaskWakeScheduled {
		t.Fatalf("gen2 status = %q, want scheduled", gen2.Status)
	}

	// Cap: fire gen2, arm gen3, fire it, then a 4th observation is capped.
	if err := store.MarkFired(ctx, gen2.WakeID, "turn_bgtask-34882-g2"); err != nil {
		t.Fatalf("fire gen2: %v", err)
	}
	reg.ObservedEventID = "exit-3"
	gen3, outcome, err := store.Register(ctx, reg)
	if err != nil || outcome != BackgroundTaskWakeRegisterRearmed || gen3.Generation != 3 {
		t.Fatalf("gen3 register = (%q, gen %d, %v)", outcome, gen3.Generation, err)
	}
	if err := store.MarkFired(ctx, gen3.WakeID, "turn_bgtask-34882-g3"); err != nil {
		t.Fatalf("fire gen3: %v", err)
	}
	reg.ObservedEventID = "exit-4"
	if _, outcome, err := store.Register(ctx, reg); err != nil || outcome != BackgroundTaskWakeRegisterGenerationCapped {
		t.Fatalf("capped register = (%q, %v), want generation_capped", outcome, err)
	}

	// Cancelled rows are never resurrected by later observations.
	cancelReg := reg
	cancelReg.TaskID = "55555"
	cancelReg.ObservedEventID = "exit-c1"
	c1, outcome, err := store.Register(ctx, cancelReg)
	if err != nil || outcome != BackgroundTaskWakeRegisterScheduled {
		t.Fatalf("cancel-case register = (%q, %v)", outcome, err)
	}
	cancelled, err := store.CancelPendingForTask(ctx, "default", "63", "55555", "delivered_mid_turn")
	if err != nil || cancelled != 1 {
		t.Fatalf("cancel pending for task = (%d, %v), want 1", cancelled, err)
	}
	if got, _, _ := store.Register(ctx, cancelReg); got.WakeID != c1.WakeID {
		t.Fatalf("post-cancel register touched a different row: %+v", got)
	}
	if _, outcome, err := store.Register(ctx, cancelReg); err != nil || outcome != BackgroundTaskWakeRegisterTerminalNoop {
		t.Fatalf("post-cancel register = (%q, %v), want terminal_noop", outcome, err)
	}
}
