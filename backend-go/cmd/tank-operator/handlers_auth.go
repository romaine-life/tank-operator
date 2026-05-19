package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

// userResponseBody is the canonical JSON shape returned for the SPA's
// `user` object — used by /api/auth/exchange (fresh sign-in) and
// /api/auth/me (existing-JWT bootstrap). Both paths must agree: the SPA
// reads whichever returns from the call it makes (exchange on fresh, me
// on reload), and a missing field (e.g. installation_id) reads as
// undefined → undefined == null in JS → the OnboardingWall renders even
// for users with the GitHub App installed.
//
// `role` rides along so the SPA's OnboardingWall can skip itself for callers
// that do not need a user-facing GitHub installation: admins (covered by the
// host installation) and service principals (platform-internal test/session
// automation).
//
// Keep this the single source of truth so the shape can't drift
// between the two paths again.
func userResponseBody(sub, email, name, role string, profile profiles.Profile) map[string]any {
	return map[string]any{
		"sub":             sub,
		"email":           email,
		"name":            name,
		"role":            role,
		"avatar_url":      auth.GravatarURL(email, 64),
		"github_login":    profile.GitHubLogin,
		"installation_id": profile.InstallationID,
		"run_prefs":       profile.RunPrefs,
	}
}

// handleAuthExchange exchanges an auth.romaine.life JWT for a
// tank-operator session JWT. Microsoft sign-in happens upstream at
// auth.romaine.life (Better Auth + Microsoft social provider); that
// service issues an RS256 JWT, the SPA fetches it, then posts it here.
// We verify the upstream signature against auth.romaine.life/api/auth/jwks
// and mint our own tank-operator-signed session JWT so all downstream
// verifier / minter / MCP-attestation plumbing stays on our KV signer.
//
// The access gate is the upstream role claim: ExchangeRomaineLifeToken
// rejects anything other than `admin`/`user` (in particular, the default
// `pending` for fresh Microsoft sign-ups). Tank-operator no longer
// maintains its own email allowlist — admin promotion via the
// auth.romaine.life /admin console is the single source of truth.
func (s *appServer) handleAuthExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthJWT string `json:"auth_jwt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AuthJWT == "" {
		writeError(w, http.StatusBadRequest, "missing auth_jwt")
		return
	}

	email, name, sub, role, err := auth.ExchangeRomaineLifeToken(r.Context(), body.AuthJWT)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return
	}

	token, err := s.minter.MintSession(sub, email, name, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint token: "+err.Error())
		return
	}

	// Read the profile so the response carries installation_id /
	// github_login / run_prefs alongside the JWT. The SPA uses this
	// `user` object directly on the fresh-login path (it does NOT then
	// call /api/auth/me), so omitting these fields here makes the SPA
	// believe installation_id is null even when the profiles row has it
	// — surfacing as a spurious "Connect GitHub" wall after any flow
	// that forces a re-login (cookie expiry, localStorage reap, manual
	// logout). Read-only here; don't write — there's nothing new to
	// merge in. A bad profile read shouldn't block sign-in, so we log
	// and continue with a zero profile rather than 500.
	profile, err := s.profiles.GetOrCreate(r.Context(), email)
	if err != nil {
		slog.Warn("profile read failed during login", "email", email, "error", err)
	}

	// Set httpOnly cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL.Seconds()),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  userResponseBody(sub, email, name, role, profile),
	})
}

// handleLogout clears the session cookie.
func (s *appServer) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe returns user info + profile.
func (s *appServer) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	profile, err := s.profiles.GetOrCreate(r.Context(), user.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, userResponseBody(user.Sub, user.Email, user.Name, user.Role, profile))
}

// handleUpdatePrefs persists the SPA's run-pane preferences (chat font
// scale, sound volume, etc.) on the caller's Postgres profiles row. The
// body shape is opaque to the orchestrator — the SPA owns the schema
// (frontend/src/App.tsx → RunPrefs). The store does a merge-safe
// upsert so unrelated profile fields (installation_id, github_login)
// survive.
func (s *appServer) handleUpdatePrefs(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	prefsStore, ok := s.profiles.(profilesPrefsStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "profile store does not support prefs writes")
		return
	}
	var body struct {
		RunPrefs map[string]any `json:"run_prefs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	profile, err := prefsStore.UpdatePrefs(r.Context(), user.Email, body.RunPrefs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_prefs": profile.RunPrefs,
	})
}

// handleK8sAuth exchanges a ServiceAccount token for a session JWT.
func (s *appServer) handleK8sAuth(w http.ResponseWriter, r *http.Request) {
	saToken, err := auth.ParseSAToken(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return
	}

	subject, err := auth.ValidateSAToken(r.Context(), s.k8s, saToken, nil)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return
	}

	subjectMapRaw := os.Getenv("K8S_AUTH_SUBJECT_MAP")
	subjectMap := auth.K8sAuthSubjectMap(subjectMapRaw)
	email, ok := subjectMap[subject.Qualified()]
	if !ok {
		writeError(w, http.StatusForbidden, "subject not in allowed map: "+subject.Qualified())
		return
	}

	// K8s-SA exchanges are platform-internal callers (currently the
	// mcp-github sidecar minting a tank-operator session to call back
	// for installation_id resolution). Stamp role=user so the resulting
	// JWT passes the verifier's role check; SA-bound endpoints do their
	// own authorization layer on top of authentication.
	token, err := s.minter.MintSession(subject.Qualified(), email, "", "user")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// handleGitHubInstallURL returns a redirect URL for the GitHub App install flow.
func (s *appServer) handleGitHubInstallURL(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	stateToken, err := s.minter.MintInstallState(user.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint state: "+err.Error())
		return
	}

	appSlug := envDefault("GITHUB_APP_SLUG", "tank-operator-romaine-life")
	redirectURL := "https://github.com/apps/" + appSlug + "/installations/new?state=" + stateToken

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// handleGitHubInstallCallback validates GitHub's install callback.
func (s *appServer) handleGitHubInstallCallback(w http.ResponseWriter, r *http.Request) {
	tankUIHost := envDefault("TANK_UI_HOST", "https://tank.romaine.life")

	stateToken := r.URL.Query().Get("state")
	if stateToken == "" {
		http.Redirect(w, r, tankUIHost+"/?install_error=missing_state", http.StatusFound)
		return
	}

	stateEmail, err := s.minter.VerifyInstallState(stateToken)
	if err != nil {
		http.Redirect(w, r, tankUIHost+"/?install_error=invalid_state", http.StatusFound)
		return
	}

	// Verify the auth_token cookie's email matches the state email.
	user, userErr := s.verifier.CurrentUser(r)
	if userErr != nil {
		http.Redirect(w, r, tankUIHost+"/?install_error=not_authenticated", http.StatusFound)
		return
	}
	if !strings.EqualFold(user.Email, stateEmail) {
		http.Redirect(w, r, tankUIHost+"/?install_error=email_mismatch", http.StatusFound)
		return
	}

	// Parse installation_id from query.
	installationIDStr := r.URL.Query().Get("installation_id")
	if installationIDStr == "" {
		http.Redirect(w, r, tankUIHost+"/?install_error=missing_installation_id", http.StatusFound)
		return
	}
	var installationID int64
	if _, scanErr := fmt.Sscan(installationIDStr, &installationID); scanErr != nil {
		http.Redirect(w, r, tankUIHost+"/?install_error=invalid_installation_id", http.StatusFound)
		return
	}

	// Update profile.
	if updater, ok := s.profiles.(profilesUpdateStore); ok {
		if _, err := updater.UpdateInstallation(r.Context(), user.Email, installationID, nil); err != nil {
			slog.Warn("installation update failed", "email", user.Email, "error", err)
		}
	}

	http.Redirect(w, r, tankUIHost+"/", http.StatusFound)
}
