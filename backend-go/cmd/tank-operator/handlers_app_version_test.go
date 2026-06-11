package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminAppVersionRequiresAdmin(t *testing.T) {
	app := &appServer{verifier: authVerifierForTests(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/app-version", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	app.handleAdminAppVersion(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestAdminAppVersionReportsConfiguredImageTags(t *testing.T) {
	t.Setenv("TANK_OPERATOR_IMAGE", "romainecr.azurecr.io/tank-operator:app-abc123")
	t.Setenv("SESSION_IMAGE", "romainecr.azurecr.io/claude-container:claude-def456")
	t.Setenv("CODEX_SESSION_IMAGE", "romainecr.azurecr.io/codex-container:codex-789")
	t.Setenv("ANTIGRAVITY_SESSION_IMAGE", "romainecr.azurecr.io/antigravity-container:antigravity-012")
	t.Setenv("SESSION_REGISTRY_SCOPE", "tank-operator-slot-6")
	t.Setenv("HOSTNAME", "tank-operator-6d7")

	body := appVersionBody(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))

	if body.AppImage.Tag != "app-abc123" || body.SessionImage.Tag != "claude-def456" || body.CodexSessionImage.Tag != "codex-789" {
		t.Fatalf("tags = %#v", body)
	}
	if body.AntigravitySessionImage.Tag != "antigravity-012" {
		t.Fatalf("antigravity tag = %#v", body.AntigravitySessionImage)
	}
	if body.SessionScope != "tank-operator-slot-6" || body.PodName != "tank-operator-6d7" || body.FetchedAt == "" {
		t.Fatalf("metadata = %#v", body)
	}
}

func TestAdminAppVersionHandlerReturnsBody(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", "admin@example.com")
	t.Setenv("TANK_OPERATOR_IMAGE", "romainecr.azurecr.io/tank-operator:app-abc123")

	app := &appServer{verifier: authVerifierForTests(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/app-version", nil)
	req.Header.Set("Authorization", "Bearer "+signedAdminToken(t, "admin@example.com"))
	rec := httptest.NewRecorder()

	app.handleAdminAppVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	var body appVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AppImage.Image != "romainecr.azurecr.io/tank-operator:app-abc123" || body.AppImage.Tag != "app-abc123" {
		t.Fatalf("app image = %#v", body.AppImage)
	}
}
