package sessions

import (
	"testing"
	"time"
)

func TestApplyLivePreviewPatch_PushReceiptMergesAndPreserves(t *testing.T) {
	existing := map[string]any{
		"active":       true,
		"url":          "https://tank-operator-slot-2.tank.dev.romaine.life",
		"slot_index":   2,
		"live_preview": map[string]any{"enabled": true},
	}
	when := time.Date(2026, 6, 20, 10, 30, 0, 0, time.FixedZone("x", 3600)) // 09:30:00Z
	build := "  app-abc123  "
	got := applyLivePreviewPatch(existing, LivePreviewPatch{PushedAt: &when, PushedBuild: &build})

	if got["active"] != true || got["url"] != "https://tank-operator-slot-2.tank.dev.romaine.life" {
		t.Fatalf("unrelated test_state keys not preserved: %+v", got)
	}
	live, ok := got["live_preview"].(map[string]any)
	if !ok {
		t.Fatalf("live_preview missing/wrong type: %+v", got["live_preview"])
	}
	if live["enabled"] != true {
		t.Fatalf("push receipt must not clobber enabled: %+v", live)
	}
	if live["pushed_build"] != "app-abc123" {
		t.Fatalf("pushed_build = %v, want trimmed app-abc123", live["pushed_build"])
	}
	if live["pushed_at"] != "2026-06-20T09:30:00Z" {
		t.Fatalf("pushed_at = %v, want UTC RFC3339", live["pushed_at"])
	}
	if srcLive := existing["live_preview"].(map[string]any); len(srcLive) != 1 {
		t.Fatalf("source live_preview map mutated: %+v", srcLive)
	}
}

func TestApplyLivePreviewPatch_EnableOnEmptyState(t *testing.T) {
	on := true
	got := applyLivePreviewPatch(nil, LivePreviewPatch{Enabled: &on})
	live := got["live_preview"].(map[string]any)
	if live["enabled"] != true {
		t.Fatalf("enabled = %v, want true", live["enabled"])
	}
	if _, ok := live["pushed_at"]; ok {
		t.Fatalf("pushed_at should be absent when not patched: %+v", live)
	}
}

func TestApplyLivePreviewPatch_DisablePreservesReceipt(t *testing.T) {
	off := false
	existing := map[string]any{"live_preview": map[string]any{
		"enabled": true, "pushed_at": "2026-06-20T09:30:00Z", "pushed_build": "app-abc",
	}}
	got := applyLivePreviewPatch(existing, LivePreviewPatch{Enabled: &off})
	live := got["live_preview"].(map[string]any)
	if live["enabled"] != false {
		t.Fatalf("enabled = %v, want false", live["enabled"])
	}
	if live["pushed_build"] != "app-abc" {
		t.Fatalf("disable must preserve last push receipt: %+v", live)
	}
}
