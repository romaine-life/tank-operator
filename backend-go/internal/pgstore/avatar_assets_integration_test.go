package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
)

func TestPostgresAvatarAssetUpdateKindClearsUnusedDeckEntries(t *testing.T) {
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
	schema := fmt.Sprintf("tank_avatar_kind_%d", time.Now().UnixNano())
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

	store := NewAvatarAssetStore(pool)
	if _, err := store.Create(ctx, avatarassets.NewAsset{
		ID:             "av_kind_flip",
		Kind:           "system",
		Name:           "Flippable",
		Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
		AvatarMIME:     "image/png",
		AvatarBlobKey:  "avatars/av_kind_flip/avatar.png",
		BackingMIME:    "image/png",
		BackingBlobKey: "avatars/av_kind_flip/backing.png",
		CreatedBy:      "tester@example.com",
	}); err != nil {
		t.Fatalf("create avatar: %v", err)
	}

	insertEntry := `
		INSERT INTO avatar_deck_entries (
			email, session_scope, kind, cycle, position, avatar_id, used_session_id, used_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	// Two owners, one with an unused entry (must be cleaned), the other
	// with a used entry (historical, must stay).
	if _, err := pool.Exec(ctx, insertEntry, "alpha@example.com", "default", "system", 1, 1, "av_kind_flip", nil, nil); err != nil {
		t.Fatalf("insert unused system entry: %v", err)
	}
	usedAt := time.Now().UTC()
	if _, err := pool.Exec(ctx, insertEntry, "bravo@example.com", "default", "system", 1, 1, "av_kind_flip", "session-7", usedAt); err != nil {
		t.Fatalf("insert used system entry: %v", err)
	}
	// An agent-kind unused entry pointing at a different avatar should
	// survive the flip — only the OLD kind's unused entries for THIS
	// avatar should be removed.
	if _, err := store.Create(ctx, avatarassets.NewAsset{
		ID:             "av_other",
		Kind:           "agent",
		Name:           "Other",
		Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
		AvatarMIME:     "image/png",
		AvatarBlobKey:  "avatars/av_other/avatar.png",
		BackingMIME:    "image/png",
		BackingBlobKey: "avatars/av_other/backing.png",
		CreatedBy:      "tester@example.com",
	}); err != nil {
		t.Fatalf("create other avatar: %v", err)
	}
	if _, err := pool.Exec(ctx, insertEntry, "alpha@example.com", "default", "agent", 1, 1, "av_other", nil, nil); err != nil {
		t.Fatalf("insert agent entry: %v", err)
	}

	meta, err := store.UpdateKind(ctx, "av_kind_flip", "agent")
	if err != nil {
		t.Fatalf("update kind: %v", err)
	}
	if meta.Kind != "agent" {
		t.Fatalf("meta.Kind = %q, want agent", meta.Kind)
	}

	var unusedRemaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM avatar_deck_entries
		WHERE avatar_id = $1 AND kind = 'system' AND used_session_id IS NULL
	`, "av_kind_flip").Scan(&unusedRemaining); err != nil {
		t.Fatalf("count unused system entries: %v", err)
	}
	if unusedRemaining != 0 {
		t.Fatalf("expected unused system entries cleaned, got %d", unusedRemaining)
	}

	var usedRemaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM avatar_deck_entries
		WHERE avatar_id = $1 AND kind = 'system' AND used_session_id IS NOT NULL
	`, "av_kind_flip").Scan(&usedRemaining); err != nil {
		t.Fatalf("count used system entries: %v", err)
	}
	if usedRemaining != 1 {
		t.Fatalf("expected used system entry preserved, got %d", usedRemaining)
	}

	var otherRemaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM avatar_deck_entries
		WHERE avatar_id = 'av_other'
	`).Scan(&otherRemaining); err != nil {
		t.Fatalf("count other agent entries: %v", err)
	}
	if otherRemaining != 1 {
		t.Fatalf("expected unrelated avatar entry preserved, got %d", otherRemaining)
	}

	// Round-trip the kind back to system; the previously cleaned unused
	// entry should not magically reappear (cycle won't refill mid-life).
	if _, err := store.UpdateKind(ctx, "av_kind_flip", "system"); err != nil {
		t.Fatalf("flip back: %v", err)
	}
	var systemUnused int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM avatar_deck_entries
		WHERE avatar_id = $1 AND kind = 'system' AND used_session_id IS NULL
	`, "av_kind_flip").Scan(&systemUnused); err != nil {
		t.Fatalf("count system unused after round-trip: %v", err)
	}
	if systemUnused != 0 {
		t.Fatalf("expected no unused system entries after round-trip, got %d", systemUnused)
	}

	if _, err := store.UpdateKind(ctx, "av_kind_flip", "system"); !errors.Is(err, avatarassets.ErrKindUnchanged) {
		t.Fatalf("expected ErrKindUnchanged on no-op flip, got %v", err)
	}

	if _, err := store.UpdateKind(ctx, "av_missing", "agent"); !errors.Is(err, avatarassets.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing id, got %v", err)
	}
}
