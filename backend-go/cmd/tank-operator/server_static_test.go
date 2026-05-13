package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTankStaticFilePrefersOverride(t *testing.T) {
	base := t.TempDir()
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "index.html"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "index.html"), []byte("override"), 0644); err != nil {
		t.Fatal(err)
	}
	found, ok := tankStaticFile(tankStaticRootSet{override: override, base: base}, "index.html")
	if !ok {
		t.Fatal("expected static file")
	}
	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "override" {
		t.Fatalf("body=%q", string(body))
	}
}

func TestTankStaticFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := tankStaticFile(tankStaticRootSet{base: root}, "..", filepath.Base(outside)); ok {
		t.Fatal("expected traversal to be rejected")
	}
}
