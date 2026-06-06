package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// imageOverrideAdapter adapts the pgstore session-image override store to the
// sessions.SessionImageOverrides resolver interface the Manager consumes,
// mapping "no row" to ok=false so the manager falls back to the pinned image.
type imageOverrideAdapter struct {
	store *pgstore.SessionImageOverrideStore
}

func (a imageOverrideAdapter) Get(ctx context.Context, scope string) (claudeImage, codexImage, antigravityImage string, ok bool, err error) {
	ov, getErr := a.store.Get(ctx, scope)
	if getErr != nil {
		if errors.Is(getErr, pgstore.ErrSessionImageOverrideNotFound) {
			return "", "", "", false, nil
		}
		return "", "", "", false, getErr
	}
	return ov.ClaudeImage, ov.CodexImage, ov.AntigravityImage, true, nil
}

// requireServiceOrAdminCaller authenticates an internal call that may be driven
// either by a session pod / MCP service principal (role=service + actor_email)
// or by an admin bot token. Mirrors requireSessionScopeRetireCaller; used by
// the session-image override endpoints below.
func (s *appServer) requireServiceOrAdminCaller(w http.ResponseWriter, r *http.Request) *auth.User {
	if s.verifier == nil {
		writeError(w, http.StatusInternalServerError, "JWT verifier not configured")
		return nil
	}
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return nil
	}
	if user.Role == auth.RoleService {
		if user.ActorEmail == "" {
			writeError(w, http.StatusUnauthorized, "service-role token missing actor_email")
			return nil
		}
		return &user
	}
	if hasAdminPower(user) {
		return &user
	}
	writeError(w, http.StatusForbidden, "route requires role=service or admin")
	return nil
}

// handleInternalGetSessionImageOverride reports the current durable
// session-image override for a scope — the authoritative answer to "what image
// will NEW sessions in this slot boot?". 404 when no override is set.
func (s *appServer) handleInternalGetSessionImageOverride(w http.ResponseWriter, r *http.Request) {
	if s.requireServiceOrAdminCaller(w, r) == nil {
		return
	}
	if s.imageOverrides == nil {
		writeError(w, http.StatusServiceUnavailable, "session image override store not configured")
		return
	}
	scope := normalizeSessionScope(r.PathValue("session_scope"))
	ov, err := s.imageOverrides.Get(r.Context(), scope)
	if err != nil {
		if errors.Is(err, pgstore.ErrSessionImageOverrideNotFound) {
			writeError(w, http.StatusNotFound, "no session image override set for scope")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionImageOverrideResponse(ov))
}

// handleInternalSetSessionImageOverride upserts the durable session-image
// override for a non-production scope. New sessions created in that scope then
// boot the override image (the test-slot repoint flow, docs/testing.md). It
// refuses the production scope and requires the test-env gate, so production
// sessions can never be repointed through this surface.
func (s *appServer) handleInternalSetSessionImageOverride(w http.ResponseWriter, r *http.Request) {
	user := s.requireServiceOrAdminCaller(w, r)
	if user == nil {
		return
	}
	if !s.sessionImageOverridesEnabled {
		writeError(w, http.StatusForbidden, "session image overrides are disabled on this deployment")
		return
	}
	if s.imageOverrides == nil {
		writeError(w, http.StatusServiceUnavailable, "session image override store not configured")
		return
	}
	scope := normalizeSessionScope(r.PathValue("session_scope"))
	if scope == prodSessionScope {
		writeError(w, http.StatusBadRequest, "refusing to override the production session image")
		return
	}
	var body struct {
		ClaudeImage      string `json:"claude_image"`
		CodexImage       string `json:"codex_image"`
		AntigravityImage string `json:"antigravity_image"`
		GitRef           string `json:"git_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	claudeImage := strings.TrimSpace(body.ClaudeImage)
	codexImage := strings.TrimSpace(body.CodexImage)
	antigravityImage := strings.TrimSpace(body.AntigravityImage)
	if claudeImage == "" && codexImage == "" && antigravityImage == "" {
		writeError(w, http.StatusBadRequest, "at least one of claude_image / codex_image / antigravity_image is required")
		return
	}
	setBy := user.ActorEmail
	if setBy == "" {
		setBy = user.Email
	}
	if err := s.imageOverrides.Upsert(r.Context(), pgstore.SessionImageOverride{
		SessionScope:     scope,
		ClaudeImage:      claudeImage,
		CodexImage:       codexImage,
		AntigravityImage: antigravityImage,
		GitRef:           strings.TrimSpace(body.GitRef),
		SetBy:            setBy,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordSessionImageOverrideWrite("set")
	ov, err := s.imageOverrides.Get(r.Context(), scope)
	if err != nil {
		// The write succeeded; a read-back failure shouldn't fail the call.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "session_scope": scope})
		return
	}
	writeJSON(w, http.StatusOK, sessionImageOverrideResponse(ov))
}

// handleInternalDeleteSessionImageOverride clears a scope's override so new
// sessions revert to the chart-pinned image. Called on slot return / teardown.
func (s *appServer) handleInternalDeleteSessionImageOverride(w http.ResponseWriter, r *http.Request) {
	if s.requireServiceOrAdminCaller(w, r) == nil {
		return
	}
	if s.imageOverrides == nil {
		writeError(w, http.StatusServiceUnavailable, "session image override store not configured")
		return
	}
	scope := normalizeSessionScope(r.PathValue("session_scope"))
	if scope == prodSessionScope {
		writeError(w, http.StatusBadRequest, "refusing to mutate the production session image")
		return
	}
	removed, err := s.imageOverrides.Delete(r.Context(), scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordSessionImageOverrideWrite("delete")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "session_scope": scope, "removed": removed})
}

func sessionImageOverrideResponse(ov pgstore.SessionImageOverride) map[string]any {
	return map[string]any{
		"session_scope":     ov.SessionScope,
		"claude_image":      ov.ClaudeImage,
		"codex_image":       ov.CodexImage,
		"antigravity_image": ov.AntigravityImage,
		"git_ref":           ov.GitRef,
		"set_by":            ov.SetBy,
		"set_at":            ov.SetAt,
	}
}
