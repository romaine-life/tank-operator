package pgstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
)

type AvatarAssetStore struct {
	pool *pgxpool.Pool
}

func NewAvatarAssetStore(pool *pgxpool.Pool) *AvatarAssetStore {
	return &AvatarAssetStore{pool: pool}
}

func (s *AvatarAssetStore) List(ctx context.Context) ([]avatarassets.Metadata, error) {
	const q = `
		SELECT id, kind, name, crop, created_by, created_at, updated_at
		FROM avatar_assets
		WHERE deleted_at IS NULL
		ORDER BY kind ASC, created_at DESC, id ASC
	`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []avatarassets.Metadata{}
	for rows.Next() {
		meta, err := scanAvatarMetadata(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, meta)
	}
	return out, rows.Err()
}

func (s *AvatarAssetStore) Create(ctx context.Context, asset avatarassets.NewAsset) (avatarassets.Metadata, error) {
	cropJSON, err := json.Marshal(asset.Crop)
	if err != nil {
		return avatarassets.Metadata{}, err
	}
	const q = `
		INSERT INTO avatar_assets (
			id, kind, name, avatar_mime, avatar_bytes,
			backing_mime, backing_bytes, crop, created_by, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		RETURNING id, kind, name, crop, created_by, created_at, updated_at
	`
	return scanAvatarMetadata(s.pool.QueryRow(
		ctx,
		q,
		asset.ID,
		asset.Kind,
		asset.Name,
		asset.AvatarMIME,
		asset.AvatarBytes,
		asset.BackingMIME,
		asset.BackingBytes,
		cropJSON,
		asset.CreatedBy,
	))
}

func (s *AvatarAssetStore) GetImage(ctx context.Context, id, variant string) (avatarassets.Image, error) {
	var q string
	switch variant {
	case "avatar":
		q = `
			SELECT avatar_mime, avatar_bytes
			FROM avatar_assets
			WHERE id = $1 AND deleted_at IS NULL
		`
	case "backing":
		q = `
			SELECT backing_mime, backing_bytes
			FROM avatar_assets
			WHERE id = $1 AND deleted_at IS NULL
		`
	default:
		return avatarassets.Image{}, avatarassets.ErrNotFound
	}
	var img avatarassets.Image
	if err := s.pool.QueryRow(ctx, q, id).Scan(&img.MIME, &img.Bytes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return avatarassets.Image{}, avatarassets.ErrNotFound
		}
		return avatarassets.Image{}, err
	}
	return img, nil
}

func (s *AvatarAssetStore) Delete(ctx context.Context, id string) error {
	const q = `
		UPDATE avatar_assets
		SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
	`
	tag, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return avatarassets.ErrNotFound
	}
	return nil
}

type avatarMetadataScanner interface {
	Scan(dest ...any) error
}

func scanAvatarMetadata(row avatarMetadataScanner) (avatarassets.Metadata, error) {
	var (
		meta    avatarassets.Metadata
		cropRaw []byte
	)
	if err := row.Scan(
		&meta.ID,
		&meta.Kind,
		&meta.Name,
		&cropRaw,
		&meta.CreatedBy,
		&meta.CreatedAt,
		&meta.UpdatedAt,
	); err != nil {
		return avatarassets.Metadata{}, err
	}
	if len(cropRaw) > 0 {
		if err := json.Unmarshal(cropRaw, &meta.Crop); err != nil {
			return avatarassets.Metadata{}, err
		}
	}
	return meta, nil
}
