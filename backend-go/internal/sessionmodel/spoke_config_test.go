package sessionmodel

import (
	"encoding/json"
	"testing"
)

func TestSpokeConfigRoundTrip(t *testing.T) {
	// Verify that a map[string]any round-trips through json.Marshal/Unmarshal
	// identically to the pattern used for rollout_state and other jsonb columns.
	config := map[string]any{
		"mode":    "claude_gui",
		"count":   float64(3),
		"repos":   []any{"romaine-life/tank-operator"},
		"enabled": true,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mode"] != "claude_gui" {
		t.Errorf("mode = %v, want claude_gui", got["mode"])
	}
	if got["count"] != float64(3) {
		t.Errorf("count = %v, want 3", got["count"])
	}
	if got["enabled"] != true {
		t.Errorf("enabled = %v, want true", got["enabled"])
	}
}

func TestSpokeConfigNilSafe(t *testing.T) {
	// nil spoke_config (NULL DB column) must produce a nil map — same
	// invariant as rollout_state when the column hasn't been set.
	var config map[string]any
	if config != nil {
		t.Fatal("zero-value map[string]any must be nil")
	}
	// Verify that the SessionRecord zero-value has nil SpokeConfig.
	var record SessionRecord
	if record.SpokeConfig != nil {
		t.Fatalf("zero SessionRecord.SpokeConfig = %v, want nil", record.SpokeConfig)
	}
}
