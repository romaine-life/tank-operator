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
		SELECT id, kind, name, crop, created_by, created_at, updated_at,
			avatar_mime, coalesce(avatar_blob_key, ''),
			backing_mime, coalesce(backing_blob_key, '')
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

func (s *AvatarAssetStore) Get(ctx context.Context, id string) (avatarassets.Metadata, error) {
	const q = `
		SELECT id, kind, name, crop, created_by, created_at, updated_at,
			avatar_mime, coalesce(avatar_blob_key, ''),
			backing_mime, coalesce(backing_blob_key, '')
		FROM avatar_assets
		WHERE id = $1 AND deleted_at IS NULL
	`
	meta, err := scanAvatarMetadata(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return avatarassets.Metadata{}, avatarassets.ErrNotFound
		}
		return avatarassets.Metadata{}, err
	}
	return meta, nil
}

func (s *AvatarAssetStore) Create(ctx context.Context, asset avatarassets.NewAsset) (avatarassets.Metadata, error) {
	cropJSON, err := json.Marshal(asset.Crop)
	if err != nil {
		return avatarassets.Metadata{}, err
	}
	const q = `
		INSERT INTO avatar_assets (
			id, kind, name, avatar_mime, avatar_blob_key,
			backing_mime, backing_blob_key, crop, created_by, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		RETURNING id, kind, name, crop, created_by, created_at, updated_at,
			avatar_mime, coalesce(avatar_blob_key, ''),
			backing_mime, coalesce(backing_blob_key, '')
	`
	return scanAvatarMetadata(s.pool.QueryRow(
		ctx,
		q,
		asset.ID,
		asset.Kind,
		asset.Name,
		asset.AvatarMIME,
		asset.AvatarBlobKey,
		asset.BackingMIME,
		asset.BackingBlobKey,
		cropJSON,
		asset.CreatedBy,
	))
}

func (s *AvatarAssetStore) Ensure(ctx context.Context, asset avatarassets.NewAsset) error {
	cropJSON, err := json.Marshal(asset.Crop)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO avatar_assets (
			id, kind, name, avatar_mime, avatar_blob_key,
			backing_mime, backing_blob_key, crop, created_by, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (id) DO NOTHING
	`
	_, err = s.pool.Exec(
		ctx,
		q,
		asset.ID,
		asset.Kind,
		asset.Name,
		asset.AvatarMIME,
		asset.AvatarBlobKey,
		asset.BackingMIME,
		asset.BackingBlobKey,
		cropJSON,
		asset.CreatedBy,
	)
	return err
}

func (s *AvatarAssetStore) Delete(ctx context.Context, id string) (avatarassets.Metadata, error) {
	const q = `
		UPDATE avatar_assets
		SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id, kind, name, crop, created_by, created_at, updated_at,
			avatar_mime, coalesce(avatar_blob_key, ''),
			backing_mime, coalesce(backing_blob_key, '')
	`
	meta, err := scanAvatarMetadata(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return avatarassets.Metadata{}, avatarassets.ErrNotFound
		}
		return avatarassets.Metadata{}, err
	}
	return meta, nil
}

type LegacyAvatarAssetImages struct {
	ID             string
	AvatarMIME     string
	AvatarBytes    []byte
	AvatarBlobKey  string
	BackingMIME    string
	BackingBytes   []byte
	BackingBlobKey string
}

func (s *AvatarAssetStore) ListLegacyImages(ctx context.Context) ([]LegacyAvatarAssetImages, error) {
	const q = `
		SELECT id, avatar_mime, avatar_bytes, coalesce(avatar_blob_key, ''),
			backing_mime, backing_bytes, coalesce(backing_blob_key, '')
		FROM avatar_assets
		WHERE deleted_at IS NULL
		  AND (
			(avatar_blob_key IS NULL AND avatar_bytes IS NOT NULL)
			OR (backing_blob_key IS NULL AND backing_bytes IS NOT NULL)
		  )
		ORDER BY created_at ASC, id ASC
	`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LegacyAvatarAssetImages{}
	for rows.Next() {
		var legacy LegacyAvatarAssetImages
		if err := rows.Scan(
			&legacy.ID,
			&legacy.AvatarMIME,
			&legacy.AvatarBytes,
			&legacy.AvatarBlobKey,
			&legacy.BackingMIME,
			&legacy.BackingBytes,
			&legacy.BackingBlobKey,
		); err != nil {
			return nil, err
		}
		out = append(out, legacy)
	}
	return out, rows.Err()
}

func (s *AvatarAssetStore) MarkLegacyImagesMigrated(ctx context.Context, id, avatarKey, backingKey string) error {
	const q = `
		UPDATE avatar_assets
		SET avatar_blob_key = $2,
			backing_blob_key = $3,
			avatar_bytes = NULL,
			backing_bytes = NULL,
			updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
	`
	tag, err := s.pool.Exec(ctx, q, id, avatarKey, backingKey)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return avatarassets.ErrNotFound
	}
	return nil
}

func (s *AvatarAssetStore) EnsureBlobConstraints(ctx context.Context) error {
	const q = `
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'avatar_assets_avatar_blob_key_active_required'
				  AND conrelid = 'avatar_assets'::regclass
			) THEN
				ALTER TABLE avatar_assets
					ADD CONSTRAINT avatar_assets_avatar_blob_key_active_required
					CHECK (
						deleted_at IS NOT NULL
						OR (avatar_blob_key IS NOT NULL AND length(avatar_blob_key) BETWEEN 1 AND 512)
					);
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'avatar_assets_backing_blob_key_active_required'
				  AND conrelid = 'avatar_assets'::regclass
			) THEN
				ALTER TABLE avatar_assets
					ADD CONSTRAINT avatar_assets_backing_blob_key_active_required
					CHECK (
						deleted_at IS NOT NULL
						OR (backing_blob_key IS NOT NULL AND length(backing_blob_key) BETWEEN 1 AND 512)
					);
			END IF;
		END $$;
	`
	_, err := s.pool.Exec(ctx, q)
	return err
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
		&meta.AvatarMIME,
		&meta.AvatarBlobKey,
		&meta.BackingMIME,
		&meta.BackingBlobKey,
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
