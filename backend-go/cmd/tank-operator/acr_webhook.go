package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const maxACRWebhookBytes = 1 << 20 // 1 MiB — ACR push payloads are small

// defaultACRRegistry is the fallback registry host stamped onto the durable
// record when the delivery omits request.host. The repo only publishes to one
// registry, so an empty host is a malformed-but-recoverable delivery rather than
// a reason to drop the image-readiness signal.
const defaultACRRegistry = "romainecr.azurecr.io"

// shaImageTagPrefix is the per-commit image tag prefix the build pipeline stamps
// (`sha-<commit>`). The receiver only records these — `app-`/`claude-`/
// `api-proxy-`/etc. tags are not commit-addressable deploy signals.
const shaImageTagPrefix = "sha-"

// acrWebhookPayload captures only the fields the image-readiness receiver needs
// from an Azure ACR `push` webhook delivery. See
// https://learn.microsoft.com/azure/container-registry/container-registry-webhook-reference.
type acrWebhookPayload struct {
	Action string `json:"action"`
	Target struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Digest     string `json:"digest"`
	} `json:"target"`
	Request struct {
		Host string `json:"host"`
	} `json:"request"`
}

// verifyACRWebhookSecret compares the presented `Authorization: Bearer <secret>`
// header against the configured secret with constant-time equality. Fails closed
// when the configured secret is empty (an unconfigured receiver rejects every
// delivery), the same posture as the GitHub HMAC receiver.
func (s *appServer) verifyACRWebhookSecret(authHeader string) bool {
	secret := strings.TrimSpace(s.acrWebhookSecret)
	if secret == "" {
		return false // fail closed: an unconfigured secret rejects all deliveries
	}
	const prefix = "Bearer "
	authHeader = strings.TrimSpace(authHeader)
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	presented := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	return subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) == 1
}

// handleACRWebhook is the public inbound Azure ACR webhook for the
// image-readiness receiver. Azure ACR fires a `push` delivery the instant a
// `sha-<commit>` image tag lands in the registry; this records that as the
// durable "the deployable image for this commit now exists" signal, replacing
// the test-slot provisioning gate's image-build polling wait. Stage 1 only
// records the signal — nothing consumes ci_image_available yet. It authenticates
// by a static bearer secret (no HMAC; ACR sends a fixed custom header), is
// idempotent, and is safe to re-deliver. See docs/event-driven-rollout.md.
func (s *appServer) handleACRWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxACRWebhookBytes))
	if err != nil {
		recordACRWebhook("parse_error")
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	recordACRWebhook("received")
	if !s.verifyACRWebhookSecret(r.Header.Get("Authorization")) {
		recordACRWebhook("rejected_auth")
		writeError(w, http.StatusUnauthorized, "invalid webhook secret")
		return
	}
	var p acrWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		recordACRWebhook("parse_error")
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if strings.TrimSpace(p.Action) != "push" {
		recordACRWebhook("ignored_action")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	tag := strings.TrimSpace(p.Target.Tag)
	if !strings.HasPrefix(tag, shaImageTagPrefix) {
		recordACRWebhook("ignored_tag")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	commit := strings.TrimPrefix(tag, shaImageTagPrefix)
	registry := strings.TrimSpace(p.Request.Host)
	if registry == "" {
		registry = defaultACRRegistry
	}
	repoName := strings.TrimSpace(p.Target.Repository)
	// ack + no-op when the store is unconfigured (stub mode) so ACR stops
	// retrying, or when the delivery is missing the repository it would key on.
	if s.ciImageAvailable == nil || repoName == "" || commit == "" {
		recordACRWebhook("ignored_tag")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := s.ciImageAvailable.UpsertCIImageAvailable(r.Context(), pgstore.CIImageAvailable{
		Registry:    registry,
		RepoName:    repoName,
		CommitSHA:   commit,
		ImageTag:    tag,
		ImageDigest: strings.TrimSpace(p.Target.Digest),
	}); err != nil {
		recordACRWebhook("error")
		slog.Warn("acr webhook upsert failed", "registry", registry, "repo", repoName, "commit", commit, "error", err)
		writeError(w, http.StatusInternalServerError, "could not record image availability")
		return
	}
	recordACRWebhook("recorded")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
