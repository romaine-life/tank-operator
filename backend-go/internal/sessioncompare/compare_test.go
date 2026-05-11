package sessioncompare

import (
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

func TestCompareMatchesEquivalentSessions(t *testing.T) {
	name := "Workbench"
	created := "2026-05-11T00:00:01+00:00"
	session := sessions.Info{
		ID:          "12",
		PodName:     str("session-12"),
		Owner:       "nelson@romaine.life",
		Status:      "Active",
		Mode:        "codex_gui",
		RequestedAt: &created,
		CreatedAt:   &created,
		Name:        &name,
		TestState:   map[string]any{"active": true},
	}

	result := Compare([]sessions.Info{session}, []sessions.Info{session})
	if !result.Match {
		t.Fatalf("match = false, diffs = %#v", result.Diffs)
	}
	if result.PythonCount != 1 || result.GoCount != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", result.PythonCount, result.GoCount)
	}
}

func TestCompareReportsFieldAndMissingSessionDiffs(t *testing.T) {
	python := []sessions.Info{
		{ID: "12", PodName: str("session-12"), Status: "Active"},
		{ID: "13", PodName: str("session-13"), Status: "Failed"},
	}
	goSessions := []sessions.Info{
		{ID: "12", PodName: str("session-12"), Status: "Pending"},
		{ID: "14", PodName: str("session-14"), Status: "Active"},
	}

	result := Compare(python, goSessions)
	if result.Match {
		t.Fatal("match = true, want false")
	}
	if len(result.Diffs) != 3 {
		t.Fatalf("diff count = %d, want 3: %#v", len(result.Diffs), result.Diffs)
	}
	assertDiff(t, result.Diffs[0], "12", "status", "Active", "Pending")
	assertDiff(t, result.Diffs[1], "13", "_session", map[string]any{
		"id":            "13",
		"pod_name":      "session-13",
		"owner":         "",
		"status":        "Failed",
		"mode":          "",
		"requested_at":  nil,
		"created_at":    nil,
		"ready_at":      nil,
		"name":          nil,
		"test_state":    map[string]any(nil),
		"rollout_state": map[string]any(nil),
	}, nil)
	assertDiff(t, result.Diffs[2], "14", "_session", nil, map[string]any{
		"id":            "14",
		"pod_name":      "session-14",
		"owner":         "",
		"status":        "Active",
		"mode":          "",
		"requested_at":  nil,
		"created_at":    nil,
		"ready_at":      nil,
		"name":          nil,
		"test_state":    map[string]any(nil),
		"rollout_state": map[string]any(nil),
	})
}

func assertDiff(t *testing.T, got Diff, sessionID, field string, python, goValue any) {
	t.Helper()
	if got.SessionID != sessionID || got.Field != field {
		t.Fatalf("diff = %#v, want session=%s field=%s", got, sessionID, field)
	}
	if canonical(got.Python) != canonical(python) || canonical(got.Go) != canonical(goValue) {
		t.Fatalf("diff values = %#v, want python=%#v go=%#v", got, python, goValue)
	}
}

func str(value string) *string {
	return &value
}
