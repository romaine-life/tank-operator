package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
)

func newUploadedAvatarBlobKey(id, variant, mime string) string {
	return fmt.Sprintf(
		"avatars/uploads/%s/%s-%s.%s",
		avatarBlobSafeSegment(id),
		avatarBlobSafeSegment(variant),
		auth.RandomHex(16),
		avatarBlobExtension(mime),
	)
}

func defaultAvatarBlobKey(id string) string {
	return "avatars/defaults/" + avatarBlobSafeSegment(id) + ".png"
}

func legacyAvatarBlobKey(id, variant, mime string) string {
	return fmt.Sprintf(
		"avatars/legacy/%s/%s.%s",
		avatarBlobSafeSegment(id),
		avatarBlobSafeSegment(variant),
		avatarBlobExtension(mime),
	)
}

func avatarBlobExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/avif":
		return "avif"
	case "image/bmp":
		return "bmp"
	default:
		return "bin"
	}
}

func avatarBlobSafeSegment(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	dash := false
	for _, r := range raw {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "asset"
	}
	return out
}

func migrateLegacyAvatarAssetImages(ctx context.Context, store *pgstore.AvatarAssetStore, images avatarassets.ImageStore) error {
	if store == nil || images == nil {
		return nil
	}
	legacyRows, err := store.ListLegacyImages(ctx)
	if err != nil {
		return err
	}
	if len(legacyRows) == 0 {
		return nil
	}
	slog.Info("migrating legacy avatar image bytes to blob storage", "count", len(legacyRows))
	for _, row := range legacyRows {
		avatarKey := strings.TrimSpace(row.AvatarBlobKey)
		backingKey := strings.TrimSpace(row.BackingBlobKey)
		uploadedAvatar := false
		if avatarKey == "" {
			if len(row.AvatarBytes) == 0 {
				return fmt.Errorf("legacy avatar asset %s has no avatar bytes to migrate", row.ID)
			}
			avatarKey = legacyAvatarBlobKey(row.ID, avatarassets.VariantAvatar, row.AvatarMIME)
			if err := images.Put(ctx, avatarKey, avatarassets.Image{MIME: row.AvatarMIME, Bytes: row.AvatarBytes}); err != nil {
				return fmt.Errorf("migrate avatar image %s: %w", row.ID, err)
			}
			uploadedAvatar = true
		}
		if backingKey == "" {
			if len(row.BackingBytes) == 0 {
				return fmt.Errorf("legacy avatar asset %s has no backing bytes to migrate", row.ID)
			}
			if uploadedAvatar && row.AvatarMIME == row.BackingMIME && bytes.Equal(row.AvatarBytes, row.BackingBytes) {
				backingKey = avatarKey
			} else {
				backingKey = legacyAvatarBlobKey(row.ID, avatarassets.VariantBacking, row.BackingMIME)
				if err := images.Put(ctx, backingKey, avatarassets.Image{MIME: row.BackingMIME, Bytes: row.BackingBytes}); err != nil {
					return fmt.Errorf("migrate backing image %s: %w", row.ID, err)
				}
			}
		}
		if err := store.MarkLegacyImagesMigrated(ctx, row.ID, avatarKey, backingKey); err != nil {
			return fmt.Errorf("mark legacy avatar image %s migrated: %w", row.ID, err)
		}
	}
	slog.Info("legacy avatar image migration complete", "count", len(legacyRows))
	return nil
}

func cleanupAvatarImageKeys(ctx context.Context, images avatarassets.ImageStore, keys ...string) {
	if images == nil {
		return
	}
	seen := map[string]struct{}{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := images.Delete(ctx, key); err != nil && !errors.Is(err, avatarassets.ErrNotFound) {
			slog.Warn("avatar image blob cleanup failed", "key", key, "error", err)
		}
	}
}
