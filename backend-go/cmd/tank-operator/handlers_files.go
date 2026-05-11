package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

const (
	maxFileBytes = 262144  // 256 KiB
	maxRawBytes  = 8388608 // 8 MiB
)

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
		`python3 -c "import os,json; p=%s; entries=[{'name':e,'is_dir':os.path.isdir(os.path.join(p,e))} for e in os.listdir(p)]; print(json.dumps(entries))"`,
		shellQuote(absPath),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var entries []map[string]any
	if err := json.Unmarshal(out, &entries); err != nil {
		writeError(w, http.StatusInternalServerError, "parse dir listing: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
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

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"head", "-c", fmt.Sprintf("%d", maxFileBytes+1), "--", absPath})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	truncated := len(out) > maxFileBytes
	if truncated {
		out = out[:maxFileBytes]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":   string(out),
		"truncated": truncated,
	})
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
		`python3 -c "import os,json; p=%s; result=[]; [result.extend([{'path':os.path.join(root,f),'is_dir':False} for f in files]+[{'path':os.path.join(root,d),'is_dir':True} for d in dirs]) for root,dirs,files in os.walk(p)]; print(json.dumps(result))"`,
		shellQuote(absPath),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var entries []map[string]any
	if err := json.Unmarshal(out, &entries); err != nil {
		writeError(w, http.StatusInternalServerError, "parse walk: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
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
	writeJSON(w, http.StatusOK, map[string]string{"path": destPath})
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
	writeJSON(w, http.StatusOK, map[string]string{"path": absPath})
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
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	var mcpConfig map[string]any
	if err := json.Unmarshal(out, &mcpConfig); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, mcpConfig)
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
