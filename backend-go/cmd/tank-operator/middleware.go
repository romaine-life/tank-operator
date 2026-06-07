package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const workspaceRoot = "/workspace"

var (
	turnIDPattern           = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	backgroundTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)
	turnArgPattern          = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	skillNamePattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
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

func validateModelArg(provider, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if allowed := allowedModelsForProvider(provider); allowed != nil {
		for _, model := range allowed {
			if v == model {
				return v
			}
		}
		return ""
	}
	if len([]byte(v)) > 128 {
		return ""
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return v
}

var providerModels = map[string][]string{
	"claude": {
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	},
	"codex": {
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex-spark",
	},
	"antigravity": {
		"Gemini 3.5 Flash (Medium)",
	},
}

func allowedModelsForProvider(provider string) []string {
	models, ok := providerModels[provider]
	if !ok {
		return nil
	}
	return append([]string(nil), models...)
}

// providerEfforts is the canonical extended-thinking effort allowlist.
// Claude mirrors the EffortLevel union in @anthropic-ai/claude-agent-sdk,
// and Codex mirrors @openai/codex-sdk's modelReasoningEffort values, so a
// typo or stale UI value can't poison the runner's options at pod boot.
// Empty input is allowed and means "use the runner's baked-in default" — keep
// that mapping intact.
//
// sessionRunOptions() advertises these values to the frontend and MCP. Keep
// the defaults in lockstep with agent-runner/src/runner.ts DEFAULT_EFFORT.
// The runner does NOT re-validate (it trusts whatever lands on the wire) so
// this is the single point of allowlist enforcement.
var providerEfforts = map[string][]string{
	"claude": {"low", "medium", "high", "xhigh", "max"},
	"codex":  {"low", "medium", "high", "xhigh"},
}

func allowedEffortsForProvider(provider string) []string {
	efforts, ok := providerEfforts[provider]
	if !ok {
		return []string{}
	}
	return append([]string(nil), efforts...)
}

func modelUnsupportedMessage(provider string) string {
	models := allowedModelsForProvider(provider)
	if len(models) == 0 {
		return "model is invalid"
	}
	return "model is not available for " + provider + "; want one of " + strings.Join(models, "|")
}

func validateSkillName(v string) string {
	if skillNamePattern.MatchString(v) {
		return v
	}
	return ""
}

func validateEffort(provider string, v string) string {
	if v == "" {
		return ""
	}
	for _, effort := range allowedEffortsForProvider(provider) {
		if v == effort {
			return v
		}
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
func (s *appServer) requireBrowserStreamAuth(w http.ResponseWriter, r *http.Request, streamKind, sessionID string) (user auth.User, sessionScope string, ok bool) {
	if s.streamAuthTickets == nil {
		recordStreamAuthTicket("validate", streamKind, "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "stream auth ticket store not configured")
		return auth.User{}, "", false
	}
	token := strings.TrimSpace(r.URL.Query().Get("stream_ticket"))
	requestedScope := strings.TrimSpace(r.URL.Query().Get("session_scope"))
	if requestedScope == "" {
		requestedScope = s.localSessionScope()
	} else {
		requestedScope = normalizeSessionScope(requestedScope)
	}
	ticket, err := s.streamAuthTickets.Validate(r.Context(), token, streamKind, requestedScope, sessionID)
	if err != nil {
		if errors.Is(err, pgstore.ErrStreamAuthTicketInvalid) {
			recordStreamAuthTicket("validate", streamKind, "invalid")
			writeError(w, http.StatusUnauthorized, err.Error())
			return auth.User{}, "", false
		}
		if isClientCanceled(err) {
			recordStreamAuthTicket("validate", streamKind, "canceled")
			writeError(w, statusClientClosedRequest, "client canceled request")
			return auth.User{}, "", false
		}
		recordStreamAuthTicket("validate", streamKind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return auth.User{}, "", false
	}
	recordStreamAuthTicket("validate", streamKind, "ok")
	user = auth.User{
		Sub:        ticket.Sub,
		Email:      ticket.Email,
		Name:       ticket.Name,
		Role:       ticket.Role,
		ActorEmail: ticket.ActorEmail,
	}
	resolvedScope, status, scopeErr := s.resolveSessionScope(user, requestedScope)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return auth.User{}, "", false
	}
	attachAuthToRequest(r, user)
	return user, resolvedScope, true
}

const statusClientClosedRequest = 499

func isClientCanceled(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	detail := err.Error()
	return strings.Contains(detail, "context canceled") ||
		strings.Contains(detail, "context already done")
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
