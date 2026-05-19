package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

// authorizeSessionRead resolves a session by id and decides whether the
// caller is allowed to read it.
//
// Read paths: events list + SSE stream, single-session metadata, read-state
// marker, file reads, MCP listings, skills listings. Write paths (turns,
// uploads, terminal attach, name/test/rollout patches, delete) intentionally
// keep their per-owner GetByOwner gate so an admin token cannot mutate
// someone else's session — admin lift is read-only by construction.
//
// Authorization rule:
//   - role=admin     → allowed for any owner
//   - role=service   → allowed only when info.Owner == user.ActorEmail
//   - role=user/etc  → allowed only when info.Owner == user.Email
//
// On a cross-user denial for a non-admin, returns 404 (not 403) so the
// API surface doesn't leak the existence of sessions a caller can't
// read. Same shape ErrNotFound takes when the session id genuinely
// doesn't exist. Admin reads of nonexistent sessions also get a clean
// 404 — no need to differentiate.
//
// Returns the resolved sessions.Info on allow so callers can re-use the
// pod name / owner / status without a second round-trip.
func (s *appServer) authorizeSessionRead(
	ctx context.Context,
	user auth.User,
	sessionID string,
) (sessions.Info, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessions.Info{}, http.StatusBadRequest, errors.New("missing session_id")
	}

	if user.Role != auth.RoleAdmin {
		owner := user.OwnerEmail()
		registered, regErr := s.mgr.GetRegisteredByOwner(ctx, owner, sessionID)
		if regErr == nil {
			return registered, http.StatusOK, nil
		}
		if regErr != nil && !errors.Is(regErr, sessions.ErrNotFound) {
			return sessions.Info{}, http.StatusInternalServerError, regErr
		}
	}

	info, err := s.mgr.GetByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
		}
		return sessions.Info{}, http.StatusInternalServerError, err
	}
	if user.Role == auth.RoleAdmin {
		if info.Owner != user.Email {
			recordAdminCrossUserRead()
		}
		return info, http.StatusOK, nil
	}
	owner := user.OwnerEmail()
	if !strings.EqualFold(info.Owner, owner) {
		// Mask existence — same 404 the caller would have seen if the
		// session truly didn't exist. Don't surface owner email; that
		// would leak who owns the session id.
		return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
	}
	return info, http.StatusOK, nil
}

// listSessionsOwner returns the email to scope a list-sessions / list-events
// query to. Admins can pass `?owner=<email>` to view someone else's list;
// non-admins always get their own list regardless of the query param.
// Empty `?owner=` reads as "self" for admins too — the explicit signal is
// what unlocks the cross-user path.
func listSessionsOwner(user auth.User, r *http.Request) string {
	queryOwner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if user.Role == auth.RoleAdmin && queryOwner != "" {
		recordAdminCrossUserList()
		return queryOwner
	}
	return user.OwnerEmail()
}
