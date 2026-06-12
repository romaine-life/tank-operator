package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/kubeexec"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// maxLaunchAttachments bounds the ordinal a launch-attachment upload may
// target. Matches normalizeDisplayAttachments' 32-attachment cap.
const maxLaunchAttachments = 32

const (
	maxFileBytes = 262144  // 256 KiB
	maxRawBytes  = 8388608 // 8 MiB
	// screenshotsRelDir is the workspace-relative directory where image
	// uploads land — pasted screenshots are the main case. The in-pod
	// script picks the next free `<n>.<ext>` slot under this directory
	// using O_EXCL, so two concurrent uploads can't collide on the same
	// id (browsers name every clipboard image `image.png`, and the SPA
	// fires upload requests for every file in a paste event without
	// awaiting).
	screenshotsRelDir = "screenshots"
	// attachmentsRelDir is the workspace-relative directory non-image
	// uploads (PDFs, .txt, etc.) land in. Mirrors the Python orchestrator's
	// pre-Go-rewrite behavior — nanosecond-stamped path keeps repeat
	// uploads of the same source name from overwriting each other.
	attachmentsRelDir = ".attachments"
)

// attachmentNameSanitizer keeps only filesystem-safe ASCII; the rest is
// folded to `_` so a pasted file named e.g. `Notes 2026-05-16.txt` lands
// as `Notes_2026-05-16.txt`.
var attachmentNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// imageExtensionByMIME maps the image MIME types the SPA can produce
// (clipboard pastes are always PNG; drag/drop and file-picker uploads
// can be any of these) to the on-disk extension we want to use. The
// extension is server-controlled — we never trust a filename extension
// for the `screenshots/` rename path — to keep the directory's
// filenames predictable for downstream tools.
var imageExtensionByMIME = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/jpg":     ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/bmp":     ".bmp",
	"image/svg+xml": ".svg",
	"image/heic":    ".heic",
	"image/heif":    ".heif",
	"image/avif":    ".avif",
}

// isImageContentType returns true if contentType is an `image/...` MIME
// type. Case-insensitive on the prefix; parameters (e.g. `; charset=`)
// are tolerated. Used to route uploads to `screenshots/` vs `.attachments/`.
func isImageContentType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return strings.HasPrefix(ct, "image/")
}

// screenshotExtension picks the on-disk extension for an image upload.
// Preference order: known MIME map, sanitized extension off the original
// filename, hard fallback to `.png`. The fallback is correct because
// clipboard pastes from every major browser are PNG.
func screenshotExtension(contentType, rawName string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ext, ok := imageExtensionByMIME[ct]; ok {
		return ext
	}
	if dot := strings.LastIndexByte(rawName, '.'); dot >= 0 && dot < len(rawName)-1 {
		ext := strings.ToLower(rawName[dot:])
		// Allow only short, alnum extensions — keeps a hostile filename
		// like `image.png/../../evil` from poisoning the on-disk name.
		if len(ext) <= 6 {
			ok := true
			for i := 1; i < len(ext); i++ {
				c := ext[i]
				if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
					ok = false
					break
				}
			}
			if ok {
				return ext
			}
		}
	}
	return ".png"
}

// uniqueAttachmentRelPath returns the workspace-relative path a non-image
// upload should land at — `.attachments/<unix-ns>-<sanitized-name>`.
// Image uploads go through the in-pod O_EXCL `screenshots/<n>.<ext>`
// allocator instead; this is the fallback for everything else.
func uniqueAttachmentRelPath(rawName string, now time.Time) string {
	name := rawName
	if name == "" {
		name = "file"
	}
	safe := attachmentNameSanitizer.ReplaceAllString(name, "_")
	if len(safe) > 100 {
		safe = safe[:100]
	}
	if safe == "" {
		safe = "file"
	}
	return fmt.Sprintf("%s/%d-%s", attachmentsRelDir, now.UnixNano(), safe)
}

// screenshotAllocatorScript is the in-pod Python script that picks the
// next free `screenshots/<n><ext>` slot atomically and writes the
// uploaded bytes to it. Atomicity matters because the SPA's paste
// handler at `frontend/src/App.tsx` (`for (const f of fs) void
// uploadAttachment(f)`) fires every clipboard file's upload request
// without awaiting — two screenshots pasted at once arrive at the
// orchestrator within microseconds, and a non-atomic "list then write"
// would collide. O_EXCL on the candidate path is the gate; the loop
// increments past any race-loser's win.
//
// Args (sys.argv): root_dir, extension, expected_size_bytes.
// Stdout (one line of JSON): {"abs_path": "...", "rel_path": "...", "id": N}.
const screenshotAllocatorScript = `import json
import os
import re
import sys

root = sys.argv[1]
ext = sys.argv[2]
size = int(sys.argv[3])

os.makedirs(root, exist_ok=True)
existing = []
for entry in os.listdir(root):
    m = re.match(r'^(\d+)', entry)
    if m:
        try:
            existing.append(int(m.group(1)))
        except ValueError:
            pass
candidate = (max(existing) + 1) if existing else 1

while True:
    abs_path = os.path.join(root, f"{candidate}{ext}")
    try:
        fd = os.open(abs_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o644)
        break
    except FileExistsError:
        candidate += 1

try:
    remaining = size
    while remaining > 0:
        chunk = sys.stdin.buffer.read(min(65536, remaining))
        if not chunk:
            break
        os.write(fd, chunk)
        remaining -= len(chunk)
finally:
    os.close(fd)

print(json.dumps({"abs_path": abs_path, "id": candidate}))
`

const workspacePathBoundaryCheckScript = `import json
import os
import sys

root = os.path.realpath(sys.argv[1])
target = sys.argv[2]

if os.path.lexists(target):
    resolved = os.path.realpath(target)
else:
    parent = os.path.realpath(os.path.dirname(target) or ".")
    resolved = os.path.normpath(os.path.join(parent, os.path.basename(target)))

if resolved != root and not resolved.startswith(root + os.sep):
    print(json.dumps({
        "ok": False,
        "error": "path escapes workspace",
        "resolved_path": resolved,
    }))
    raise SystemExit(0)

print(json.dumps({"ok": True, "resolved_path": resolved}))
`

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
	resolvedPath, err := s.resolveInPodWorkspacePath(r.Context(), podName, absPath)
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
if not os.path.exists(p):
    print(json.dumps({"path": rel_path, "entries": entries, "error": "not_found"}))
    raise SystemExit(0)
if not os.path.isdir(p):
    print(json.dumps({"path": rel_path, "entries": entries, "error": "not_directory"}))
    raise SystemExit(0)
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
		shellQuote(resolvedPath),
		shellQuote(workspaceRelPath(absPath)),
	)
	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body struct {
		Path    string              `json:"path"`
		Entries []fileEntryResponse `json:"entries"`
		Error   string              `json:"error"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		writeError(w, http.StatusInternalServerError, "parse dir listing: "+err.Error())
		return
	}
	switch body.Error {
	case "":
		writeJSON(w, http.StatusOK, fileListResponse{Path: body.Path, Entries: body.Entries})
	case "not_found":
		writeError(w, http.StatusNotFound, "path not found: "+body.Path)
	case "not_directory":
		writeError(w, http.StatusBadRequest, "path is not a directory: "+body.Path)
	default:
		writeError(w, http.StatusInternalServerError, "list dir: "+body.Error)
	}
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
	resolvedPath, err := s.resolveInPodWorkspacePath(r.Context(), podName, absPath)
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
		shellQuote(resolvedPath),
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
	resolvedPath, err := s.resolveInPodWorkspacePath(r.Context(), podName, absPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"head", "-c", fmt.Sprintf("%d", maxRawBytes), "--", resolvedPath})
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
	resolvedPath, err := s.resolveInPodWorkspacePath(r.Context(), podName, absPath)
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
		shellQuote(resolvedPath),
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
	owner := user.OwnerEmail()
	_, podName, herr := s.resolveSessionPod(r.Context(), owner, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name")
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxRawBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	contentType := r.Header.Get("Content-Type")
	if isImageContentType(contentType) {
		destPath, err := allocateScreenshotPath(r.Context(), s, podName, contentType, name, data)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path":     workspaceRelPath(destPath),
			"abs_path": destPath,
			"name":     name,
			"size":     len(data),
		})
		return
	}

	// Non-image fallback: `.attachments/<ns>-<sanitized>` so two
	// uploads of the same source name don't overwrite each other.
	// `safeWorkspacePath` still guards path escapes; the sanitizer
	// strips `/` and the prefix is server-built.
	relPath := uniqueAttachmentRelPath(name, time.Now())
	destPath, err := safeWorkspacePath(relPath)
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

// handleStageLaunchAttachment durably stages one attachment's bytes for an
// attachment-backed deferred launch (#865). Unlike handleUploadFile, which
// writes straight into the live pod, the bytes land in Postgres keyed by the
// launch turn id + ordinal, so the launch survives a browser that goes away
// before the pod is ready; the dispatch reconciler materializes them into the
// workspace when it dispatches. Targets the launch by `turn_id` (returned by
// the create boundary) since one session can hold at most one pending launch
// but the row is keyed by turn.
func (s *appServer) handleStageLaunchAttachment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	ordinal, err := strconv.Atoi(strings.TrimSpace(r.PathValue("ordinal")))
	if err != nil || ordinal < 0 || ordinal >= maxLaunchAttachments {
		writeError(w, http.StatusBadRequest, "ordinal must be an integer in [0, 32)")
		return
	}
	// Keyed by client_nonce (what the browser holds after create); the turn id
	// is derived server-side the same way the create boundary and the runners
	// do, so the frontend never has to replicate the (hashing) derivation.
	clientNonce := strings.TrimSpace(r.URL.Query().Get("client_nonce"))
	if clientNonce == "" || !turnIDPattern.MatchString(clientNonce) {
		writeError(w, http.StatusBadRequest, "client_nonce is required and must match turn id syntax")
		return
	}
	turnID := conversation.TurnIDForClientNonce(clientNonce)
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name")
		return
	}
	if s.pendingLaunch == nil {
		writeError(w, http.StatusServiceUnavailable, "launch attachment staging unavailable")
		return
	}
	owner := user.OwnerEmail()
	// Ownership gate. Staging does not require the pod (bytes go to Postgres),
	// so GetByOwner — not resolveSessionPod — is the right check: the pod may
	// still be coming up while the browser uploads.
	if _, err := s.mgr.GetByOwner(r.Context(), owner, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	launch, err := s.pendingLaunch.Get(r.Context(), storageKey, turnID)
	if errors.Is(err, pgstore.ErrPendingLaunchNotFound) {
		writeError(w, http.StatusNotFound, "no pending launch for that turn")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup pending launch: "+err.Error())
		return
	}
	// Reject ordinals beyond the declared attachment count so a stray upload
	// can't push the staged-row count to the ready threshold with wrong slots.
	if ordinal >= launch.AttachmentCount {
		writeError(w, http.StatusBadRequest, "ordinal exceeds the launch attachment count")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRawBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status, err := s.pendingLaunch.StageAttachment(r.Context(), storageKey, turnID, pgstore.LaunchAttachmentBlob{
		Ordinal:     ordinal,
		Name:        name,
		ContentType: r.Header.Get("Content-Type"),
		Size:        int64(len(data)),
		Bytes:       data,
	})
	switch {
	case errors.Is(err, pgstore.ErrPendingLaunchNotFound):
		writeError(w, http.StatusNotFound, "no pending launch for that turn")
		return
	case errors.Is(err, pgstore.ErrPendingLaunchNotAcceptingBytes):
		writeError(w, http.StatusConflict, "launch is no longer accepting attachments")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "stage attachment: "+err.Error())
		return
	}
	recordLaunchAttachmentStaged(string(status))
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  string(status),
		"ordinal": ordinal,
		"size":    len(data),
		"turn_id": turnID,
	})
}

// allocateScreenshotPath runs the in-pod atomic id-allocator, writes the
// bytes, and returns the absolute path the file landed at. The path
// shape is `/workspace/screenshots/<n>.<ext>` where `<n>` is the
// smallest positive integer not already in use; the script picks it via
// O_EXCL so concurrent uploads can't collide.
func allocateScreenshotPath(ctx context.Context, s *appServer, podName, contentType, rawName string, data []byte) (string, error) {
	rootAbs, err := safeWorkspacePath(screenshotsRelDir)
	if err != nil {
		return "", err
	}
	ext := screenshotExtension(contentType, rawName)
	cmd := []string{
		"python3", "-c", screenshotAllocatorScript,
		rootAbs, ext, fmt.Sprintf("%d", len(data)),
	}
	out, err := kubeexec.CaptureWithStdin(ctx, s.k8s, s.restCfg, s.namespace, podName, cmd, data)
	if err != nil {
		return "", err
	}
	var body struct {
		AbsPath string `json:"abs_path"`
		ID      int    `json:"id"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", fmt.Errorf("parse allocator output: %w", err)
	}
	if body.AbsPath == "" {
		return "", fmt.Errorf("allocator returned empty path")
	}
	// Defense-in-depth: re-validate that the allocator's chosen path
	// is still inside the workspace. The script is server-controlled
	// and its inputs are sanitized, but a fresh `safeWorkspacePath`
	// keeps the contract explicit at the response boundary.
	return safeWorkspacePath(body.AbsPath)
}

// handleWriteFile writes text content to a file.
func (s *appServer) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	owner := user.OwnerEmail()
	_, podName, herr := s.resolveSessionPod(r.Context(), owner, sessionID)
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
	if _, err := s.resolveInPodWorkspacePath(r.Context(), podName, absPath); err != nil {
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

def mcp_post(url, payload, session_id=None):
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if session_id:
        headers["Mcp-Session-Id"] = session_id
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers=headers,
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=4) as resp:
        return sse_json(resp.read()), resp.headers.get("Mcp-Session-Id")

def mcp_list_tools(url):
    init_msg, session_id = mcp_post(url, {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize",
        "params": {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "tank-operator-capability-probe", "version": "1.0"},
        },
    })
    if init_msg.get("error"):
        raise RuntimeError(json.dumps(init_msg["error"]))
    if session_id:
        mcp_post(url, {
            "jsonrpc": "2.0",
            "method": "notifications/initialized",
            "params": {},
        }, session_id)
    tools_msg, _ = mcp_post(url, {
        "jsonrpc": "2.0",
        "id": 2,
        "method": "tools/list",
        "params": {},
    }, session_id)
    return tools_msg

entries = []
errors = []
for server, raw in sorted((config.get("mcpServers") or {}).items()):
    if not isinstance(raw, dict):
        continue
    url = str(raw.get("url") or "").strip()
    if not url:
        continue
    try:
        msg = mcp_list_tools(url)
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

func (s *appServer) resolveInPodWorkspacePath(ctx context.Context, podName, absPath string) (string, error) {
	out, err := kubeexec.Capture(ctx, s.k8s, s.restCfg, s.namespace, podName, []string{
		"python3",
		"-c",
		workspacePathBoundaryCheckScript,
		workspaceRoot,
		absPath,
	})
	if err != nil {
		return "", fmt.Errorf("validate workspace path: %w", err)
	}
	var body struct {
		OK           bool   `json:"ok"`
		Error        string `json:"error"`
		ResolvedPath string `json:"resolved_path"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", fmt.Errorf("parse workspace path validation: %w", err)
	}
	if !body.OK {
		if strings.TrimSpace(body.Error) != "" {
			return "", fmt.Errorf("%s: %s", body.Error, absPath)
		}
		return "", fmt.Errorf("path escapes workspace: %s", absPath)
	}
	if body.ResolvedPath == "" {
		return "", fmt.Errorf("workspace path validation returned empty path")
	}
	return body.ResolvedPath, nil
}

func workspaceRelPath(absPath string) string {
	rel := strings.TrimPrefix(absPath, workspaceRoot)
	rel = strings.TrimPrefix(rel, "/")
	return rel
}
