package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
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
