package sessionregistry

// DSN-gated integration coverage for ClaimIdleForReap — the durable
// replacement for the per-replica in-memory idle reaper (issue #1079
// item 1). The whole reap predicate is one conditional UPDATE; these
// tests pin every guard arm against the real migrations: fresh rows,
// working-ish activity, pending scheduled wakeups, pending background
// wakes, and undispatched launch turns all defeat the claim, while a
// genuinely idle session is claimed exactly once (the claim hides the
// row, so a second pass — or the second replica — finds nothing).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

func TestClaimIdleForReap(t *testing.T) {
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_reap_%d", time.Now().UnixNano())
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
		t.Fatalf("parse dsn: %v", err)
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
	if err := pgstore.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	store := NewPostgresStore(pool, "default")
	owner := "user@example.com"
	now := time.Now().UTC()
	stale := now.Add(-8 * 24 * time.Hour)

	insertSession := func(id, activityStatus string, updatedAt time.Time) {
		t.Helper()
		summary := "NULL"
		args := []any{owner, id, "session-" + id, updatedAt}
		if activityStatus != "" {
			summary = "$5::jsonb"
			args = append(args, fmt.Sprintf(`{"status":%q,"last_order_key":"k"}`, activityStatus))
		}
		q := fmt.Sprintf(`
			INSERT INTO sessions (
				email, session_scope, session_id, mode, pod_name, name, visible,
				requested_at, created_at, updated_at, status, activity_summary
			) VALUES ($1, 'default', $2, 'claude_gui', $3, $2, true, $4, $4, $4, 'Active', %s)
		`, summary)
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}

	// idle1: stale, settled, unguarded — the reap target.
	insertSession("idle1", "ready", stale)
	// fresh1: settled but recently touched.
	insertSession("fresh1", "ready", now.Add(-time.Hour))
	// working1: stale but the durable status says the agent is mid-flight.
	insertSession("working1", "streaming", stale)
	// waked1: stale + settled, but a pending scheduled wakeup promises life.
	insertSession("waked1", "ready", stale)
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_scheduled_wakeups (
			wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, provider_item_id,
			scheduled_at, due_at, status
		) VALUES ('w1', 'default', 'waked1', 'default/waked1', $1,
			'claude', 'resume', 'n1', 'item1', $2, $2, 'scheduled')
	`, owner, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert wakeup: %v", err)
	}
	// bg1: stale + settled, but a pending background-task wake.
	insertSession("bg1", "ready", stale)
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_background_task_wakes (
			wake_id, session_scope, session_id, tank_session_id, owner_email,
			provider, task_id, prompt, client_nonce, registered_at, due_at, status
		) VALUES ('b1', 'default', 'bg1', 'default/bg1', $1,
			'claude', 'task1', 'report', 'n2', $2, $2, 'claiming')
	`, owner, now); err != nil {
		t.Fatalf("insert background wake: %v", err)
	}
	// launch1: stale + settled, but an undispatched launch turn.
	insertSession("launch1", "ready", stale)
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_pending_launch_turns (
			tank_session_id, turn_id, session_scope, session_id, client_nonce,
			owner_email, runtime, base_prompt, status
		) VALUES ('default/launch1', 'turn_l1', 'default', 'launch1', 'n3',
			$1, 'claude', 'hello', 'awaiting_bytes')
	`, owner); err != nil {
		t.Fatalf("insert pending launch: %v", err)
	}
	// nostatus1: stale with NULL activity_summary (pre-activity sessions) —
	// reapable: no summary means nothing ever ran.
	insertSession("nostatus1", "", stale)

	cutoff := now.Add(-7 * 24 * time.Hour)
	claimed, err := store.ClaimIdleForReap(ctx, cutoff, 50)
	if err != nil {
		t.Fatalf("ClaimIdleForReap: %v", err)
	}
	got := map[string]ReapedSession{}
	for _, row := range claimed {
		got[row.SessionID] = row
	}
	if len(got) != 2 {
		t.Fatalf("claimed = %v, want exactly idle1 and nostatus1", got)
	}
	for _, want := range []string{"idle1", "nostatus1"} {
		row, ok := got[want]
		if !ok {
			t.Fatalf("expected %s claimed; got %v", want, got)
		}
		if row.Email != owner || row.PodName != "session-"+want {
			t.Fatalf("claimed row %s = %+v, want owner/pod populated", want, row)
		}
	}

	// The claim is durable: the rows are invisible now, so a second pass
	// (or the second replica racing the first) finds nothing.
	again, err := store.ClaimIdleForReap(ctx, cutoff, 50)
	if err != nil {
		t.Fatalf("second ClaimIdleForReap: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second claim = %v, want empty", again)
	}

	var visible bool
	if err := pool.QueryRow(ctx,
		`SELECT visible FROM sessions WHERE email=$1 AND session_scope='default' AND session_id='idle1'`,
		owner).Scan(&visible); err != nil {
		t.Fatalf("read idle1: %v", err)
	}
	if visible {
		t.Fatalf("claimed session still visible")
	}
	for _, untouched := range []string{"fresh1", "working1", "waked1", "bg1", "launch1"} {
		if err := pool.QueryRow(ctx,
			`SELECT visible FROM sessions WHERE email=$1 AND session_scope='default' AND session_id=$2`,
			owner, untouched).Scan(&visible); err != nil {
			t.Fatalf("read %s: %v", untouched, err)
		}
		if !visible {
			t.Fatalf("guarded session %s was claimed", untouched)
		}
	}
}
