package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
)

const workspaceRoot = "/workspace"

var (
	turnIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	turnArgPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"detail": message})
}

func safeWorkspacePath(path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/workspace/" + path
	}
	abs := filepath.Clean(path)
	if !strings.HasPrefix(abs, workspaceRoot+"/") && abs != workspaceRoot {
		return "", &pathEscapeError{abs}
	}
	return abs, nil
}

type pathEscapeError struct{ path string }

func (e *pathEscapeError) Error() string { return "path escapes workspace: " + e.path }

func validateTurnArg(v string) string {
	if turnArgPattern.MatchString(v) {
		return v
	}
	return ""
}

func validateSkillName(v string) string {
	if skillNamePattern.MatchString(v) {
		return v
	}
	return ""
}

// allowedClaudeEfforts is the canonical Claude extended-thinking effort
// allowlist. Mirrors the EffortLevel union in @anthropic-ai/claude-agent-sdk
// so a typo or stale UI value can't poison the runner's options at pod boot.
// Empty input is allowed and means "use the runner's baked-in default" — keep
// that mapping intact.
//
// Keep this list in lockstep with frontend/src/App.tsx CLAUDE_EFFORTS
// and agent-runner/src/runner.ts DEFAULT_EFFORT. The runner does NOT
// re-validate (it trusts whatever lands on the wire) so this is the
// single point of allowlist enforcement.
var allowedClaudeEfforts = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
	"max":    {},
}

// allowedCodexEfforts mirrors @openai/codex-sdk's modelReasoningEffort values
// exposed in Tank. Codex models do not accept Claude's "max" value.
var allowedCodexEfforts = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
}

func validateEffort(provider string, v string) string {
	if v == "" {
		return ""
	}
	allowed := allowedClaudeEfforts
	if provider == "codex" {
		allowed = allowedCodexEfforts
	}
	if _, ok := allowed[v]; ok {
		return v
	}
	return ""
}

// requireAuth extracts the user from the request, writes an error and returns
// false if auth fails. On success the user's email is stashed on the
// per-request metadata struct the HTTP middleware threads through so a
// later 5xx slog line carries the caller's identity.
func (s *appServer) requireAuth(w http.ResponseWriter, r *http.Request) (user auth.User, ok bool) {
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return auth.User{}, false
	}
	attachAuthToRequest(r, user)
	return user, true
}

// requireBrowserStreamAuth extracts the user from a browser-native streaming
// request. Native EventSource callers cannot attach Authorization headers, so
// only these handlers accept a short-lived opaque stream_ticket minted through
// a normal Authorization-bearing fetch.
func (s *appServer) requireBrowserStreamAuth(w http.ResponseWriter, r *http.Request, streamKind, sessionID string) (user auth.User, ok bool) {
	if s.streamAuthTickets == nil {
		recordStreamAuthTicket("validate", streamKind, "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "stream auth ticket store not configured")
		return auth.User{}, false
	}
	token := strings.TrimSpace(r.URL.Query().Get("stream_ticket"))
	ticket, err := s.streamAuthTickets.Validate(r.Context(), token, streamKind, s.sessionScope, sessionID)
	if err != nil {
		if errors.Is(err, pgstore.ErrStreamAuthTicketInvalid) {
			recordStreamAuthTicket("validate", streamKind, "invalid")
			writeError(w, http.StatusUnauthorized, err.Error())
			return auth.User{}, false
		}
		recordStreamAuthTicket("validate", streamKind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return auth.User{}, false
	}
	recordStreamAuthTicket("validate", streamKind, "ok")
	user = auth.User{
		Sub:        ticket.Sub,
		Email:      ticket.Email,
		Name:       ticket.Name,
		Role:       ticket.Role,
		ActorEmail: ticket.ActorEmail,
	}
	attachAuthToRequest(r, user)
	return user, true
}

// requireWSAuth extracts the user from a WebSocket upgrade request.
func (s *appServer) requireWSAuth(w http.ResponseWriter, r *http.Request) (user auth.User, ok bool) {
	user, err := s.verifier.CurrentUserFromWebSocket(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return auth.User{}, false
	}
	attachAuthToRequest(r, user)
	return user, true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
