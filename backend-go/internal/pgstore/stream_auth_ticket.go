package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrStreamAuthTicketInvalid = errors.New("stream auth ticket invalid")

type StreamAuthTicket struct {
	Ticket       string
	Sub          string
	Email        string
	Name         string
	Role         string
	ActorEmail   string
	StreamKind   string
	SessionScope string
	SessionID    string
	ExpiresAt    time.Time
}

type StreamAuthTicketStore struct {
	pool *pgxpool.Pool
}

func NewStreamAuthTicketStore(pool *pgxpool.Pool) *StreamAuthTicketStore {
	return &StreamAuthTicketStore{pool: pool}
}

func (s *StreamAuthTicketStore) Create(ctx context.Context, ticket StreamAuthTicket) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("stream auth ticket store not configured")
	}
	ticket.Ticket = strings.TrimSpace(ticket.Ticket)
	ticket.Email = strings.ToLower(strings.TrimSpace(ticket.Email))
	ticket.StreamKind = strings.TrimSpace(ticket.StreamKind)
	ticket.SessionScope = strings.TrimSpace(ticket.SessionScope)
	ticket.SessionID = strings.TrimSpace(ticket.SessionID)
	if ticket.Ticket == "" || ticket.Email == "" || ticket.Role == "" || ticket.StreamKind == "" || ticket.SessionScope == "" || ticket.ExpiresAt.IsZero() {
		return fmt.Errorf("stream auth ticket: ticket, email, role, stream, scope, and expiresAt are required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("stream auth ticket: begin create: %w", err)
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, `DELETE FROM stream_auth_tickets WHERE expires_at < now() - interval '1 hour'`)
	tag, err := tx.Exec(ctx, `
		INSERT INTO stream_auth_tickets (
			ticket, sub, email, name, role, actor_email,
			stream_kind, session_scope, session_id, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (ticket) DO NOTHING
	`, ticket.Ticket, ticket.Sub, ticket.Email, ticket.Name, ticket.Role, ticket.ActorEmail,
		ticket.StreamKind, ticket.SessionScope, ticket.SessionID, ticket.ExpiresAt)
	if err != nil {
		return fmt.Errorf("stream auth ticket: insert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("stream auth ticket collision")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("stream auth ticket: commit create: %w", err)
	}
	return nil
}

func (s *StreamAuthTicketStore) Validate(ctx context.Context, token, streamKind, sessionScope, sessionID string) (StreamAuthTicket, error) {
	if s == nil || s.pool == nil {
		return StreamAuthTicket{}, fmt.Errorf("stream auth ticket store not configured")
	}
	token = strings.TrimSpace(token)
	streamKind = strings.TrimSpace(streamKind)
	sessionScope = strings.TrimSpace(sessionScope)
	sessionID = strings.TrimSpace(sessionID)
	if token == "" || streamKind == "" || sessionScope == "" {
		return StreamAuthTicket{}, ErrStreamAuthTicketInvalid
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StreamAuthTicket{}, fmt.Errorf("stream auth ticket: begin validate: %w", err)
	}
	defer tx.Rollback(ctx)

	var ticket StreamAuthTicket
	if err := tx.QueryRow(ctx, `
		SELECT ticket, sub, email, name, role, actor_email,
			stream_kind, session_scope, session_id, expires_at
		FROM stream_auth_tickets
		WHERE ticket = $1
		FOR UPDATE
	`, token).Scan(
		&ticket.Ticket,
		&ticket.Sub,
		&ticket.Email,
		&ticket.Name,
		&ticket.Role,
		&ticket.ActorEmail,
		&ticket.StreamKind,
		&ticket.SessionScope,
		&ticket.SessionID,
		&ticket.ExpiresAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StreamAuthTicket{}, ErrStreamAuthTicketInvalid
		}
		return StreamAuthTicket{}, fmt.Errorf("stream auth ticket: select validate: %w", err)
	}

	if time.Now().After(ticket.ExpiresAt) ||
		ticket.StreamKind != streamKind ||
		ticket.SessionScope != sessionScope ||
		strings.TrimSpace(ticket.SessionID) != sessionID {
		return StreamAuthTicket{}, ErrStreamAuthTicketInvalid
	}

	if _, err := tx.Exec(ctx, `
		UPDATE stream_auth_tickets
		SET last_used_at = now()
		WHERE ticket = $1
	`, token); err != nil {
		return StreamAuthTicket{}, fmt.Errorf("stream auth ticket: mark used: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return StreamAuthTicket{}, fmt.Errorf("stream auth ticket: commit validate: %w", err)
	}
	return ticket, nil
}
