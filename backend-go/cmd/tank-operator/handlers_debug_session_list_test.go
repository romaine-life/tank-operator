package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestDebugSessionListStateAdminGate(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	t.Run("non-admin role 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-state", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListState(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("admin gets stub response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-state?owner=target@example.com", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListState(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("service admin actor gets stub response", func(t *testing.T) {
		t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-state?owner=target@example.com", nil)
		req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", adminEmail))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListState(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
		}
	})
}

func TestDebugRowJSONCarriesRecoveryRunConfig(t *testing.T) {
	row := debugRowJSON(sessionmodel.SessionRecord{
		ID:                  "851",
		Mode:                sessionmodel.CodexGUIMode,
		PodName:             "session-851",
		Name:                "Investigate NATS session freeze",
		Visible:             false,
		Status:              "Terminated",
		Repos:               []string{"romaine-life/tank-operator"},
		Capabilities:       []string{sessionmodel.SessionCapabilitySpireLensMCP},
		Model:               "gpt-5.5",
		Effort:              "xhigh",
		RuntimeModel:        "gpt-5.5",
		RuntimeEffort:       "xhigh",
		RuntimeConfiguredAt: "2026-06-13T01:02:03Z",
		CloneState:          map[string]any{"romaine-life/tank-operator": "ok"},
	})

	assertDebugRowField(t, row, "visible", false)
	assertDebugRowField(t, row, "mode", sessionmodel.CodexGUIMode)
	assertDebugRowField(t, row, "name", "Investigate NATS session freeze")
	assertDebugRowField(t, row, "model", "gpt-5.5")
	assertDebugRowField(t, row, "effort", "xhigh")
	assertDebugRowField(t, row, "runtime_model", "gpt-5.5")
	assertDebugRowField(t, row, "runtime_effort", "xhigh")
	assertDebugRowField(t, row, "runtime_configured_at", "2026-06-13T01:02:03Z")
	assertDebugRowField(t, row, "has_clone_state", true)

	repos, ok := row["repos"].([]string)
	if !ok || len(repos) != 1 || repos[0] != "romaine-life/tank-operator" {
		t.Fatalf("repos = %#v, want selected repo slug", row["repos"])
	}
	capabilities, ok := row["capabilities"].([]string)
	if !ok || len(capabilities) != 1 || capabilities[0] != sessionmodel.SessionCapabilitySpireLensMCP {
		t.Fatalf("capabilities = %#v, want selected capability", row["capabilities"])
	}
}

func assertDebugRowField(t *testing.T, row map[string]any, key string, want any) {
	t.Helper()
	if got := row[key]; got != want {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
