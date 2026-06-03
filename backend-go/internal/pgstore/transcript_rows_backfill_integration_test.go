package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestTranscriptRowBackfillIgnoresMigrationStatusRows(t *testing.T) {
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
	schema := fmt.Sprintf("tank_transcript_backfill_%d", time.Now().UnixNano())
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

	scope := "default"
	sessionID := "backfill-status-rows"
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)
	owner := "user@example.com"
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (
			email, session_scope, session_id, mode, pod_name, visible,
			requested_at, created_at, updated_at, status
		) VALUES ($1, $2, $3, $4, $5, true, $6, $6, $6, 'Pending')
	`, owner, scope, sessionID, sessionmodel.CodexGUIMode, "session-"+sessionID, now); err != nil {
		t.Fatalf("insert session row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE sessions
		SET status = 'Active', ready_at = $4, updated_at = $4
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`, owner, scope, sessionID, now.Add(time.Second)); err != nil {
		t.Fatalf("mark session active: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_events (
			tank_session_id, order_key, event_id, turn_id, event_type, payload
		) VALUES (
			$1, '9999999999999-00000002-turn-1:user', 'turn-1:user', 'turn-1', 'user_message.created',
			'{"event_id":"turn-1:user","order_key":"9999999999999-00000002-turn-1:user","session_id":"backfill-status-rows","tank_session_id":"backfill-status-rows","turn_id":"turn-1","timeline_id":"turn-1:user","client_nonce":"turn-1","type":"user_message.created","actor":"user","source":"tank","created_at":"2026-01-01T00:00:00Z","payload":{"text":"hello"}}'::jsonb
		)
	`, storageKey); err != nil {
		t.Fatalf("insert historical user event: %v", err)
	}

	var statusRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM session_transcript_rows
		WHERE tank_session_id = $1
	`, storageKey).Scan(&statusRows); err != nil {
		t.Fatalf("count status rows: %v", err)
	}
	if statusRows == 0 {
		t.Fatal("expected migration trigger to create status transcript rows")
	}

	rowStore := store.NewPostgresSessionTranscriptRowStore(pool, scope)
	needsBackfill, err := rowStore.NeedsBackfill(ctx, sessionID)
	if err != nil {
		t.Fatalf("NeedsBackfill before marker: %v", err)
	}
	if !needsBackfill {
		t.Fatal("NeedsBackfill before marker = false, want true")
	}

	if err := rowStore.ReplaceForSession(ctx, sessionID, []map[string]any{{
		"id":       "turn-1:user",
		"kind":     "message",
		"role":     "user",
		"text":     "hello",
		"orderKey": "9999999999999-00000002-turn-1:user",
		"turnId":   "turn-1",
	}}); err != nil {
		t.Fatalf("ReplaceForSession: %v", err)
	}
	needsBackfill, err = rowStore.NeedsBackfill(ctx, sessionID)
	if err != nil {
		t.Fatalf("NeedsBackfill after marker: %v", err)
	}
	if needsBackfill {
		t.Fatal("NeedsBackfill after marker = true, want false")
	}
}
