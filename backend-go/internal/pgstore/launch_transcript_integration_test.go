package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestPostgresLaunchTranscriptOrdering(t *testing.T) {
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
	schema := fmt.Sprintf("tank_launch_%d", time.Now().UnixNano())
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

	sessionID := "launch-order"
	owner := "user@example.com"
	scope := "default"
	launchAt := time.Now().UTC().Truncate(time.Millisecond)
	requestedAt := launchAt.Add(2 * time.Millisecond)
	readyAt := launchAt.Add(10 * time.Millisecond)

	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (
			email, session_scope, session_id, mode, pod_name, name, visible,
			requested_at, created_at, updated_at, status
		) VALUES ($1, $2, $3, $4, $5, $3, true, $6, $6, $6, 'Pending')
	`, owner, scope, sessionID, sessionmodel.ClaudeGUIMode, "session-"+sessionID, requestedAt); err != nil {
		t.Fatalf("insert session row: %v", err)
	}

	eventStore := store.NewPostgresSessionEventStore(pool, scope)
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)
	_, events, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             owner,
		ClientNonce:       "turn-launch-order",
		Text:              "hello from launch",
		Message:           map[string]any{"role": "user", "content": "hello from launch"},
		Runtime:           "claude",
		Now:               launchAt,
	})
	if err != nil {
		t.Fatalf("build launch events: %v", err)
	}
	for i, event := range events {
		eventTime := launchAt.Add(time.Duration(i) * time.Millisecond)
		event["created_at"] = eventTime.Format(time.RFC3339Nano)
		event["written_at"] = eventTime.Format(time.RFC3339Nano)
		eventID, _ := event["event_id"].(string)
		event["order_key"] = fmt.Sprintf("%013d-%08d-%s", eventTime.UnixMilli(), i, eventID)
		if _, err := eventStore.Upsert(ctx, event); err != nil {
			t.Fatalf("upsert launch event %d: %v", i, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE sessions
		SET status = 'Active', ready_at = $4, updated_at = $4
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`, owner, scope, sessionID, readyAt); err != nil {
		t.Fatalf("mark session active: %v", err)
	}

	page, err := eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{}, 10)
	if err != nil {
		t.Fatalf("list session events: %v", err)
	}
	if len(page.Events) != 4 {
		t.Fatalf("events = %d, want 4; events=%#v", len(page.Events), page.Events)
	}
	wantTypes := []string{"user_message.created", "turn.submitted", "session.status", "session.status"}
	for i, want := range wantTypes {
		if got, _ := page.Events[i]["type"].(string); got != want {
			t.Fatalf("event[%d].type = %q, want %q; events=%#v", i, got, want, page.Events)
		}
	}
	for i, want := range map[int]string{2: "loading", 3: "ready"} {
		payload, ok := page.Events[i]["payload"].(map[string]any)
		if !ok {
			t.Fatalf("event[%d].payload = %#v", i, page.Events[i]["payload"])
		}
		if got, _ := payload["status"].(string); got != want {
			t.Fatalf("event[%d].payload.status = %q, want %q", i, got, want)
		}
	}
}
