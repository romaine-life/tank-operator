package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

const (
	maxFileBytes = 262144  // 256 KiB
	maxRawBytes  = 8388608 // 8 MiB
)

type mcpServerEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Target    string `json:"target"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
}

type fileEntryResponse struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Size      int64   `json:"size"`
	GitHubURL *string `json:"github_url"`
}

type fileListResponse struct {
	Path    string              `json:"path"`
	Entries []fileEntryResponse `json:"entries"`
}

type selectedFileResponse struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Text      string `json:"text"`
	Binary    bool   `json:"binary"`
}

// handleListFiles lists the directory contents at the given path query param.
func (s *appServer) handleListFiles(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/workspace"
	}
	absPath, err := safeWorkspacePath(dirPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	script := fmt.Sprintf(
		`python3 - %s %s <<'PY'
import json
import os
import stat
import sys

p = sys.argv[1]
rel_path = sys.argv[2]
entries = []
for name in sorted(os.listdir(p), key=lambda s: s.lower()):
    full = os.path.join(p, name)
    try:
        st = os.lstat(full)
    except OSError:
        continue
    mode = st.st_mode
    if stat.S_ISLNK(mode):
        typ = "symlink"
    elif stat.S_ISDIR(mode):
        typ = "dir"
    elif stat.S_ISREG(mode):
        typ = "file"
    else:
        typ = "other"
    entries.append({
        "name": name,
        "type": typ,
        "size": 0 if typ == "dir" else st.st_size,
        "github_url": None,
    })
print(json.dumps({"path": rel_path, "entries": entries}))
PY`,
		shellQuote(absPath),
		shellQuote(workspaceRelPath(absPath)),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body fileListResponse
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse dir listing: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleGetFileContent returns the first 262144 bytes of a file as text.
func (s *appServer) handleGetFileContent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	absPath, err := safeWorkspacePath(filePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	script := fmt.Sprintf(
		`python3 - %s %d %s <<'PY'
import json
import os
import sys

p = sys.argv[1]
max_bytes = int(sys.argv[2])
rel_path = sys.argv[3]
st = os.stat(p)
with open(p, "rb") as fh:
    data = fh.read(max_bytes + 1)
truncated = len(data) > max_bytes
data = data[:max_bytes]
try:
    text = data.decode("utf-8")
    binary = False
except UnicodeDecodeError:
    text = ""
    binary = True
print(json.dumps({
    "path": rel_path,
    "size": st.st_size,
    "truncated": truncated,
    "text": text,
    "binary": binary,
}))
PY`,
		shellQuote(absPath),
		maxFileBytes,
		shellQuote(workspaceRelPath(absPath)),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body selectedFileResponse
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse file content: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleGetFileRaw returns raw file bytes (up to 8 MiB).
func (s *appServer) handleGetFileRaw(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	absPath, err := safeWorkspacePath(filePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"head", "-c", fmt.Sprintf("%d", maxRawBytes), "--", absPath})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// handleWalkFiles recursively walks a directory and returns entries.
func (s *appServer) handleWalkFiles(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/workspace"
	}
	absPath, err := safeWorkspacePath(dirPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	script := fmt.Sprintf(
		`python3 - %s <<'PY'
import json
import os
import sys

p = sys.argv[1]
root = "/workspace"
paths = []
for current, dirs, files in os.walk(p):
    dirs[:] = sorted(
        [d for d in dirs if d not in {".git", "node_modules"}],
        key=lambda s: s.lower(),
    )
    for name in sorted(files, key=lambda s: s.lower()):
        rel = os.path.relpath(os.path.join(current, name), root)
        if not rel.startswith(".." + os.sep) and rel != "..":
            paths.append(rel)
print(json.dumps({"paths": paths}))
PY`,
		shellQuote(absPath),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse walk: "+err.Error())
		return
	}
	if body.Paths == nil {
		body.Paths = []string{}
	}
	writeJSON(w, http.StatusOK, body)
}

// handleUploadFile uploads raw body as a file.
func (s *appServer) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name")
		return
	}
	destPath, err := safeWorkspacePath(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxRawBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := kubeexec.WriteFile(r.Context(), s.k8s, s.restCfg, s.namespace, podName, destPath, data); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     workspaceRelPath(destPath),
		"abs_path": destPath,
		"name":     name,
		"size":     len(data),
	})
}

// handleWriteFile writes text content to a file.
func (s *appServer) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	absPath, err := safeWorkspacePath(filePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if err := kubeexec.WriteFile(r.Context(), s.k8s, s.restCfg, s.namespace, podName, absPath, []byte(body.Text)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, selectedFileResponse{
		Path:      workspaceRelPath(absPath),
		Size:      int64(len([]byte(body.Text))),
		Truncated: false,
		Text:      body.Text,
		Binary:    false,
	})
}

// handleListSkills lists SKILL.md files in the session.
func (s *appServer) handleListSkills(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", "find /workspace -name 'SKILL.md' 2>/dev/null | sort"})
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}

	var skills []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			skills = append(skills, line)
		}
	}
	if skills == nil {
		skills = []string{}
	}
	writeJSON(w, http.StatusOK, skills)
}

// handleListMCPServers lists MCP server entries from the session config.
func (s *appServer) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", "cat /workspace/.mcp.json 2>/dev/null || echo '{}'"})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []mcpServerEntry{}})
		return
	}

	var mcpConfig map[string]any
	if err := json.Unmarshal(out, &mcpConfig); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []mcpServerEntry{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": parseMCPServerEntries(mcpConfig, "/workspace/.mcp.json"),
	})
}

func parseMCPServerEntries(config map[string]any, source string) []mcpServerEntry {
	rawServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return []mcpServerEntry{}
	}

	entries := make([]mcpServerEntry, 0, len(rawServers))
	for name, raw := range rawServers {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		transport := stringValue(value["type"])
		command := stringValue(value["command"])
		if transport == "" {
			if command != "" {
				transport = "stdio"
			} else {
				transport = "unknown"
			}
		}
		entries = append(entries, mcpServerEntry{
			Name:      name,
			Transport: transport,
			Target:    firstNonEmpty(stringValue(value["url"]), command),
			Source:    source,
			Enabled:   true,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ── Helpers ──────────────────────────────────────────────────────────────────

type podResolveError struct {
	status int
	msg    string
}

// resolveSessionPod validates ownership and returns the pod name.
func (s *appServer) resolveSessionPod(ctx context.Context, email, sessionID string) (sessions.Info, string, *podResolveError) {
	info, err := s.mgr.GetByOwner(ctx, email, sessionID)
	if err != nil {
		return sessions.Info{}, "", &podResolveError{http.StatusNotFound, "session not found"}
	}
	if info.PodName == nil {
		return sessions.Info{}, "", &podResolveError{http.StatusServiceUnavailable, "session pod not ready"}
	}
	return info, *info.PodName, nil
}

// shellQuote single-quotes a string for use in shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func workspaceRelPath(absPath string) string {
	rel := strings.TrimPrefix(absPath, workspaceRoot)
	rel = strings.TrimPrefix(rel, "/")
	return rel
}
