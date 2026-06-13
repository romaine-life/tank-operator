package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

func TestDebugStuckTurnsNonAdmin403(t *testing.T) {
	app := adminTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/debug/stuck-turns", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	resp := httptest.NewRecorder()

	app.handleDebugStuckTurns(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugStuckTurnsPgNotConfigured503(t *testing.T) {
	app := adminTestServer(t)
	app.pgPool = nil

	req := httptest.NewRequest(http.MethodGet, "/api/debug/stuck-turns", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugStuckTurns(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", resp.Code, resp.Body.String())
	}
}

func TestClampedQueryInt(t *testing.T) {
	cases := []struct {
		raw           string
		def, min, max int
		want          int
	}{
		{"", 600, 60, 86400, 600},         // empty → default
		{"garbage", 600, 60, 86400, 600},  // garbage → default
		{"300", 600, 60, 86400, 300},      // in-range honored
		{"10", 600, 60, 86400, 60},        // below min clamps up
		{"999999", 600, 60, 86400, 86400}, // above max clamps down
		{"100", 100, 1, 500, 100},         // limit default range
		{"0", 100, 1, 500, 1},             // limit below min clamps to 1
		{"5000", 100, 1, 500, 500},        // limit above max clamps to 500
	}
	for _, c := range cases {
		if got := clampedQueryInt(c.raw, c.def, c.min, c.max); got != c.want {
			t.Errorf("clampedQueryInt(%q, %d, %d, %d) = %d, want %d",
				c.raw, c.def, c.min, c.max, got, c.want)
		}
	}
}

// TestDebugStuckTurnsJSONShape pins the wire-shape contract so an
// operator's runbook (and the TankSessionStuckInProgress alert
// annotation) can name stable field paths — both stall classes. The
// handler integration with a real pgxPool is covered by the
// Postgres-backed suite.
func TestDebugStuckTurnsJSONShape(t *testing.T) {
	payload := map[string]any{
		"description":                 debugStuckTurnsDescription,
		"scope":                       "default",
		"threshold_seconds":           600,
		"streaming_threshold_seconds": 1200,
		"count":                       2,
		"stuck_turns": []map[string]any{
			{
				"session_id":                      "812",
				"mode":                            "claude_gui",
				"phase":                           "accepted",
				"activity_status":                 "claimed",
				"active_turn_id":                  "turn_abc",
				"stuck_seconds":                   720,
				"last_event_at":                   "",
				"provider_rate_limit_status":      "throttled",
				"provider_rate_limit_observed_at": "2026-06-06T19:01:41Z",
			},
			{
				"session_id":                      "828",
				"mode":                            "codex_gui",
				"phase":                           "streaming",
				"activity_status":                 "streaming",
				"active_turn_id":                  "turn_7fcfb58b",
				"stuck_seconds":                   1995,
				"last_event_at":                   "2026-06-12T03:05:59Z",
				"provider_rate_limit_status":      "",
				"provider_rate_limit_observed_at": "",
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{
		`"scope":"default"`,
		`"threshold_seconds":600`,
		`"streaming_threshold_seconds":1200`,
		`"count":2`,
		`"session_id":"812"`,
		`"phase":"accepted"`,
		`"phase":"streaming"`,
		`"stuck_seconds":720`,
		`"last_event_at":"2026-06-12T03:05:59Z"`,
		`"provider_rate_limit_status":"throttled"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("encoded payload missing %s: %s", want, encoded)
		}
	}
}
