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

var (
	ErrGitHubInstallStateInvalid       = errors.New("github install state invalid")
	ErrGitHubInstallStateEmailMismatch = errors.New("github install state email mismatch")
)

type GitHubInstallStateStore struct {
	pool *pgxpool.Pool
}

func NewGitHubInstallStateStore(pool *pgxpool.Pool) *GitHubInstallStateStore {
	return &GitHubInstallStateStore{pool: pool}
}

func (s *GitHubInstallStateStore) Create(ctx context.Context, state, email string, expiresAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("github install state store not configured")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if state == "" || email == "" || expiresAt.IsZero() {
		return fmt.Errorf("github install state: state, email, and expiresAt are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("github install state: begin create: %w", err)
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, `DELETE FROM github_install_states WHERE expires_at < now() - interval '1 day'`)
	tag, err := tx.Exec(ctx, `
		INSERT INTO github_install_states (state, email, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (state) DO NOTHING
	`, state, email, expiresAt)
	if err != nil {
		return fmt.Errorf("github install state: insert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("github install state collision")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("github install state: commit create: %w", err)
	}
	return nil
}

func (s *GitHubInstallStateStore) AttachInstallation(ctx context.Context, state string, installationID int64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("github install state store not configured")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE github_install_states
		SET installation_id = $2, callback_at = now()
		WHERE state = $1
		  AND consumed_at IS NULL
		  AND expires_at > now()
	`, state, installationID)
	if err != nil {
		return fmt.Errorf("github install state: attach installation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrGitHubInstallStateInvalid
	}
	return nil
}

func (s *GitHubInstallStateStore) Consume(ctx context.Context, state, email string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("github install state store not configured")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("github install state: begin consume: %w", err)
	}
	defer tx.Rollback(ctx)

	var rowEmail string
	var installationID *int64
	var expiresAt time.Time
	var consumedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT email, installation_id, expires_at, consumed_at
		FROM github_install_states
		WHERE state = $1
		FOR UPDATE
	`, state).Scan(&rowEmail, &installationID, &expiresAt, &consumedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrGitHubInstallStateInvalid
		}
		return 0, fmt.Errorf("github install state: select consume: %w", err)
	}
	if consumedAt != nil || time.Now().After(expiresAt) || installationID == nil {
		return 0, ErrGitHubInstallStateInvalid
	}

	if _, err := tx.Exec(ctx, `
		UPDATE github_install_states
		SET consumed_at = now()
		WHERE state = $1
	`, state); err != nil {
		return 0, fmt.Errorf("github install state: mark consumed: %w", err)
	}
	if !strings.EqualFold(rowEmail, email) {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("github install state: commit mismatch consume: %w", err)
		}
		return 0, ErrGitHubInstallStateEmailMismatch
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("github install state: commit consume: %w", err)
	}
	return *installationID, nil
}
