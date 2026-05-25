package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/avataruploads"
)

type AvatarUploadAttemptStore struct {
	pool *pgxpool.Pool
}

func NewAvatarUploadAttemptStore(pool *pgxpool.Pool) *AvatarUploadAttemptStore {
	return &AvatarUploadAttemptStore{pool: pool}
}

func (s *AvatarUploadAttemptStore) Upsert(ctx context.Context, attempt avataruploads.Attempt) error {
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = time.Now().UTC()
	}
	if attempt.UpdatedAt.IsZero() {
		attempt.UpdatedAt = attempt.CreatedAt
	}
	fieldsJSON, err := json.Marshal(attempt.Fields)
	if err != nil {
		return err
	}
	diagnosticsJSON, err := json.Marshal(attempt.Diagnostics)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO avatar_upload_attempts (
			id, operation, actor_email, actor_role, method, route,
			content_type, content_type_class, content_length,
			stage, result, detail, kind, avatar_id,
			fields, diagnostics, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (id) DO UPDATE SET
			operation = EXCLUDED.operation,
			actor_email = EXCLUDED.actor_email,
			actor_role = EXCLUDED.actor_role,
			method = EXCLUDED.method,
			route = EXCLUDED.route,
			content_type = EXCLUDED.content_type,
			content_type_class = EXCLUDED.content_type_class,
			content_length = EXCLUDED.content_length,
			stage = EXCLUDED.stage,
			result = EXCLUDED.result,
			detail = EXCLUDED.detail,
			kind = EXCLUDED.kind,
			avatar_id = EXCLUDED.avatar_id,
			fields = EXCLUDED.fields,
			diagnostics = EXCLUDED.diagnostics,
			updated_at = EXCLUDED.updated_at
	`
	_, err = s.pool.Exec(
		ctx,
		q,
		attempt.ID,
		attempt.Operation,
		attempt.ActorEmail,
		attempt.ActorRole,
		attempt.Method,
		attempt.Route,
		attempt.ContentType,
		attempt.ContentTypeClass,
		attempt.ContentLength,
		attempt.Stage,
		attempt.Result,
		attempt.Detail,
		attempt.Kind,
		attempt.AvatarID,
		fieldsJSON,
		diagnosticsJSON,
		attempt.CreatedAt,
		attempt.UpdatedAt,
	)
	return err
}

func (s *AvatarUploadAttemptStore) Get(ctx context.Context, id string) (avataruploads.Attempt, error) {
	const q = `
		SELECT id, operation, actor_email, actor_role, method, route,
			content_type, content_type_class, content_length,
			stage, result, detail, kind, avatar_id,
			fields, diagnostics, created_at, updated_at
		FROM avatar_upload_attempts
		WHERE id = $1
	`
	attempt, err := scanAvatarUploadAttempt(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return avataruploads.Attempt{}, avataruploads.ErrNotFound
		}
		return avataruploads.Attempt{}, err
	}
	return attempt, nil
}

func (s *AvatarUploadAttemptStore) List(ctx context.Context, filter avataruploads.Filter) ([]avataruploads.Attempt, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	const q = `
		SELECT id, operation, actor_email, actor_role, method, route,
			content_type, content_type_class, content_length,
			stage, result, detail, kind, avatar_id,
			fields, diagnostics, created_at, updated_at
		FROM avatar_upload_attempts
		WHERE ($1 = '' OR id = $1)
		ORDER BY created_at DESC
		LIMIT $2
	`
	rows, err := s.pool.Query(ctx, q, filter.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []avataruploads.Attempt{}
	for rows.Next() {
		attempt, err := scanAvatarUploadAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

type avatarUploadAttemptScanner interface {
	Scan(dest ...any) error
}

func scanAvatarUploadAttempt(scanner avatarUploadAttemptScanner) (avataruploads.Attempt, error) {
	var attempt avataruploads.Attempt
	var fieldsJSON []byte
	var diagnosticsJSON []byte
	if err := scanner.Scan(
		&attempt.ID,
		&attempt.Operation,
		&attempt.ActorEmail,
		&attempt.ActorRole,
		&attempt.Method,
		&attempt.Route,
		&attempt.ContentType,
		&attempt.ContentTypeClass,
		&attempt.ContentLength,
		&attempt.Stage,
		&attempt.Result,
		&attempt.Detail,
		&attempt.Kind,
		&attempt.AvatarID,
		&fieldsJSON,
		&diagnosticsJSON,
		&attempt.CreatedAt,
		&attempt.UpdatedAt,
	); err != nil {
		return avataruploads.Attempt{}, err
	}
	if len(fieldsJSON) > 0 {
		if err := json.Unmarshal(fieldsJSON, &attempt.Fields); err != nil {
			return avataruploads.Attempt{}, err
		}
	}
	if len(diagnosticsJSON) > 0 {
		if err := json.Unmarshal(diagnosticsJSON, &attempt.Diagnostics); err != nil {
			return avataruploads.Attempt{}, err
		}
	}
	return attempt, nil
}
