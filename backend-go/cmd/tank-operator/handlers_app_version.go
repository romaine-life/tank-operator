package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type appVersionImage struct {
	Image          string `json:"image"`
	Tag            string `json:"tag,omitempty"`
	Display        string `json:"display,omitempty"`
	ObservedAt     string `json:"observed_at,omitempty"`
	BuiltAt        string `json:"built_at,omitempty"`
	GitSHA         string `json:"git_sha,omitempty"`
	ShortSHA       string `json:"short_sha,omitempty"`
	GitRef         string `json:"git_ref,omitempty"`
	CommitURL      string `json:"commit_url,omitempty"`
	PRNumber       string `json:"pr_number,omitempty"`
	PRURL          string `json:"pr_url,omitempty"`
	WorkflowRunURL string `json:"workflow_run_url,omitempty"`
	Repository     string `json:"repository,omitempty"`
	Actor          string `json:"actor,omitempty"`
	Source         string `json:"source,omitempty"`
}

type appVersionResponse struct {
	AppImage                appVersionImage `json:"app_image"`
	SessionImage            appVersionImage `json:"session_image"`
	CodexSessionImage       appVersionImage `json:"codex_session_image"`
	AntigravitySessionImage appVersionImage `json:"antigravity_session_image"`
	SessionScope            string          `json:"session_scope"`
	PodName                 string          `json:"pod_name,omitempty"`
	FetchedAt               string          `json:"fetched_at"`
}

func (s *appServer) handleAdminAppVersion(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	writeJSON(w, http.StatusOK, s.appVersionBody(r.Context(), time.Now().UTC()))
}

func appVersionBody(now time.Time) appVersionResponse {
	return appVersionBodyFromRows(now, nil)
}

func (s *appServer) appVersionBody(ctx context.Context, now time.Time) appVersionResponse {
	if s == nil || s.deploymentVersions == nil {
		return appVersionBody(now)
	}
	scope := normalizeSessionScope(os.Getenv("SESSION_REGISTRY_SCOPE"))
	rows, err := s.deploymentVersions.LatestByScope(ctx, scope)
	if err != nil {
		if !errors.Is(err, pgstore.ErrDeploymentImageVersionsUnavailable) {
			slog.Warn("deployment image versions read failed; falling back to process env",
				"scope", scope, "error", err)
		}
		return appVersionBody(now)
	}
	return appVersionBodyFromRows(now, rows)
}

func appVersionBodyFromRows(now time.Time, rows map[string]pgstore.DeploymentImageVersion) appVersionResponse {
	return appVersionResponse{
		AppImage:                imageVersionFromRow(rows, pgstore.DeploymentImageKindApp, os.Getenv("TANK_OPERATOR_IMAGE"), os.Getenv("TANK_OPERATOR_IMAGE_METADATA")),
		SessionImage:            imageVersionFromRow(rows, pgstore.DeploymentImageKindSessionClaude, os.Getenv("SESSION_IMAGE"), os.Getenv("SESSION_IMAGE_METADATA")),
		CodexSessionImage:       imageVersionFromRow(rows, pgstore.DeploymentImageKindSessionCodex, os.Getenv("CODEX_SESSION_IMAGE"), os.Getenv("CODEX_SESSION_IMAGE_METADATA")),
		AntigravitySessionImage: imageVersionFromRow(rows, pgstore.DeploymentImageKindSessionAntigravity, os.Getenv("ANTIGRAVITY_SESSION_IMAGE"), os.Getenv("ANTIGRAVITY_SESSION_IMAGE_METADATA")),
		SessionScope:            normalizeSessionScope(os.Getenv("SESSION_REGISTRY_SCOPE")),
		PodName:                 strings.TrimSpace(os.Getenv("HOSTNAME")),
		FetchedAt:               now.Format(time.RFC3339Nano),
	}
}

func imageVersionFromRow(rows map[string]pgstore.DeploymentImageVersion, kind, fallbackImage, fallbackMetadata string) appVersionImage {
	if rows != nil {
		if row, ok := rows[kind]; ok {
			info := imageVersion(row.ImageRef, "")
			info.applyMetadata(row.Metadata)
			if !row.ObservedAt.IsZero() {
				info.ObservedAt = row.ObservedAt.UTC().Format(time.RFC3339Nano)
			}
			info.Display = imageVersionDisplay(info)
			return info
		}
		return imageVersion("", "")
	}
	return imageVersion(fallbackImage, fallbackMetadata)
}

func imageVersion(image, metadataJSON string) appVersionImage {
	image = strings.TrimSpace(image)
	metadata := sessionmodel.ParseImageVersionMetadata(metadataJSON)
	info := appVersionImage{
		Image: image,
		Tag:   imageTag(image),
	}
	info.applyMetadata(metadata)
	info.Display = imageVersionDisplay(info)
	return info
}

func (v *appVersionImage) applyMetadata(metadata sessionmodel.ImageVersionMetadata) {
	if len(metadata) == 0 {
		return
	}
	v.BuiltAt = metadata["built_at"]
	v.GitSHA = metadata["git_sha"]
	v.ShortSHA = shortSHA(v.GitSHA)
	v.GitRef = metadata["git_ref"]
	v.CommitURL = metadata["commit_url"]
	v.PRNumber = metadata["pr_number"]
	v.PRURL = metadata["pr_url"]
	v.WorkflowRunURL = metadata["workflow_run_url"]
	v.Repository = metadata["repository"]
	v.Actor = metadata["actor"]
	v.Source = metadata["source"]
}

func imageVersionDisplay(info appVersionImage) string {
	parts := []string{}
	if builtAt := displayTimestamp(info.BuiltAt); builtAt != "" {
		parts = append(parts, builtAt)
	}
	if info.ShortSHA != "" {
		parts = append(parts, info.ShortSHA)
	}
	if info.PRNumber != "" {
		parts = append(parts, "PR #"+info.PRNumber)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " / ")
	}
	if info.Tag != "" {
		return info.Tag
	}
	return strings.TrimSpace(info.Image)
}

func imageTag(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon <= lastSlash {
		return ""
	}
	return image[lastColon+1:]
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

func displayTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func observedDeploymentImageVersions(scope, podName string, observedAt time.Time) []pgstore.DeploymentImageVersion {
	scope = normalizeSessionScope(scope)
	podName = strings.TrimSpace(podName)
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	observedAt = observedAt.UTC()
	return []pgstore.DeploymentImageVersion{
		{
			SessionScope: scope,
			PodName:      podName,
			ImageKind:    pgstore.DeploymentImageKindApp,
			ImageRef:     os.Getenv("TANK_OPERATOR_IMAGE"),
			Metadata:     sessionmodel.ParseImageVersionMetadata(os.Getenv("TANK_OPERATOR_IMAGE_METADATA")),
			ObservedAt:   observedAt,
		},
		{
			SessionScope: scope,
			PodName:      podName,
			ImageKind:    pgstore.DeploymentImageKindSessionClaude,
			ImageRef:     os.Getenv("SESSION_IMAGE"),
			Metadata:     sessionmodel.ParseImageVersionMetadata(os.Getenv("SESSION_IMAGE_METADATA")),
			ObservedAt:   observedAt,
		},
		{
			SessionScope: scope,
			PodName:      podName,
			ImageKind:    pgstore.DeploymentImageKindSessionCodex,
			ImageRef:     os.Getenv("CODEX_SESSION_IMAGE"),
			Metadata:     sessionmodel.ParseImageVersionMetadata(os.Getenv("CODEX_SESSION_IMAGE_METADATA")),
			ObservedAt:   observedAt,
		},
		{
			SessionScope: scope,
			PodName:      podName,
			ImageKind:    pgstore.DeploymentImageKindSessionAntigravity,
			ImageRef:     os.Getenv("ANTIGRAVITY_SESSION_IMAGE"),
			Metadata:     sessionmodel.ParseImageVersionMetadata(os.Getenv("ANTIGRAVITY_SESSION_IMAGE_METADATA")),
			ObservedAt:   observedAt,
		},
	}
}
