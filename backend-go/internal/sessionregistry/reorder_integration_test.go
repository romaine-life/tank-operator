package sessionregistry

// DSN-gated integration coverage for Store.Reorder (PUT /api/sessions/order).
//
// This is the guard the bug needed. Reorder persists the sidebar order with an
// UPDATE ... FROM unnest($3::text[], $4::bigint[]); an off-by-one in those
// placeholders ($4/$5) shipped a 500 on every reorder ("could not determine
// data type of parameter $3", SQLSTATE 42P18) because the hermetic suites
// (manager_test.go, auth_session_test.go) stub Reorder to return the input
// unchanged and never touch SQL. This test exercises the real query against the
// real migrations: it asserts the persisted sidebar_position ordering round
// trips through List, and that the complete-permutation contract rejects a
// partial or unknown-id order with ErrSessionOrderConflict.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func newDSNStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_reorder_%d", time.Now().UnixNano())
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
	return NewPostgresStore(pool, "default")
}

func seedVisibleSession(t *testing.T, ctx context.Context, store *Store, owner, id string) {
	t.Helper()
	rec := sessionmodel.SessionRecord{
		ID:            id,
		Email:         owner,
		Mode:          "claude_gui",
		Scope:         "default",
		PodName:       "session-" + id,
		Name:          id + " work",
		Visible:       true,
		AgentAvatarID: "av_" + id,
	}
	if err := store.Upsert(ctx, rec); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func listOrder(t *testing.T, ctx context.Context, store *Store, owner string) []string {
	t.Helper()
	list, err := store.List(ctx, owner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]string, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	return ids
}

func TestReorderPersistsSidebarOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := newDSNStore(t, ctx)
	owner := "user@example.com"

	// Seed a, b, c in that order. sidebar_position defaults to a rising sequence,
	// so the initial List (ORDER BY sidebar_position DESC) is the reverse: c,b,a.
	for _, id := range []string{"a", "b", "c"} {
		seedVisibleSession(t, ctx, store, owner, id)
	}
	if got := listOrder(t, ctx, store, owner); !equalIDs(got, []string{"c", "b", "a"}) {
		t.Fatalf("initial order = %v, want [c b a]", got)
	}

	// Reorder to a, b, c. This is the query that 500'd before the $3/$4 fix.
	want := []string{"a", "b", "c"}
	published, err := store.Reorder(ctx, owner, want)
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if len(published) != len(want) {
		t.Fatalf("Reorder published %d rows, want %d", len(published), len(want))
	}
	if got := listOrder(t, ctx, store, owner); !equalIDs(got, want) {
		t.Fatalf("order after Reorder = %v, want %v", got, want)
	}

	// A second reorder converges too (no stale-position drift).
	want2 := []string{"b", "a", "c"}
	if _, err := store.Reorder(ctx, owner, want2); err != nil {
		t.Fatalf("second Reorder: %v", err)
	}
	if got := listOrder(t, ctx, store, owner); !equalIDs(got, want2) {
		t.Fatalf("order after second Reorder = %v, want %v", got, want2)
	}
}

func TestReorderRejectsIncompletePermutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := newDSNStore(t, ctx)
	owner := "user@example.com"
	for _, id := range []string{"a", "b", "c"} {
		seedVisibleSession(t, ctx, store, owner, id)
	}

	// Missing one id: the complete-permutation contract rejects it instead of
	// letting a stale tab silently drop a row from the durable order.
	if _, err := store.Reorder(ctx, owner, []string{"a", "b"}); !errors.Is(err, sessionmodel.ErrSessionOrderConflict) {
		t.Fatalf("partial order err = %v, want ErrSessionOrderConflict", err)
	}
	// Unknown id of the right length: also a conflict.
	if _, err := store.Reorder(ctx, owner, []string{"a", "b", "zzz"}); !errors.Is(err, sessionmodel.ErrSessionOrderConflict) {
		t.Fatalf("unknown-id order err = %v, want ErrSessionOrderConflict", err)
	}
	// A real reorder still works after the rejected attempts (tx rolled back).
	if _, err := store.Reorder(ctx, owner, []string{"a", "b", "c"}); err != nil {
		t.Fatalf("Reorder after rejects: %v", err)
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
