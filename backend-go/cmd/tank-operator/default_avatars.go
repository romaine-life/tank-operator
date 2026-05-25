package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
)

type defaultAvatarAsset struct {
	id   string
	name string
	file string
}

var defaultAgentAvatarAssets = []defaultAvatarAsset{
	{id: "jp1-raptor", name: "Velociraptor", file: "jp1-raptor.png"},
	{id: "jp1-grant", name: "Dr. Alan Grant", file: "jp1-grant.png"},
	{id: "jp1-sattler", name: "Dr. Ellie Sattler", file: "jp1-sattler.png"},
	{id: "jp1-malcolm", name: "Dr. Ian Malcolm", file: "jp1-malcolm.png"},
	{id: "jp1-hammond", name: "John Hammond", file: "jp1-hammond.png"},
	{id: "jp1-nedry", name: "Dennis Nedry", file: "jp1-nedry.png"},
	{id: "jp1-muldoon", name: "Robert Muldoon", file: "jp1-muldoon.png"},
	{id: "jp1-arnold", name: "Ray Arnold", file: "jp1-arnold.png"},
}

func defaultAvatarAssetFile(id string) (string, bool) {
	for _, entry := range defaultAgentAvatarAssets {
		if entry.id == id {
			return entry.file, true
		}
	}
	return "", false
}

func seedDefaultAvatarAssets(ctx context.Context, store avatarassets.Store, images avatarassets.ImageStore, roots tankStaticRootSet) {
	if store == nil || images == nil {
		return
	}
	if !roots.enabled() {
		slog.Warn("default avatar asset seed skipped; static roots are unavailable")
		return
	}
	for _, entry := range defaultAgentAvatarAssets {
		path, ok := tankStaticFile(roots, "assets", "avatars", entry.file)
		if !ok {
			slog.Warn("default avatar asset not found", "id", entry.id, "file", entry.file)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("default avatar asset read failed", "id", entry.id, "path", path, "error", err)
			continue
		}
		key := defaultAvatarBlobKey(entry.id)
		if err := images.Put(ctx, key, avatarassets.Image{MIME: "image/png", Bytes: body}); err != nil {
			slog.Warn("default avatar asset image seed failed", "id", entry.id, "key", key, "error", err)
			continue
		}
		if err := store.Ensure(ctx, avatarassets.NewAsset{
			ID:             entry.id,
			Kind:           avatarassets.KindAgent,
			Name:           entry.name,
			Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
			AvatarMIME:     "image/png",
			AvatarBlobKey:  key,
			BackingMIME:    "image/png",
			BackingBlobKey: key,
			CreatedBy:      "tank-operator",
		}); err != nil {
			slog.Warn("default avatar asset seed failed", "id", entry.id, "error", err)
		}
	}
}
