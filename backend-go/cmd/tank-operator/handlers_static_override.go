package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// Static-override receiver — the data-plane half of the live frontend preview
// feature. A session pod builds its frontend and PUTs the gzipped tar of the
// built dist/ here; the orchestrator (running in test-env mode on a slot)
// extracts it and atomically flips the served override into place. The next
// request to the slot serves the streamed bundle, with zero kubectl/exec and
// zero new k8s RBAC — the only gate is the auth.romaine.life service principal.
//
// This is deliberately NOT the retired test-slot artifact/hot-swap path (which
// was deleted end-to-end and is guarded by
// scripts/check-session-pod-hot-swap-migration.mjs). It is a dev-only,
// test-env-gated, ephemeral "for seeing" lane that writes to the slot's
// static-override emptyDir; it is never merge evidence and never touches the
// fingerprinted image-deploy promotion path. See the live-preview capability
// ledger entry for the fidelity boundary.
//
// Layout under the receiver root ($TANK_OPERATOR_STATIC_OVERRIDE_ROOT, the
// static-override emptyDir mountPath):
//
//	<root>/releases/rel-<ts>-<rand>/   extracted bundles (newest kept, rest pruned)
//	<root>/current                     symlink → releases/rel-...  (atomically flipped)
//
// The static file server reads through <root>/current
// ($TANK_OPERATOR_STATIC_OVERRIDE_DIR, set by the chart) on every request, so a
// symlink rename is a zero-window atomic swap: a request never observes a
// half-written bundle, and DELETE simply removes the symlink to revert to the
// image-baked assets.

const (
	// maxStaticOverrideBytes bounds the compressed request body. A production
	// tank-operator dist/ is a few MB gzipped; 64 MiB is generous headroom
	// while capping a runaway or hostile upload. Enforced via
	// http.MaxBytesReader so the read fails fast without buffering the body.
	maxStaticOverrideBytes int64 = 64 << 20

	// maxStaticOverrideUncompressedBytes bounds the total extracted size so a
	// decompression bomb cannot fill the slot's emptyDir.
	maxStaticOverrideUncompressedBytes int64 = 256 << 20

	// maxStaticOverrideEntries bounds the number of archive members.
	maxStaticOverrideEntries = 20000

	// staticOverrideKeepReleases is how many extracted bundles to retain (the
	// live one plus a little history for cheap rollback); older ones are pruned
	// after each successful flip so the emptyDir does not grow without bound.
	staticOverrideKeepReleases = 3

	staticOverrideReleasesDir = "releases"
	staticOverrideCurrentName = "current"
	staticOverrideReleaseGlob = "rel-"
)

// errStaticOverrideNoIndex rejects an archive with no index.html at its root:
// such a bundle has no SPA entrypoint, so flipping `current` to it would serve
// a dead frontend. Failing the push leaves the prior good bundle live.
var errStaticOverrideNoIndex = errors.New("archive missing index.html at root")

// staticOverrideRoot returns the receiver-managed root, or "" when the receiver
// is not enabled on this deployment. It is non-empty only on a test-env render
// (the chart gates the env on tank-operator.isTestEnv + staticOverride.enabled),
// so the push surface physically cannot exist in production.
func staticOverrideRoot() string {
	return strings.TrimSpace(os.Getenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT"))
}

// handleInternalPutStaticOverride receives a gzipped tar of a built frontend
// dist/ and atomically activates it as the slot's served static override.
func (s *appServer) handleInternalPutStaticOverride(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "PUT /api/internal/static-override")
	if user == nil {
		return
	}
	root := staticOverrideRoot()
	if root == "" {
		recordStaticOverridePush("disabled")
		writeError(w, http.StatusForbidden, "static override receiver is not enabled on this deployment")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStaticOverrideBytes)

	releasesDir := filepath.Join(root, staticOverrideReleasesDir)
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		recordStaticOverridePush("error")
		writeError(w, http.StatusInternalServerError, "prepare releases dir: "+err.Error())
		return
	}
	relDir, err := os.MkdirTemp(releasesDir, fmt.Sprintf("%s%020d-*", staticOverrideReleaseGlob, time.Now().UnixNano()))
	if err != nil {
		recordStaticOverridePush("error")
		writeError(w, http.StatusInternalServerError, "create release dir: "+err.Error())
		return
	}

	files, nbytes, err := extractStaticOverrideTar(r.Body, relDir)
	if err != nil {
		_ = os.RemoveAll(relDir)
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			recordStaticOverridePush("too_large")
			writeError(w, http.StatusRequestEntityTooLarge, "archive exceeds size limit")
		case errors.Is(err, errStaticOverrideNoIndex):
			recordStaticOverridePush("bad_archive")
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			recordStaticOverridePush("bad_archive")
			writeError(w, http.StatusBadRequest, "invalid archive: "+err.Error())
		}
		return
	}

	relTarget := filepath.Join(staticOverrideReleasesDir, filepath.Base(relDir))
	if err := flipStaticOverrideCurrent(root, relTarget); err != nil {
		_ = os.RemoveAll(relDir)
		recordStaticOverridePush("error")
		writeError(w, http.StatusInternalServerError, "activate release: "+err.Error())
		return
	}
	pruneStaticOverrideReleases(root, staticOverrideKeepReleases)

	recordStaticOverridePush("ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"release": filepath.Base(relDir),
		"files":   files,
		"bytes":   nbytes,
		"by":      user.ActorEmail,
	})
}

// handleInternalDeleteStaticOverride reverts the slot to its image-baked assets
// by removing the `current` symlink and dropping the extracted releases.
func (s *appServer) handleInternalDeleteStaticOverride(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "DELETE /api/internal/static-override")
	if user == nil {
		return
	}
	root := staticOverrideRoot()
	if root == "" {
		recordStaticOverridePush("disabled")
		writeError(w, http.StatusForbidden, "static override receiver is not enabled on this deployment")
		return
	}
	currentPath := filepath.Join(root, staticOverrideCurrentName)
	if err := os.Remove(currentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		recordStaticOverridePush("error")
		writeError(w, http.StatusInternalServerError, "clear override: "+err.Error())
		return
	}
	// Drop the extracted bundles too: a reverted slot serves the baked image,
	// so retaining releases would only leak emptyDir space.
	_ = os.RemoveAll(filepath.Join(root, staticOverrideReleasesDir))
	recordStaticOverridePush("reverted")
	writeJSON(w, http.StatusOK, map[string]any{"status": "reverted"})
}

// handleSetLivePreviewEnabled is the owner's live-preview toggle behind the
// test-slot page's "Start frontend testing" control. It records the durable
// intent on test_state.live_preview.enabled; the in-pod live-preview daemon
// converges on it over the session SSE — turning its build+push loop on, and on
// disable stopping and DELETEing the slot override so the slot reverts to its
// image-baked baseline. Owner-scoped (requireAuth + GetRegisteredByOwner).
// Enabling requires an active slot with a URL: live preview streams scratch on
// top of a running slot, so without one there is nothing to preview against.
func (s *appServer) handleSetLivePreviewEnabled(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	owner := user.OwnerEmail()
	info, err := s.mgr.GetRegisteredByOwner(r.Context(), owner, sessionID)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrNotFound), errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusNotFound, "session not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Enabled {
		if !boolFromState(info.TestState, "active") || stringFromState(info.TestState, "url") == "" {
			writeError(w, http.StatusBadRequest, "no active test slot to preview against")
			return
		}
	}
	updated, err := s.mgr.UpdateLivePreviewState(r.Context(), owner, sessionID, sessions.LivePreviewPatch{Enabled: &body.Enabled})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleInternalReportLivePreviewPush records a live-preview push receipt on the
// session's test_state.live_preview (pushed_at + pushed_build) so the test-slot
// page can show "streaming · last pushed Ns ago" and surface a stalled daemon.
// The in-pod live-preview daemon calls this after each successful PUT to a
// slot's static-override receiver. It never changes the `enabled` flag — that is
// the owner's toggle. Authorized by the caller's verified per-session service
// subject (a pod may only report its own session), the #1207 invariant.
func (s *appServer) handleInternalReportLivePreviewPush(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/live-preview/push")
	if user == nil {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !s.internalCallerMatchesSession(user, sessionID) {
		writeError(w, http.StatusForbidden, "live-preview push receipts require a session pod writing its own session")
		return
	}
	var body struct {
		Build string `json:"build"`
	}
	// Body is optional (a bare receipt is valid); ignore decode errors.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)

	info, err := s.mgr.GetByID(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	now := time.Now()
	build := strings.TrimSpace(body.Build)
	updated, err := s.mgr.UpdateLivePreviewState(r.Context(), info.Owner, sessionID, sessions.LivePreviewPatch{
		PushedAt:    &now,
		PushedBuild: &build,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "test_state": updated.TestState})
}

// extractStaticOverrideTar streams a gzipped tar into dest with full
// containment, entry-count, and uncompressed-size guards. It returns the number
// of regular files written and their total byte count. It rejects any archive
// member that escapes dest, any symlink/hardlink/device member (never extract a
// link from an untrusted archive into the served tree), and any archive with no
// root index.html.
func extractStaticOverrideTar(r io.Reader, dest string) (int, int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return 0, 0, err
	}
	sep := string(filepath.Separator)

	tr := tar.NewReader(gz)
	var files int
	var total int64
	var sawIndex bool
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, total, fmt.Errorf("tar: %w", err)
		}
		if files >= maxStaticOverrideEntries {
			return files, total, fmt.Errorf("archive exceeds %d entries", maxStaticOverrideEntries)
		}

		name := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+sep) {
			return files, total, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(destAbs, name)
		if rel, rerr := filepath.Rel(destAbs, target); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			return files, total, fmt.Errorf("path escapes destination: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, total, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, total, err
			}
			remaining := maxStaticOverrideUncompressedBytes - total
			if remaining <= 0 {
				return files, total, fmt.Errorf("archive exceeds %d uncompressed bytes", maxStaticOverrideUncompressedBytes)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return files, total, err
			}
			// Copy at most remaining+1 so a member that would push us over the
			// budget is detected (n > remaining) without trusting hdr.Size.
			n, copyErr := io.CopyN(f, tr, remaining+1)
			closeErr := f.Close()
			total += n
			if copyErr != nil && !errors.Is(copyErr, io.EOF) {
				return files, total, copyErr
			}
			if n > remaining {
				return files, total, fmt.Errorf("archive exceeds %d uncompressed bytes", maxStaticOverrideUncompressedBytes)
			}
			if closeErr != nil {
				return files, total, closeErr
			}
			files++
			if name == "index.html" {
				sawIndex = true
			}
		default:
			return files, total, fmt.Errorf("unsupported tar entry type %q for %q", string(rune(hdr.Typeflag)), hdr.Name)
		}
	}
	if !sawIndex {
		return files, total, errStaticOverrideNoIndex
	}
	return files, total, nil
}

// flipStaticOverrideCurrent atomically points <root>/current at relTarget (a
// path relative to root, e.g. "releases/rel-..."). It writes a temp symlink and
// renames it over the existing `current`; rename(2) over a symlink is atomic,
// so a concurrent request resolves either the old or the new bundle, never a
// partial state.
func flipStaticOverrideCurrent(root, relTarget string) error {
	currentPath := filepath.Join(root, staticOverrideCurrentName)
	tmpLink := filepath.Join(root, fmt.Sprintf(".current.tmp.%d", time.Now().UnixNano()))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(relTarget, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, currentPath); err != nil {
		_ = os.Remove(tmpLink)
		return err
	}
	return nil
}

// pruneStaticOverrideReleases removes all but the newest `keep` extracted
// bundles. Release dir names are time-prefixed, so a lexical sort is
// chronological. The bundle `current` points at is never pruned, even if it
// somehow falls outside the keep window.
func pruneStaticOverrideReleases(root string, keep int) {
	releasesDir := filepath.Join(root, staticOverrideReleasesDir)
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), staticOverrideReleaseGlob) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)

	var liveTarget string
	if dst, err := os.Readlink(filepath.Join(root, staticOverrideCurrentName)); err == nil {
		liveTarget = filepath.Base(dst)
	}
	for _, name := range names[:len(names)-keep] {
		if name == liveTarget {
			continue
		}
		_ = os.RemoveAll(filepath.Join(releasesDir, name))
	}
}
