package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
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

type mcpToolEntry struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type mcpToolListResponse struct {
	Entries []mcpToolEntry      `json:"entries"`
	Errors  []map[string]string `json:"errors"`
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

type skillEntryResponse struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Source      string `json:"source"`
	Description string `json:"description"`
	BodyPreview string `json:"body_preview"`
}

type skillListResponse struct {
	Entries []skillEntryResponse `json:"entries"`
}

// handleListFiles lists the directory contents at the given path query param.
func (s *appServer) handleListFiles(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
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
for name in os.listdir(p):
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
# Sort: directories first (alphabetical, case-insensitive), then everything
# else (alphabetical, case-insensitive). Matches GitHub / VS Code / Finder.
entries.sort(key=lambda e: (0 if e["type"] == "dir" else 1, e["name"].lower()))
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
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
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
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
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
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
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
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", `python3 - <<'PY'
import json
import os

def parse_skill(path, source):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            text = fh.read(8192)
    except OSError:
        return None

    name = os.path.basename(os.path.dirname(path)) or "skill"
    description = ""
    body = text
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end >= 0:
            frontmatter = text[4:end].splitlines()
            body = text[end + 5:]
            for line in frontmatter:
                key, sep, value = line.partition(":")
                if not sep:
                    continue
                key = key.strip()
                value = value.strip().strip("\"'")
                if key == "name" and value:
                    name = value
                elif key == "description":
                    description = value

    preview = " ".join(body.strip().split())[:240]
    return {
        "name": name,
        "path": path,
        "source": source,
        "description": description,
        "body_preview": preview,
    }

entries = []
seen = set()

config_dir = "/opt/tank/session-config"
if os.path.isdir(config_dir):
    for filename in sorted(os.listdir(config_dir)):
        if not filename.startswith("skills__") or not filename.endswith("__SKILL.md"):
            continue
        entry = parse_skill(os.path.join(config_dir, filename), "bundled")
        if entry and entry["name"] not in seen:
            seen.add(entry["name"])
            entries.append(entry)

roots = [
    ("/home/node/.codex/skills", "codex"),
    ("/home/node/.claude/skills", "claude"),
    ("/workspace", "workspace"),
]
for root, source in roots:
    if not os.path.isdir(root):
        continue
    for current, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in {".git", "node_modules"}]
        if "SKILL.md" not in files:
            continue
        path = os.path.join(current, "SKILL.md")
        entry = parse_skill(path, source)
        dedupe_key = entry["name"] if entry else ""
        if entry and dedupe_key not in seen:
            seen.add(dedupe_key)
            entries.append(entry)

print(json.dumps({"entries": entries}))
PY`})
	if err != nil {
		writeJSON(w, http.StatusOK, skillListResponse{Entries: []skillEntryResponse{}})
		return
	}

	var body skillListResponse
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse skills: "+err.Error())
		return
	}
	if body.Entries == nil {
		body.Entries = []skillEntryResponse{}
	}
	writeJSON(w, http.StatusOK, body)
}

// handleListMCPServers lists MCP server entries from the session config.
func (s *appServer) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
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

// handleListMCPTools lists concrete MCP tools exposed inside the session pod.
func (s *appServer) handleListMCPTools(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	_, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", `python3 - <<'PY'
import json
import urllib.error
import urllib.request

try:
    with open("/workspace/.mcp.json", "r", encoding="utf-8") as fh:
        config = json.load(fh)
except Exception:
    config = {}

def sse_json(body):
    text = body.decode("utf-8", "replace")
    for line in text.splitlines():
        if line.startswith("data: "):
            try:
                return json.loads(line[6:])
            except json.JSONDecodeError:
                continue
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return {}

entries = []
errors = []
for server, raw in sorted((config.get("mcpServers") or {}).items()):
    if not isinstance(raw, dict):
        continue
    url = str(raw.get("url") or "").strip()
    if not url:
        continue
    payload = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/list",
        "params": {},
    }).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=4) as resp:
            msg = sse_json(resp.read())
    except Exception as exc:
        errors.append({"server": server, "error": str(exc)})
        continue
    tools = (((msg.get("result") or {}).get("tools")) or [])
    for tool in tools:
        if not isinstance(tool, dict):
            continue
        name = str(tool.get("name") or "").strip()
        if not name:
            continue
        entries.append({
            "server": server,
            "name": name,
            "description": str(tool.get("description") or "").strip(),
        })

print(json.dumps({"entries": entries, "errors": errors}))
PY`})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []mcpToolEntry{}, "errors": []map[string]string{}})
		return
	}

	var body struct {
		Entries []mcpToolEntry      `json:"entries"`
		Errors  []map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse MCP tools: "+err.Error())
		return
	}
	if body.Entries == nil {
		body.Entries = []mcpToolEntry{}
	}
	if body.Errors == nil {
		body.Errors = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, body)
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
// Write-side gate: an admin token can NOT pass this helper into another
// user's pod — admin lift is read-only by construction.
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

// resolveSessionPodForRead is the read-side parallel: admin can resolve
// any session pod; non-admin still gets per-owner gating (404 on miss).
// Used by file/MCP/skill READ handlers. Write handlers (uploads,
// edits, terminal attach) intentionally keep calling resolveSessionPod
// — see auth_session.go authorizeSessionRead for the rationale.
func (s *appServer) resolveSessionPodForRead(ctx context.Context, user auth.User, sessionID string) (sessions.Info, string, *podResolveError) {
	info, status, err := s.authorizeSessionRead(ctx, user, sessionID)
	if err != nil {
		return sessions.Info{}, "", &podResolveError{status, err.Error()}
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
