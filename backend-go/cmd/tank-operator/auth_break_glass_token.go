package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// handleAdminMintSessionAuthToken mints a fresh auth.romaine.life role=service
// JWT — the credential the system expects a session's agent/service principal
// to present — for the target session's owner, and returns it so an admin can
// copy it and paste it into a stuck agent's chat.
//
// This is the break-glass path for when the normal auth.romaine
// request/approve/activate flow is unavailable (e.g. hot-swap down): instead of
// recording a grant and asking the agent to self-activate, the admin obtains
// the same token a session pod's mcp-auth-proxy would get on the on-behalf-of
// exchange (role=service, actor_email = the session owner, ~15-min TTL fixed by
// auth.romaine).
//
// The minted token is NEVER persisted. Only an audit row
// (auth.break_glass.token) recording who minted it, for which session/owner,
// and the expiry is written.
func (s *appServer) handleAdminMintSessionAuthToken(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.mcpGitHub == nil {
		recordControlActionEvent("tank-operator", "auth_break_glass_token", "auth.break_glass.token", "failed", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "auth token exchange unavailable")
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	info, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	owner := strings.TrimSpace(info.Owner)
	if owner == "" {
		writeError(w, http.StatusConflict, "session has no resolved owner email")
		return
	}

	token, expiresAt, err := s.mcpGitHub.MintActorToken(r.Context(), owner)
	if err != nil {
		recordControlActionEvent("tank-operator", "auth_break_glass_token", "auth.break_glass.token", "failed", "store_error")
		slog.Warn("admin auth break-glass token mint failed",
			"session_id", sessionID, "owner", owner, "error", err.Error())
		writeError(w, http.StatusBadGateway, "auth token exchange failed: "+err.Error())
		return
	}

	expiry := expiresAt.UTC().Format(time.RFC3339)

	// Audit only — the token itself is never stored.
	auditPayload, _ := json.Marshal(map[string]any{
		"minted_by":     user.Email,
		"actor_email":   owner,
		"role":          "service",
		"expires_at":    expiry,
		"session_scope": sessionScope,
	})
	auditEvent := pgstore.ControlActionEvent{
		EventID:       "tank-auth-break-glass-token-" + sessionID + "-" + randomHex(12),
		InvocationID:  "tank-auth-break-glass-token-" + randomHex(12),
		OwnerEmail:    owner,
		SessionScope:  sessionScope,
		SessionID:     sessionID,
		SourceService: "tank-operator",
		SourceTool:    "auth_break_glass_token",
		Action:        "auth.break_glass.token",
		Status:        "succeeded",
		TargetKind:    "service_jwt",
		TargetRef:     owner,
		Payload:       auditPayload,
	}
	if s.controlActions != nil {
		if _, appendErr := s.controlActions.Append(r.Context(), auditEvent); appendErr != nil {
			// The token minted fine; a failed audit write must not strand the
			// admin. Record the metric + log, but still return the token.
			recordControlActionEvent("tank-operator", "auth_break_glass_token", "auth.break_glass.token", "succeeded", "store_error")
			slog.Warn("auth break-glass token minted but audit append failed",
				"session_id", sessionID, "owner", owner, "error", appendErr.Error())
		} else {
			recordControlActionEvent("tank-operator", "auth_break_glass_token", "auth.break_glass.token", "succeeded", "ok")
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"active":        true,
		"token":         token,
		"expires_at":    expiry,
		"actor_email":   owner,
		"role":          "service",
		"session_id":    sessionID,
		"session_scope": sessionScope,
	})
}
