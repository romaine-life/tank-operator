package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

func TestAdminHiddenSessionsNonAdmin403(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/hidden-sessions", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	resp := httptest.NewRecorder()

	app.handleAdminHiddenSessions(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
	}
}

func TestAdminHiddenSessionsRequiresPostgres(t *testing.T) {
	app := adminTestServer(t)
	app.pgPool = nil
	req := httptest.NewRequest(http.MethodGet, "/api/admin/hidden-sessions", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleAdminHiddenSessions(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", resp.Code, resp.Body.String())
	}
}

func TestAdminHiddenSessionTimelineNonAdmin403(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/hidden-sessions/63/timeline", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	resp := httptest.NewRecorder()

	app.handleAdminHiddenSessionTimeline(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
	}
}

func TestAdminHiddenSessionTimelineRequiresSessionID(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/hidden-sessions//timeline", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleAdminHiddenSessionTimeline(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
}
