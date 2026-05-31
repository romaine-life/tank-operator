package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrMessageLinkShareInvalid = errors.New("message link share invalid")

type MessageLinkShare struct {
	Token        string
	CreatedBy    string
	OwnerEmail   string
	SessionScope string
	SessionID    string
	TimelineID   string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

type MessageLinkShareStore struct {
	pool *pgxpool.Pool
}

func NewMessageLinkShareStore(pool *pgxpool.Pool) *MessageLinkShareStore {
	return &MessageLinkShareStore{pool: pool}
}

func (s *MessageLinkShareStore) Create(ctx context.Context, share MessageLinkShare) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("message link share store not configured")
	}
	share.Token = strings.TrimSpace(share.Token)
	share.CreatedBy = strings.ToLower(strings.TrimSpace(share.CreatedBy))
	share.OwnerEmail = strings.ToLower(strings.TrimSpace(share.OwnerEmail))
	share.SessionScope = strings.TrimSpace(share.SessionScope)
	share.SessionID = strings.TrimSpace(share.SessionID)
	share.TimelineID = strings.TrimSpace(share.TimelineID)
	if share.Token == "" || share.CreatedBy == "" || share.OwnerEmail == "" || share.SessionScope == "" || share.SessionID == "" || share.TimelineID == "" {
		return fmt.Errorf("message link share: token, creator, owner, scope, session, and timeline are required")
	}

	tag, err := s.pool.Exec(ctx, `
		INSERT INTO message_link_shares (
			token_hash, created_by, owner_email, session_scope, session_id, timeline_id
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (token_hash) DO NOTHING
	`, messageLinkShareTokenHash(share.Token), share.CreatedBy, share.OwnerEmail, share.SessionScope, share.SessionID, share.TimelineID)
	if err != nil {
		return fmt.Errorf("message link share: insert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("message link share token collision")
	}
	return nil
}

func (s *MessageLinkShareStore) Get(ctx context.Context, token string) (MessageLinkShare, error) {
	if s == nil || s.pool == nil {
		return MessageLinkShare{}, fmt.Errorf("message link share store not configured")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return MessageLinkShare{}, ErrMessageLinkShareInvalid
	}

	var share MessageLinkShare
	var lastUsedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE message_link_shares
		SET last_used_at = now()
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		RETURNING created_by, owner_email, session_scope, session_id, timeline_id, created_at, last_used_at
	`, messageLinkShareTokenHash(token)).Scan(
		&share.CreatedBy,
		&share.OwnerEmail,
		&share.SessionScope,
		&share.SessionID,
		&share.TimelineID,
		&share.CreatedAt,
		&lastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MessageLinkShare{}, ErrMessageLinkShareInvalid
		}
		return MessageLinkShare{}, fmt.Errorf("message link share: get: %w", err)
	}
	share.Token = token
	share.LastUsedAt = lastUsedAt
	return share, nil
}

func messageLinkShareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
