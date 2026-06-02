// handlers_factory.go provides standalone handler factory functions used by
// main_test.go to exercise individual handler logic without spinning up the
// full appServer.
package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

// sessionReader is the minimal interface needed for list/get session handlers.
type sessionReader interface {
	List(ctx context.Context, owner string) ([]sessions.Info, error)
	Get(ctx context.Context, owner, sessionID string) (sessions.Info, error)
}

// config returns the public configuration (auth.romaine.life URL etc.).
// Standalone version for tests.
func config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, publicConfig())
}

// authenticatedListSessions returns a handler that lists sessions for the
// authenticated user. The reader is injected for testability.
func authenticatedListSessions(verifier *auth.Verifier, reader sessionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := verifier.CurrentUser(r)
		if err != nil {
			writeError(w, auth.ErrorStatus(err), err.Error())
			return
		}
		infos, err := reader.List(r.Context(), user.OwnerEmail())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, infos)
	}
}

// authenticatedGetSession returns a handler that returns a single session for
// the authenticated user.
func authenticatedGetSession(verifier *auth.Verifier, reader sessionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := verifier.CurrentUser(r)
		if err != nil {
			writeError(w, auth.ErrorStatus(err), err.Error())
			return
		}
		sessionID := r.PathValue("session_id")
		info, err := reader.Get(r.Context(), user.OwnerEmail(), sessionID)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, info)
		case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusNotFound, "session not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
	}
}

// profileGetter is the minimal interface needed for the /me handler.
type profileGetter interface {
	GetOrCreate(ctx context.Context, email string) (profiles.Profile, error)
}

// me returns a handler that returns the current user's profile.
func me(verifier *auth.Verifier, store profileGetter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := verifier.CurrentUser(r)
		if err != nil {
			writeError(w, auth.ErrorStatus(err), err.Error())
			return
		}
		profile, err := store.GetOrCreate(r.Context(), user.OwnerEmail())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, userResponseBody(user.Sub, user.Email, user.Name, user.Role, hasAdminPower(user), profile))
	}
}
