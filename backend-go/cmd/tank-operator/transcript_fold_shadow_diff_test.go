package main

import (
	"strings"
	"testing"
)

// TestTranscriptShadowRowDiffNamesDifferingFields pins the #1130 diagnostic:
// the divergence log must name the differing field paths with both sides'
// values, descending one map level, bounded.
func TestTranscriptShadowRowDiffNamesDifferingFields(t *testing.T) {
	got := map[string]any{
		"id":       "turn-activity-t1",
		"same":     "x",
		"turnNumber": int64(5),
		"activity": map[string]any{"entries": 3, "status": "active"},
	}
	want := map[string]any{
		"id":       "turn-activity-t1",
		"same":     "x",
		"activity": map[string]any{"entries": 4, "status": "active"},
	}
	diff := transcriptShadowRowDiff(got, want)
	if !strings.Contains(diff, "activity.entries fold=3 ref=4") {
		t.Fatalf("nested diff missing: %q", diff)
	}
	if !strings.Contains(diff, "turnNumber fold=5 ref=null") {
		t.Fatalf("missing-key diff missing: %q", diff)
	}
	if strings.Contains(diff, "same") || strings.Contains(diff, "status") {
		t.Fatalf("equal fields must not appear: %q", diff)
	}
}
