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

// turnDirectoryTestPool spins up an isolated schema with the production
// migrations applied, mirroring the other transcript-row integration tests.
func turnDirectoryTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
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
	schema := fmt.Sprintf("tank_turn_directory_%d", time.Now().UnixNano())
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
	return pool, ctx
}

func shellEntry(id, turnID string, number int, orderKey string) map[string]any {
	return map[string]any{
		"id":         id,
		"kind":       "turn_activity",
		"turnId":     turnID,
		"turnNumber": number,
		"orderKey":   orderKey,
		"activity":   map[string]any{"status": "completed"},
	}
}

func TestListTurnDirectoryReturnsCompleteOrderedShells(t *testing.T) {
	pool, ctx := turnDirectoryTestPool(t)
	scope := "default"
	sessionID := "turn-directory-order"
	rowStore := store.NewPostgresSessionTranscriptRowStore(pool, scope)

	if err := rowStore.UpsertRows(ctx, sessionID, []map[string]any{
		shellEntry("turn_a", "turn_a", 1, "001"),
		// A non-turn_activity top-level row must be excluded from the directory.
		{"id": "msg_x", "kind": "message", "role": "assistant", "turnId": "turn_a", "orderKey": "0015"},
		shellEntry("turn_b", "turn_b", 2, "002"),
		shellEntry("turn_c", "turn_c", 3, "003"),
	}); err != nil {
		t.Fatalf("UpsertRows: %v", err)
	}

	page, err := rowStore.ListTurnDirectory(ctx, sessionID, store.TurnDirectoryMaxRows)
	if err != nil {
		t.Fatalf("ListTurnDirectory: %v", err)
	}
	if len(page.Shells) != 3 {
		t.Fatalf("len(shells) = %d, want 3 (message row must be excluded)", len(page.Shells))
	}
	wantOrder := []string{"turn_a", "turn_b", "turn_c"}
	for i, want := range wantOrder {
		if got, _ := page.Shells[i]["turnId"].(string); got != want {
			t.Fatalf("shells[%d].turnId = %q, want %q (ascending submission order)", i, got, want)
		}
		if kind, _ := page.Shells[i]["kind"].(string); kind != "turn_activity" {
			t.Fatalf("shells[%d].kind = %q, want turn_activity", i, kind)
		}
	}
	if page.LatestTurnNumber != 3 {
		t.Fatalf("latest_turn_number = %d, want 3", page.LatestTurnNumber)
	}
	if page.Truncated {
		t.Fatalf("truncated = true, want false")
	}
}

func TestListTurnDirectoryCapKeepsNewest(t *testing.T) {
	pool, ctx := turnDirectoryTestPool(t)
	scope := "default"
	sessionID := "turn-directory-cap"
	rowStore := store.NewPostgresSessionTranscriptRowStore(pool, scope)

	if err := rowStore.UpsertRows(ctx, sessionID, []map[string]any{
		shellEntry("turn_a", "turn_a", 1, "001"),
		shellEntry("turn_b", "turn_b", 2, "002"),
		shellEntry("turn_c", "turn_c", 3, "003"),
	}); err != nil {
		t.Fatalf("UpsertRows: %v", err)
	}

	page, err := rowStore.ListTurnDirectory(ctx, sessionID, 2)
	if err != nil {
		t.Fatalf("ListTurnDirectory: %v", err)
	}
	if !page.Truncated {
		t.Fatalf("truncated = false, want true at cap=2 with 3 turns")
	}
	if len(page.Shells) != 2 {
		t.Fatalf("len(shells) = %d, want 2", len(page.Shells))
	}
	// The newest turns survive the cap (the active/latest turn must stay
	// reachable); the oldest (turn_a) is elided. Returned ascending.
	if got, _ := page.Shells[0]["turnId"].(string); got != "turn_b" {
		t.Fatalf("shells[0].turnId = %q, want turn_b", got)
	}
	if got, _ := page.Shells[1]["turnId"].(string); got != "turn_c" {
		t.Fatalf("shells[1].turnId = %q, want turn_c", got)
	}
	if page.LatestTurnNumber != 3 {
		t.Fatalf("latest_turn_number = %d, want 3", page.LatestTurnNumber)
	}
}

func TestListTurnDirectoryEmptySession(t *testing.T) {
	pool, ctx := turnDirectoryTestPool(t)
	rowStore := store.NewPostgresSessionTranscriptRowStore(pool, "default")

	page, err := rowStore.ListTurnDirectory(ctx, "no-such-session", store.TurnDirectoryMaxRows)
	if err != nil {
		t.Fatalf("ListTurnDirectory: %v", err)
	}
	if len(page.Shells) != 0 || page.LatestTurnNumber != 0 || page.Truncated {
		t.Fatalf("empty page = %#v", page)
	}
}
