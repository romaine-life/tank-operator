package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type fakeDeploymentImageVersionStore struct {
	rows map[string]pgstore.DeploymentImageVersion
	err  error
}

func (f fakeDeploymentImageVersionStore) UpsertMany(context.Context, []pgstore.DeploymentImageVersion) error {
	return nil
}

func (f fakeDeploymentImageVersionStore) LatestByScope(context.Context, string) (map[string]pgstore.DeploymentImageVersion, error) {
	return f.rows, f.err
}

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

func TestAdminAppVersionPrefersDurableDeploymentLedger(t *testing.T) {
	t.Setenv("TANK_OPERATOR_IMAGE", "romainecr.azurecr.io/tank-operator:app-env")
	t.Setenv("SESSION_IMAGE", "romainecr.azurecr.io/claude-container:claude-env")
	t.Setenv("CODEX_SESSION_IMAGE", "romainecr.azurecr.io/codex-container:codex-env")
	t.Setenv("ANTIGRAVITY_SESSION_IMAGE", "romainecr.azurecr.io/antigravity-container:antigravity-env")
	t.Setenv("SESSION_REGISTRY_SCOPE", "default")

	observedAt := time.Date(2026, 6, 12, 1, 44, 6, 0, time.UTC)
	app := &appServer{
		deploymentVersions: fakeDeploymentImageVersionStore{
			rows: map[string]pgstore.DeploymentImageVersion{
				pgstore.DeploymentImageKindApp: {
					SessionScope: "default",
					PodName:      "tank-operator-new",
					ImageKind:    pgstore.DeploymentImageKindApp,
					ImageRef:     "romainecr.azurecr.io/tank-operator:app-durable",
					Metadata: sessionmodel.ImageVersionMetadata{
						"built_at":  "2026-06-12T01:44:06Z",
						"git_sha":   "1f0c7e0b29f31b706f0f100cba38f457c7907847",
						"pr_number": "1050",
						"pr_url":    "https://github.com/romaine-life/tank-operator/pull/1050",
					},
					ObservedAt: observedAt,
				},
			},
		},
	}

	body := app.appVersionBody(context.Background(), time.Date(2026, 6, 12, 2, 0, 0, 0, time.UTC))

	if got, want := body.AppImage.Tag, "app-durable"; got != want {
		t.Fatalf("app tag = %q, want durable %q", got, want)
	}
	if got, want := body.AppImage.Display, "2026-06-12 01:44 UTC / 1f0c7e0 / PR #1050"; got != want {
		t.Fatalf("app display = %q, want %q", got, want)
	}
	if got := body.AppImage.ObservedAt; got == "" {
		t.Fatalf("durable observed_at missing: %#v", body.AppImage)
	}
	if got := body.SessionImage.Tag; got != "" {
		t.Fatalf("missing durable rows should stay visibly missing once ledger is available: session tag = %q", got)
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

func TestObservedDeploymentImageVersionsIncludesAllImageKinds(t *testing.T) {
	t.Setenv("TANK_OPERATOR_IMAGE", "romainecr.azurecr.io/tank-operator:app-abc")
	t.Setenv("TANK_OPERATOR_IMAGE_METADATA", `{"git_sha":"abcdef0123456789","unknown":"dropped"}`)
	t.Setenv("SESSION_IMAGE", "romainecr.azurecr.io/claude-container:claude-abc")
	t.Setenv("CODEX_SESSION_IMAGE", "romainecr.azurecr.io/codex-container:codex-abc")
	t.Setenv("ANTIGRAVITY_SESSION_IMAGE", "romainecr.azurecr.io/antigravity-container:antigravity-abc")

	records := observedDeploymentImageVersions("", "tank-operator-abc", time.Date(2026, 6, 12, 1, 2, 3, 0, time.UTC))
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4", len(records))
	}
	byKind := map[string]pgstore.DeploymentImageVersion{}
	for _, record := range records {
		byKind[record.ImageKind] = record
	}
	if byKind[pgstore.DeploymentImageKindApp].SessionScope != "default" {
		t.Fatalf("default scope not normalized: %#v", byKind[pgstore.DeploymentImageKindApp])
	}
	if got := byKind[pgstore.DeploymentImageKindApp].Metadata["unknown"]; got != "" {
		t.Fatalf("unknown metadata key survived normalization: %q", got)
	}
	if byKind[pgstore.DeploymentImageKindSessionAntigravity].ImageRef == "" {
		t.Fatalf("antigravity record missing: %#v", byKind)
	}
}
