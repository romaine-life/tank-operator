package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

const workspaceRoot = "/workspace"

var (
	runIDPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	headlessArgPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	skillNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
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

func validateRunID(v string) string {
	if runIDPattern.MatchString(v) {
		return v
	}
	return auth.RandomHex(12)
}

func validateHeadlessArg(v string) string {
	if headlessArgPattern.MatchString(v) {
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

// requireAuth extracts the user from the request, writes an error and returns
// false if auth fails.
func (s *appServer) requireAuth(w http.ResponseWriter, r *http.Request) (user auth.User, ok bool) {
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return auth.User{}, false
	}
	return user, true
}

// requireWSAuth extracts the user from a WebSocket upgrade request.
func (s *appServer) requireWSAuth(w http.ResponseWriter, r *http.Request) (user auth.User, ok bool) {
	user, err := s.verifier.CurrentUserFromWebSocket(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return auth.User{}, false
	}
	return user, true
}
