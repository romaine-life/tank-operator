package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
)

func TestSeedDefaultAvatarAssets(t *testing.T) {
	root := t.TempDir()
	avatarDir := filepath.Join(root, "assets", "avatars")
	if err := os.MkdirAll(avatarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range defaultAgentAvatarAssets {
		if err := os.WriteFile(filepath.Join(avatarDir, entry.file), []byte("png:"+entry.id), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store := avatarassets.NewMemoryStore()
	seedDefaultAvatarAssets(context.Background(), store, tankStaticRootSet{base: root})

	metas, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != len(defaultAgentAvatarAssets) {
		t.Fatalf("seeded avatar count = %d, want %d", len(metas), len(defaultAgentAvatarAssets))
	}
	img, err := store.GetImage(context.Background(), "jp1-raptor", "avatar")
	if err != nil {
		t.Fatal(err)
	}
	backing, err := store.GetImage(context.Background(), "jp1-raptor", "backing")
	if err != nil {
		t.Fatal(err)
	}
	if img.MIME != "image/png" || backing.MIME != "image/png" {
		t.Fatalf("seeded MIME = %q/%q, want image/png", img.MIME, backing.MIME)
	}
	if !bytes.Equal(img.Bytes, backing.Bytes) {
		t.Fatal("seeded backing image should match avatar image")
	}
}
