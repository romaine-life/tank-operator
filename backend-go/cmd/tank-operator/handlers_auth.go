package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

const (
	gitHubInstallStateTTL = 10 * time.Minute
	streamAuthTicketTTL   = 2 * time.Minute

	streamKindSessionList   = "session-list"
	streamKindSessionEvents = "session-events"
	streamKindPinnedRepos   = "pinned-repos"
)

type gitHubInstallStateStore interface {
	Create(ctx context.Context, state, email string, expiresAt time.Time) error
	AttachInstallation(ctx context.Context, state string, installationID int64) error
	Consume(ctx context.Context, state, email string) (int64, error)
}

// userResponseBody is the canonical JSON shape returned for the SPA's
// `user` object. /api/auth/me and the GitHub install completion path both
// return this shape so profile fields cannot drift between auth entrypoints.
//
// `role` rides along so the SPA's OnboardingWall can skip itself for callers
// that do not need a user-facing GitHub installation: admins (covered by the
// host installation) and service principals (platform-internal test/session
// automation). `is_admin` is Tank's local admin-power decision; admin-owned
// service principals keep role=service but get the same Tank admin access as
// their actor.
func userResponseBody(sub, email, name, role string, isAdmin bool, profile profiles.Profile) map[string]any {
	pinnedRepos := profile.PinnedRepos
	if pinnedRepos == nil {
		pinnedRepos = []string{}
	}
	return map[string]any{
		"sub":                sub,
		"email":              email,
		"name":               name,
		"role":               role,
		"is_admin":           isAdmin,
		"avatar_url":         auth.GravatarURL(email, 64),
		"github_login":       profile.GitHubLogin,
		"installation_id":    profile.InstallationID,
		"profile_updated_at": profile.UpdatedAt,
		"pinned_repos":       pinnedRepos,
		"run_prefs":          profile.RunPrefs,
	}
}

// handleMe returns user info + profile for the upstream auth.romaine.life JWT
// presented directly by the caller.
func (s *appServer) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	profile, err := s.profiles.GetOrCreate(r.Context(), user.OwnerEmail())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, userResponseBody(user.Sub, user.Email, user.Name, user.Role, hasAdminPower(user), profile))
}

// handleUpdatePrefs persists the SPA's run-pane preferences (chat font
// scale, sound volume, etc.) on the caller's Postgres profiles row. The
// body shape is opaque to the orchestrator; the SPA owns the schema
// (frontend/src/App.tsx -> RunPrefs). The store does a merge-safe
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
	profile, err := prefsStore.UpdatePrefs(r.Context(), user.OwnerEmail(), body.RunPrefs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_prefs": profile.RunPrefs,
	})
}

func (s *appServer) handleCreateStreamTicket(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.streamAuthTickets == nil {
		recordStreamAuthTicket("create", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "stream auth ticket store not configured")
		return
	}

	var body struct {
		Stream       string `json:"stream"`
		SessionID    string `json:"session_id"`
		SessionScope string `json:"session_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		recordStreamAuthTicket("create", "", "invalid")
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	streamKind := strings.TrimSpace(body.Stream)
	sessionID := strings.TrimSpace(body.SessionID)
	sessionScope, status, scopeErr := s.resolveSessionScope(user, body.SessionScope)
	if scopeErr != nil {
		recordStreamAuthTicket("create", streamKind, "denied")
		writeError(w, status, scopeErr.Error())
		return
	}
	switch streamKind {
	case streamKindSessionList:
		sessionID = ""
	case streamKindPinnedRepos:
		sessionID = ""
	case streamKindSessionEvents:
		if sessionID == "" {
			recordStreamAuthTicket("create", streamKind, "invalid")
			writeError(w, http.StatusBadRequest, "session_id is required for session event streams")
			return
		}
		if _, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
			recordStreamAuthTicket("create", streamKind, "denied")
			writeError(w, status, err.Error())
			return
		}
	default:
		recordStreamAuthTicket("create", streamKind, "invalid")
		writeError(w, http.StatusBadRequest, "unknown stream")
		return
	}

	expiresAt := time.Now().Add(streamAuthTicketTTL)
	ticket := auth.RandomHex(32)
	if err := s.streamAuthTickets.Create(r.Context(), pgstore.StreamAuthTicket{
		Ticket:       ticket,
		Sub:          user.Sub,
		Email:        user.Email,
		Name:         user.Name,
		Role:         user.Role,
		ActorEmail:   user.ActorEmail,
		StreamKind:   streamKind,
		SessionScope: sessionScope,
		SessionID:    sessionID,
		ExpiresAt:    expiresAt,
	}); err != nil {
		recordStreamAuthTicket("create", streamKind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordStreamAuthTicket("create", streamKind, "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"ticket":     ticket,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// handleGitHubInstallURL returns a redirect URL for the GitHub App install
// flow. The state is an opaque, single-use Postgres nonce bound to the
// authenticated caller; it is not a JWT and carries no authority by itself.
func (s *appServer) handleGitHubInstallURL(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.gitHubInstallStates == nil {
		writeError(w, http.StatusServiceUnavailable, "github install state store not configured")
		return
	}

	state := auth.RandomHex(32)
	if err := s.gitHubInstallStates.Create(r.Context(), state, user.Email, time.Now().Add(gitHubInstallStateTTL)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create install state: "+err.Error())
		return
	}

	appSlug := envDefault("GITHUB_APP_SLUG", "tank-operator-romaine-life")
	redirectURL := "https://github.com/apps/" + appSlug + "/installations/new?state=" + url.QueryEscape(state)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// handleGitHubInstallCallback receives GitHub's unauthenticated browser
// callback. It records the installation_id on the opaque state, then sends the
// browser back to the SPA. The SPA finalizes with Authorization: Bearer
// <auth.romaine.life JWT>, which preserves the email-match defense that the
// retired Tank httpOnly cookie used to provide.
func (s *appServer) handleGitHubInstallCallback(w http.ResponseWriter, r *http.Request) {
	tankUIHost := envDefault("TANK_UI_HOST", "https://tank.romaine.life")
	redirectErr := func(reason string) {
		http.Redirect(w, r, tankUIHost+"/?install_error="+url.QueryEscape(reason), http.StatusFound)
	}

	if s.gitHubInstallStates == nil {
		redirectErr("install_state_unavailable")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		redirectErr("missing_state")
		return
	}

	installationIDStr := r.URL.Query().Get("installation_id")
	if installationIDStr == "" {
		redirectErr("missing_installation_id")
		return
	}
	var installationID int64
	if _, scanErr := fmt.Sscan(installationIDStr, &installationID); scanErr != nil {
		redirectErr("invalid_installation_id")
		return
	}

	if err := s.gitHubInstallStates.AttachInstallation(r.Context(), state, installationID); err != nil {
		if errors.Is(err, pgstore.ErrGitHubInstallStateInvalid) {
			redirectErr("invalid_state")
			return
		}
		slog.Warn("github install state attach failed", "error", err)
		redirectErr("install_state_failed")
		return
	}

	landing := tankUIHost + "/?github_install_state=" + url.QueryEscape(state)
	http.Redirect(w, r, landing, http.StatusFound)
}

// handleGitHubInstallComplete consumes a GitHub install state after the SPA
// has bootstrapped auth.romaine.life authentication. This is where the state
// email and the verified JWT email must match.
func (s *appServer) handleGitHubInstallComplete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.gitHubInstallStates == nil {
		writeError(w, http.StatusServiceUnavailable, "github install state store not configured")
		return
	}

	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.State == "" {
		writeError(w, http.StatusBadRequest, "missing state")
		return
	}

	installationID, err := s.gitHubInstallStates.Consume(r.Context(), body.State, user.Email)
	if err != nil {
		switch {
		case errors.Is(err, pgstore.ErrGitHubInstallStateEmailMismatch):
			writeError(w, http.StatusForbidden, "email_mismatch")
		case errors.Is(err, pgstore.ErrGitHubInstallStateInvalid):
			writeError(w, http.StatusBadRequest, "invalid_state")
		default:
			writeError(w, http.StatusInternalServerError, "install state consume failed: "+err.Error())
		}
		return
	}

	updater, ok := s.profiles.(profilesUpdateStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "profile store does not support installation updates")
		return
	}
	profile, err := updater.UpdateInstallation(r.Context(), user.Email, installationID, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userResponseBody(user.Sub, user.Email, user.Name, user.Role, hasAdminPower(user), profile),
	})
}
