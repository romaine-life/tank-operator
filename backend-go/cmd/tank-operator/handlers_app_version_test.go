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
	t.Setenv("TANK_OPERATOR_IMAGE_METADATA", `{"built_at":"2026-06-11T08:06:08Z","git_sha":"532dd02176ac6d0013478aaf63ee419a3eb17d24","git_ref":"main","commit_url":"https://github.com/romaine-life/tank-operator/commit/532dd02176ac6d0013478aaf63ee419a3eb17d24","pr_number":"1049","pr_url":"https://github.com/romaine-life/tank-operator/pull/1049","workflow_run_url":"https://github.com/romaine-life/tank-operator/actions/runs/27332914448","repository":"romaine-life/tank-operator","actor":"github-actions[bot]"}`)
	t.Setenv("SESSION_IMAGE", "romainecr.azurecr.io/claude-container:claude-def456")
	t.Setenv("SESSION_IMAGE_METADATA", `{"built_at":"2026-06-10T18:00:00Z","git_sha":"abcdef0123456789","pr_number":"1001"}`)
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
	if body.AppImage.Display != "2026-06-11 08:06 UTC / 532dd02 / PR #1049" {
		t.Fatalf("app display = %q", body.AppImage.Display)
	}
	if body.AppImage.CommitURL == "" || body.AppImage.PRURL == "" || body.AppImage.WorkflowRunURL == "" {
		t.Fatalf("app metadata links missing: %#v", body.AppImage)
	}
	if body.SessionImage.Display != "2026-06-10 18:00 UTC / abcdef0 / PR #1001" {
		t.Fatalf("session display = %q", body.SessionImage.Display)
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
