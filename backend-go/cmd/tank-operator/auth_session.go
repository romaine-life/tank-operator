package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

const prodSessionScope = "default"

func normalizeSessionScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return prodSessionScope
	}
	return scope
}

func (s *appServer) localSessionScope() string {
	return normalizeSessionScope(s.sessionScope)
}

func (s *appServer) resolveSessionScope(user auth.User, requested string) (string, int, error) {
	local := s.localSessionScope()
	scope := strings.TrimSpace(requested)
	if scope == "" {
		return local, http.StatusOK, nil
	}
	scope = normalizeSessionScope(scope)
	if scope == local {
		return scope, http.StatusOK, nil
	}
	if !hasAdminPower(user) {
		return "", http.StatusForbidden, errors.New("session scope not allowed")
	}
	if local == prodSessionScope || scope != prodSessionScope {
		return "", http.StatusForbidden, errors.New("session scope not allowed")
	}
	return scope, http.StatusOK, nil
}

func (s *appServer) resolveSessionScopeFromRequest(user auth.User, r *http.Request) (string, int, error) {
	return s.resolveSessionScope(user, r.URL.Query().Get("session_scope"))
}

func (s *appServer) sessionRegistryForScope(scope string) *sessionregistry.Store {
	if s.pgPool == nil {
		return nil
	}
	return sessionregistry.NewPostgresStore(s.pgPool, normalizeSessionScope(scope))
}

func (s *appServer) listSessionsInScope(ctx context.Context, owner, scope string) ([]sessions.Info, error) {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() {
		return s.mgr.ListSessions(ctx, owner)
	}
	reg := s.sessionRegistryForScope(scope)
	if reg == nil {
		return nil, nil
	}
	records, err := reg.List(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]sessions.Info, 0, len(records))
	for _, record := range records {
		if !record.Visible {
			continue
		}
		out = append(out, sessions.InfoFromRecord(owner, record))
	}
	return out, nil
}

func (s *appServer) getRegisteredByOwnerInScope(ctx context.Context, owner, sessionID, scope string) (sessions.Info, error) {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() {
		return s.mgr.GetRegisteredByOwner(ctx, owner, sessionID)
	}
	reg := s.sessionRegistryForScope(scope)
	if reg == nil {
		return sessions.Info{}, sessions.ErrNotFound
	}
	record, found, err := reg.Get(ctx, owner, sessionID)
	if err != nil {
		return sessions.Info{}, err
	}
	if !found || !record.Visible {
		return sessions.Info{}, sessions.ErrNotFound
	}
	return sessions.InfoFromRecord(owner, record), nil
}

func (s *appServer) getRegisteredTranscriptByOwnerInScope(ctx context.Context, owner, sessionID, scope string) (sessions.Info, bool, error) {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() {
		return s.mgr.GetRegisteredByOwnerAnyVisibility(ctx, owner, sessionID)
	}
	reg := s.sessionRegistryForScope(scope)
	if reg == nil {
		return sessions.Info{}, false, sessions.ErrNotFound
	}
	record, found, err := reg.Get(ctx, owner, sessionID)
	if err != nil {
		return sessions.Info{}, false, err
	}
	if !found {
		return sessions.Info{}, false, sessions.ErrNotFound
	}
	return sessions.InfoFromRecord(owner, record), record.Visible, nil
}

func (s *appServer) ownerForSessionInScope(ctx context.Context, scope, sessionID string) (string, error) {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() && s.mgr != nil {
		owner, err := s.mgr.RegisteredOwnerForSession(ctx, scope, sessionID)
		if err != nil || owner != "" {
			return owner, err
		}
	}
	reg := s.sessionRegistryForScope(scope)
	if reg == nil {
		return "", nil
	}
	return reg.OwnerForSession(ctx, scope, sessionID)
}

// authorizeSessionRead resolves a session by id and decides whether the
// caller is allowed to read it.
//
// General read paths: SSE stream, single-session metadata, read-state marker,
// file reads, MCP listings, skills listings. Durable transcript history uses
// authorizeSessionTranscriptReadInScope below because sidebar visibility is
// not a transcript-history boundary. Write paths (turns, uploads, terminal
// attach, name/test/rollout patches, delete) intentionally keep their
// per-owner GetByOwner gate so an admin token cannot mutate someone else's
// session — admin lift is read-only by construction.
//
// Authorization rule:
//   - admin power    → allowed for any owner
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
	return s.authorizeSessionReadInScope(ctx, user, sessionID, s.localSessionScope())
}

func (s *appServer) authorizeSessionReadInScope(
	ctx context.Context,
	user auth.User,
	sessionID string,
	scope string,
) (sessions.Info, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessions.Info{}, http.StatusBadRequest, errors.New("missing session_id")
	}
	scope = normalizeSessionScope(scope)

	owner := user.OwnerEmail()
	if registered, regErr := s.getRegisteredByOwnerInScope(ctx, owner, sessionID, scope); regErr == nil {
		return registered, http.StatusOK, nil
	} else if regErr != nil && !errors.Is(regErr, sessions.ErrNotFound) {
		return sessions.Info{}, http.StatusInternalServerError, regErr
	}

	if scope != s.localSessionScope() {
		if hasAdminPower(user) {
			sessionOwner, ownerErr := s.ownerForSessionInScope(ctx, scope, sessionID)
			if ownerErr != nil {
				return sessions.Info{}, http.StatusInternalServerError, ownerErr
			}
			if sessionOwner != "" {
				info, err := s.getRegisteredByOwnerInScope(ctx, sessionOwner, sessionID, scope)
				if err == nil {
					if !strings.EqualFold(info.Owner, owner) {
						recordAdminCrossUserRead()
					}
					return info, http.StatusOK, nil
				}
				if !errors.Is(err, sessions.ErrNotFound) {
					return sessions.Info{}, http.StatusInternalServerError, err
				}
			}
		}
		return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
	}

	info, err := s.mgr.GetByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
		}
		return sessions.Info{}, http.StatusInternalServerError, err
	}
	if hasAdminPower(user) {
		if !strings.EqualFold(info.Owner, owner) {
			recordAdminCrossUserRead()
		}
		return info, http.StatusOK, nil
	}
	if !strings.EqualFold(info.Owner, owner) {
		// Mask existence — same 404 the caller would have seen if the
		// session truly didn't exist. Don't surface owner email; that
		// would leak who owns the session id.
		return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
	}
	return info, http.StatusOK, nil
}

// authorizeSessionTranscriptReadInScope gates durable transcript-history reads.
//
// The key distinction from authorizeSessionReadInScope is visibility:
// sessions.visible is a sidebar/list tombstone, not a history-retention or
// access-control boundary. Copied message links, /timeline, Turn activity, and
// the mcp-tank-operator read_transcript tool are explicit durable-ledger reads,
// so an authorized owner/admin can read a registry row even after the session
// was soft-deleted from the sidebar. Live streams, metadata, files, skills, and
// other read surfaces keep the visible-row gate above.
func (s *appServer) authorizeSessionTranscriptReadInScope(
	ctx context.Context,
	user auth.User,
	sessionID string,
	scope string,
) (sessions.Info, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessions.Info{}, http.StatusBadRequest, errors.New("missing session_id")
	}
	scope = normalizeSessionScope(scope)

	owner := user.OwnerEmail()
	if registered, visible, regErr := s.getRegisteredTranscriptByOwnerInScope(ctx, owner, sessionID, scope); regErr == nil {
		if !visible {
			recordSessionTranscriptInvisibleRead()
		}
		return registered, http.StatusOK, nil
	} else if regErr != nil && !errors.Is(regErr, sessions.ErrNotFound) {
		return sessions.Info{}, http.StatusInternalServerError, regErr
	}

	if hasAdminPower(user) {
		sessionOwner, ownerErr := s.ownerForSessionInScope(ctx, scope, sessionID)
		if ownerErr != nil {
			return sessions.Info{}, http.StatusInternalServerError, ownerErr
		}
		if sessionOwner != "" {
			info, visible, err := s.getRegisteredTranscriptByOwnerInScope(ctx, sessionOwner, sessionID, scope)
			if err == nil {
				if !strings.EqualFold(info.Owner, owner) {
					recordAdminCrossUserRead()
				}
				if !visible {
					recordSessionTranscriptInvisibleRead()
				}
				return info, http.StatusOK, nil
			}
			if !errors.Is(err, sessions.ErrNotFound) {
				return sessions.Info{}, http.StatusInternalServerError, err
			}
		}
	}

	if scope != s.localSessionScope() {
		return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
	}

	info, err := s.mgr.GetByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return sessions.Info{}, http.StatusNotFound, errors.New("session not found")
		}
		return sessions.Info{}, http.StatusInternalServerError, err
	}
	if hasAdminPower(user) {
		if !strings.EqualFold(info.Owner, owner) {
			recordAdminCrossUserRead()
		}
		return info, http.StatusOK, nil
	}
	if !strings.EqualFold(info.Owner, owner) {
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
	if hasAdminPower(user) && queryOwner != "" {
		recordAdminCrossUserList()
		return queryOwner
	}
	return user.OwnerEmail()
}
