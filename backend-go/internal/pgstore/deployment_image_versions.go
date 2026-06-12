package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	DeploymentImageKindApp                = "app"
	DeploymentImageKindSessionClaude      = "session_claude"
	DeploymentImageKindSessionCodex       = "session_codex"
	DeploymentImageKindSessionAntigravity = "session_antigravity"
)

var deploymentImageKinds = map[string]struct{}{
	DeploymentImageKindApp:                {},
	DeploymentImageKindSessionClaude:      {},
	DeploymentImageKindSessionCodex:       {},
	DeploymentImageKindSessionAntigravity: {},
}

// ErrDeploymentImageVersionsUnavailable means the deployment_image_versions
// table is not present in this database yet. That is expected only in
// RUN_MIGRATIONS=false validation slots before the migration merges.
var ErrDeploymentImageVersionsUnavailable = errors.New("deployment image versions table unavailable")

// DeploymentImageVersion is one durable observation of an image ref and its
// release metadata by an orchestrator pod.
type DeploymentImageVersion struct {
	SessionScope string
	PodName      string
	ImageKind    string
	ImageRef     string
	Metadata     sessionmodel.ImageVersionMetadata
	ObservedAt   time.Time
}

type DeploymentImageVersionStore struct {
	pool *pgxpool.Pool
}

func NewDeploymentImageVersionStore(pool *pgxpool.Pool) *DeploymentImageVersionStore {
	return &DeploymentImageVersionStore{pool: pool}
}

func (s *DeploymentImageVersionStore) UpsertMany(ctx context.Context, records []DeploymentImageVersion) error {
	if len(records) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, record := range records {
		normalized, err := normalizeDeploymentImageVersion(record)
		if err != nil {
			return err
		}
		metadataJSON := []byte(`{}`)
		if len(normalized.Metadata) > 0 {
			metadataJSON, err = json.Marshal(normalized.Metadata)
			if err != nil {
				return fmt.Errorf("deployment image metadata marshal: %w", err)
			}
		}
		batch.Queue(`
			INSERT INTO deployment_image_versions (
				session_scope, pod_name, image_kind, image_ref, image_metadata, observed_at
			) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (session_scope, pod_name, image_kind) DO UPDATE
			SET image_ref      = EXCLUDED.image_ref,
				image_metadata = EXCLUDED.image_metadata,
				observed_at    = EXCLUDED.observed_at
		`,
			normalized.SessionScope,
			normalized.PodName,
			normalized.ImageKind,
			normalized.ImageRef,
			metadataJSON,
			normalized.ObservedAt,
		)
	}
	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range records {
		if _, err := results.Exec(); err != nil {
			if isUndefinedTableError(err) {
				return ErrDeploymentImageVersionsUnavailable
			}
			return err
		}
	}
	return nil
}

func (s *DeploymentImageVersionStore) LatestByScope(ctx context.Context, scope string) (map[string]DeploymentImageVersion, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	const q = `
		SELECT DISTINCT ON (image_kind)
			session_scope,
			pod_name,
			image_kind,
			image_ref,
			image_metadata,
			observed_at
		FROM deployment_image_versions
		WHERE session_scope = $1
		ORDER BY image_kind, observed_at DESC, pod_name DESC
	`
	rows, err := s.pool.Query(ctx, q, scope)
	if err != nil {
		if isUndefinedTableError(err) {
			return nil, ErrDeploymentImageVersionsUnavailable
		}
		return nil, err
	}
	defer rows.Close()

	out := map[string]DeploymentImageVersion{}
	for rows.Next() {
		var row DeploymentImageVersion
		var metadata []byte
		if err := rows.Scan(
			&row.SessionScope,
			&row.PodName,
			&row.ImageKind,
			&row.ImageRef,
			&metadata,
			&row.ObservedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = sessionmodel.DecodeImageVersionMetadata(metadata)
		out[row.ImageKind] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeDeploymentImageVersion(record DeploymentImageVersion) (DeploymentImageVersion, error) {
	record.SessionScope = strings.TrimSpace(record.SessionScope)
	if record.SessionScope == "" {
		record.SessionScope = "default"
	}
	record.PodName = strings.TrimSpace(record.PodName)
	record.ImageKind = strings.TrimSpace(record.ImageKind)
	if _, ok := deploymentImageKinds[record.ImageKind]; !ok {
		return DeploymentImageVersion{}, fmt.Errorf("unsupported deployment image kind %q", record.ImageKind)
	}
	record.ImageRef = strings.TrimSpace(record.ImageRef)
	record.Metadata = sessionmodel.NormalizeImageVersionMetadata(record.Metadata)
	if record.ObservedAt.IsZero() {
		record.ObservedAt = time.Now().UTC()
	} else {
		record.ObservedAt = record.ObservedAt.UTC()
	}
	return record, nil
}

func isUndefinedTableError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
