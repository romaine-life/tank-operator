package sessionregistry

// DSN-gated integration coverage for the child→parent nesting pointer
// (sessions.parent_session_id, migration 0179). Pins the write-once round trip
// against the real migrations: Upsert stamps the pointer on create, Get and
// List read it back through the modified SELECT/scan, and a later re-upsert
// (the create-time created_at refresh, or any lifecycle write, which carries no
// pointer) preserves it because parent_session_id is absent from the ON CONFLICT
// update set. This is the durable edge that lets the sidebar nest a spawned
// child under its origin from the first snapshot/row-update instead of reflowing
// once the parent's spawned_sessions append lands.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestParentSessionIDRoundTrip(t *testing.T) {
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
	schema := fmt.Sprintf("tank_parent_ptr_%d", time.Now().UnixNano())
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

	child := sessionmodel.SessionRecord{
		ID:              "child-1",
		Email:           owner,
		Mode:            "claude_gui",
		Scope:           "default",
		PodName:         "session-child-1",
		Name:            "child work",
		Visible:         true,
		AgentAvatarID:   "av_child",
		ParentSessionID: "origin-9",
	}
	if err := store.Upsert(ctx, child); err != nil {
		t.Fatalf("upsert child: %v", err)
	}

	got, ok, err := store.Get(ctx, owner, "child-1")
	if err != nil || !ok {
		t.Fatalf("get child: ok=%v err=%v", ok, err)
	}
	if got.ParentSessionID != "origin-9" {
		t.Fatalf("Get parent_session_id = %q, want %q", got.ParentSessionID, "origin-9")
	}

	list, err := store.List(ctx, owner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed *sessionmodel.SessionRecord
	for i := range list {
		if list[i].ID == "child-1" {
			listed = &list[i]
		}
	}
	if listed == nil {
		t.Fatalf("List did not return child-1 (got %d rows)", len(list))
	}
	if listed.ParentSessionID != "origin-9" {
		t.Fatalf("List parent_session_id = %q, want %q", listed.ParentSessionID, "origin-9")
	}

	// A later re-upsert (status/created_at refresh) carries no pointer;
	// parent_session_id is write-once and must be preserved.
	refresh := child
	refresh.ParentSessionID = ""
	refresh.Status = "Active"
	if err := store.Upsert(ctx, refresh); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got2, ok, err := store.Get(ctx, owner, "child-1")
	if err != nil || !ok {
		t.Fatalf("get child after refresh: ok=%v err=%v", ok, err)
	}
	if got2.ParentSessionID != "origin-9" {
		t.Fatalf("after pointer-less re-upsert, parent_session_id = %q, want preserved %q",
			got2.ParentSessionID, "origin-9")
	}
	if got2.Status != "Active" {
		t.Fatalf("re-upsert did not apply status: got %q", got2.Status)
	}

	// A root session (no origin) stores an empty pointer.
	root := sessionmodel.SessionRecord{
		ID:            "root-1",
		Email:         owner,
		Mode:          "claude_gui",
		Scope:         "default",
		PodName:       "session-root-1",
		Name:          "root work",
		Visible:       true,
		AgentAvatarID: "av_root",
	}
	if err := store.Upsert(ctx, root); err != nil {
		t.Fatalf("upsert root: %v", err)
	}
	gotRoot, ok, err := store.Get(ctx, owner, "root-1")
	if err != nil || !ok {
		t.Fatalf("get root: ok=%v err=%v", ok, err)
	}
	if gotRoot.ParentSessionID != "" {
		t.Fatalf("root parent_session_id = %q, want empty", gotRoot.ParentSessionID)
	}
}
