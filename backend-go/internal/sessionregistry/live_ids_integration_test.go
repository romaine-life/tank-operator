package sessionregistry

// DSN-gated integration coverage for ListLiveIDsForScope — the
// orphan-consumer sweep's liveness source (issue #1077 item 6).
// Sessions rows are never hard-deleted, so the retired row-exists
// predicate (ListAllIDsForScope) classified every session id ever
// created as live and made the sweep permanently blind. These tests
// pin the replacement predicate against the real migrations:
//
//   - a visible row is live no matter how stale;
//   - a soft-deleted row stays live only while updated_at is within
//     the recency window (the in-flight-drain safety margin);
//   - a soft-deleted stale row finally falls out — the property that
//     lets the sweep identify orphans at all;
//   - MarkDeleted bumps updated_at in the same UPDATE that hides the
//     row, so a just-deleted session is still protected — the exact
//     interaction the 24h DefaultLiveSessionRecencyWindow exists for;
//   - rows in other scopes never leak in (consumer names encode scope,
//     and cross-scope orchestrators sweep only their own consumers).

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

func TestListLiveIDsForScope(t *testing.T) {
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
	schema := fmt.Sprintf("tank_live_ids_%d", time.Now().UnixNano())
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
	recent := now.Add(-1 * time.Hour)

	insertSession := func(id, scope string, visible bool, updatedAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO sessions (
				email, session_scope, session_id, mode, pod_name, name, visible,
				requested_at, created_at, updated_at, status
			) VALUES ($1, $2, $3, 'claude_gui', 'session-' || $3, $3, $4, $5, $5, $5, 'Active')
		`, owner, scope, id, visible, updatedAt); err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}

	// vis-stale: visible row, untouched for 8 days — visible is live
	// unconditionally; staleness never evicts a row the user can see.
	insertSession("vis-stale", "default", true, stale)
	// invis-recent: soft-deleted an hour ago — inside the recency
	// window, so its draining consumers stay protected.
	insertSession("invis-recent", "default", false, recent)
	// invis-stale: soft-deleted 8 days ago — outside the window; the
	// orphan the old predicate could never surface.
	insertSession("invis-stale", "default", false, stale)
	// invis-edge-out: soft-deleted just past the 24h window boundary.
	insertSession("invis-edge-out", "default", false, now.Add(-25*time.Hour))
	// other-scope: visible in a different scope — never in this
	// store's set regardless of state.
	insertSession("other-scope", "slot-1", true, recent)

	assertLive := func(label string, got map[string]struct{}, want ...string) {
		t.Helper()
		wantSet := map[string]struct{}{}
		for _, id := range want {
			wantSet[id] = struct{}{}
		}
		for id := range wantSet {
			if _, ok := got[id]; !ok {
				t.Errorf("%s: live set missing %q (got %v)", label, id, got)
			}
		}
		for id := range got {
			if _, ok := wantSet[id]; !ok {
				t.Errorf("%s: live set has unexpected %q (want %v)", label, id, want)
			}
		}
	}

	live, err := store.ListLiveIDsForScope(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("ListLiveIDsForScope: %v", err)
	}
	assertLive("24h window", live, "vis-stale", "invis-recent")

	// updatedWithin <= 0 falls back to the 24h default — same set.
	liveDefault, err := store.ListLiveIDsForScope(ctx, 0)
	if err != nil {
		t.Fatalf("ListLiveIDsForScope(default window): %v", err)
	}
	assertLive("default window", liveDefault, "vis-stale", "invis-recent")

	// The delete-race property the recency union exists for: deleting
	// a session flips visible=false AND bumps updated_at=now() in one
	// UPDATE, so the freshly-deleted session stays in the live set —
	// its consumers get the full window to drain before the sweep may
	// touch them.
	if err := store.MarkDeleted(ctx, owner, "vis-stale"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	var visible bool
	if err := pool.QueryRow(ctx,
		`SELECT visible FROM sessions WHERE email=$1 AND session_scope='default' AND session_id='vis-stale'`,
		owner).Scan(&visible); err != nil {
		t.Fatalf("read vis-stale: %v", err)
	}
	if visible {
		t.Fatalf("MarkDeleted left vis-stale visible")
	}
	liveAfterDelete, err := store.ListLiveIDsForScope(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("ListLiveIDsForScope after MarkDeleted: %v", err)
	}
	assertLive("after MarkDeleted", liveAfterDelete, "vis-stale", "invis-recent")

	// Shrinking the window below invis-recent's age evicts it too,
	// pinning that the recency arm really is updated_at-driven (while
	// the just-deleted vis-stale, bumped to now(), survives).
	liveNarrow, err := store.ListLiveIDsForScope(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("ListLiveIDsForScope(30m): %v", err)
	}
	assertLive("30m window", liveNarrow, "vis-stale")
}
