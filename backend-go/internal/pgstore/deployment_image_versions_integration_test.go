package pgstore

import (
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestDeploymentImageVersionStoreLatestByScope(t *testing.T) {
	ctx, pool := newTurnNumberTestPool(t)
	store := NewDeploymentImageVersionStore(pool)
	older := time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	if err := store.UpsertMany(ctx, []DeploymentImageVersion{
		{
			SessionScope: "default",
			PodName:      "tank-operator-old",
			ImageKind:    DeploymentImageKindApp,
			ImageRef:     "romainecr.azurecr.io/tank-operator:app-old",
			Metadata:     sessionmodel.ImageVersionMetadata{"git_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			ObservedAt:   older,
		},
		{
			SessionScope: "default",
			PodName:      "tank-operator-new",
			ImageKind:    DeploymentImageKindApp,
			ImageRef:     "romainecr.azurecr.io/tank-operator:app-new",
			Metadata: sessionmodel.ImageVersionMetadata{
				"git_sha":   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"pr_number": "1050",
				"unknown":   "dropped",
			},
			ObservedAt: newer,
		},
		{
			SessionScope: "default",
			PodName:      "tank-operator-new",
			ImageKind:    DeploymentImageKindSessionCodex,
			ImageRef:     "romainecr.azurecr.io/codex-container:codex-new",
			ObservedAt:   newer,
		},
	}); err != nil {
		t.Fatalf("upsert versions: %v", err)
	}

	rows, err := store.LatestByScope(ctx, "default")
	if err != nil {
		t.Fatalf("latest by scope: %v", err)
	}
	app := rows[DeploymentImageKindApp]
	if got, want := app.ImageRef, "romainecr.azurecr.io/tank-operator:app-new"; got != want {
		t.Fatalf("app image = %q, want %q", got, want)
	}
	if got, want := app.Metadata["pr_number"], "1050"; got != want {
		t.Fatalf("app pr_number = %q, want %q", got, want)
	}
	if got := app.Metadata["unknown"]; got != "" {
		t.Fatalf("unknown metadata key survived normalization: %q", got)
	}
	if got, want := rows[DeploymentImageKindSessionCodex].ImageRef, "romainecr.azurecr.io/codex-container:codex-new"; got != want {
		t.Fatalf("codex image = %q, want %q", got, want)
	}
}
