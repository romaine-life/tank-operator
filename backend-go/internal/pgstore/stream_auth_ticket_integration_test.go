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
)

func TestPostgresStreamAuthTicketStore(t *testing.T) {
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
	schema := fmt.Sprintf("tank_stream_ticket_%d", time.Now().UnixNano())
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

	store := NewStreamAuthTicketStore(pool)
	if err := store.Create(ctx, StreamAuthTicket{
		Ticket:       "ticket-live",
		Sub:          "sub-user@example.com",
		Email:        "User@Example.COM",
		Name:         "User",
		Role:         "user",
		StreamKind:   "session-events",
		SessionScope: "slot-a",
		SessionID:    "152",
		ExpiresAt:    time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	ticket, err := store.Validate(ctx, " ticket-live ", "session-events", "slot-a", "152")
	if err != nil {
		t.Fatalf("validate ticket: %v", err)
	}
	if ticket.Email != "user@example.com" || ticket.StreamKind != "session-events" || ticket.SessionScope != "slot-a" || ticket.SessionID != "152" {
		t.Fatalf("validated ticket = %#v", ticket)
	}

	invalidCases := []struct {
		name    string
		token   string
		stream  string
		scope   string
		session string
	}{
		{name: "wrong stream", token: "ticket-live", stream: "session-list", scope: "slot-a"},
		{name: "wrong scope", token: "ticket-live", stream: "session-events", scope: "slot-b", session: "152"},
		{name: "wrong session", token: "ticket-live", stream: "session-events", scope: "slot-a", session: "153"},
		{name: "missing token", stream: "session-events", scope: "slot-a", session: "152"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Validate(ctx, tc.token, tc.stream, tc.scope, tc.session)
			if !errors.Is(err, ErrStreamAuthTicketInvalid) {
				t.Fatalf("Validate error = %v, want ErrStreamAuthTicketInvalid", err)
			}
		})
	}

	if err := store.Create(ctx, StreamAuthTicket{
		Ticket:       "ticket-expired",
		Sub:          "sub-user@example.com",
		Email:        "user@example.com",
		Role:         "user",
		StreamKind:   "session-list",
		SessionScope: "slot-a",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create expired ticket: %v", err)
	}
	_, err = store.Validate(ctx, "ticket-expired", "session-list", "slot-a", "")
	if !errors.Is(err, ErrStreamAuthTicketInvalid) {
		t.Fatalf("expired Validate error = %v, want ErrStreamAuthTicketInvalid", err)
	}
}
