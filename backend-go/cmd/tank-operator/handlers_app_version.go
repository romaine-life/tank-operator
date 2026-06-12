package main

import (
	"net/http"
	"os"
	"strings"
	"time"
)

type appVersionImage struct {
	Image string `json:"image"`
	Tag   string `json:"tag,omitempty"`
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
	writeJSON(w, http.StatusOK, appVersionBody(time.Now().UTC()))
}

func appVersionBody(now time.Time) appVersionResponse {
	return appVersionResponse{
		AppImage:                imageVersion(os.Getenv("TANK_OPERATOR_IMAGE")),
		SessionImage:            imageVersion(os.Getenv("SESSION_IMAGE")),
		CodexSessionImage:       imageVersion(os.Getenv("CODEX_SESSION_IMAGE")),
		AntigravitySessionImage: imageVersion(os.Getenv("ANTIGRAVITY_SESSION_IMAGE")),
		SessionScope:            normalizeSessionScope(os.Getenv("SESSION_REGISTRY_SCOPE")),
		PodName:                 strings.TrimSpace(os.Getenv("HOSTNAME")),
		FetchedAt:               now.Format(time.RFC3339Nano),
	}
}

func imageVersion(image string) appVersionImage {
	image = strings.TrimSpace(image)
	return appVersionImage{Image: image, Tag: imageTag(image)}
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
