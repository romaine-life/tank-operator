package sessionmodel

import (
	"encoding/json"
	"testing"
)

func TestDecodeSpawnedSessionsRoundTrip(t *testing.T) {
	refs := []SpawnedSessionRef{
		{
			ID:        "101",
			Name:      "Child Work",
			Mode:      "claude_gui",
			Model:     "opus",
			Repos:     []string{"romaine-life/tank-operator"},
			URL:       "https://tank.romaine.life/?session=101",
			CreatedAt: "2026-06-19T00:00:00Z",
		},
		{ID: "102", Name: "Sibling", URL: "https://tank.romaine.life/?session=102"},
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	got := DecodeSpawnedSessions(raw)
	if len(got) != 2 {
		t.Fatalf("decoded %d refs, want 2", len(got))
	}
	if got[0].ID != "101" || got[0].Name != "Child Work" || got[0].Mode != "claude_gui" ||
		got[0].Model != "opus" || got[0].URL != "https://tank.romaine.life/?session=101" ||
		len(got[0].Repos) != 1 || got[0].Repos[0] != "romaine-life/tank-operator" {
		t.Fatalf("ref[0] = %#v", got[0])
	}
}

func TestDecodeSpawnedSessionsEmptyAndMalformed(t *testing.T) {
	cases := map[string][]byte{
		"nil":            nil,
		"empty":          {},
		"json null":      []byte("null"),
		"not an array":   []byte(`{"id":"1"}`),
		"malformed json": []byte(`[{"id":`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// Display-only projection: a bad column must decode to nil, never
			// panic or error, so the session list stays renderable.
			if got := DecodeSpawnedSessions(raw); got != nil {
				t.Fatalf("DecodeSpawnedSessions(%q) = %#v, want nil", name, got)
			}
		})
	}
}
