package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/kubeexec"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// staticPageTTL is how long a captured page snapshot stays renderable after the
// most recent open. Opening a page recaptures and resets this window. Matches
// the product decision: these are ephemeral "glance / share now" artifacts.
const staticPageTTL = 12 * time.Hour

// isHTMLPath reports whether path ends in .html/.htm (case-insensitive).
func isHTMLPath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm")
}

type staticPageResponse struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	ByteSize    int    `json:"byte_size"`
	ExpiresAt   string `json:"expires_at"`
	CreatedAt   string `json:"created_at"`
	Text        string `json:"text"`
}

// handleCaptureStaticPage reads the current bytes of an HTML workspace file from
// the live session pod, stores them as a durable 12h snapshot (recapture on each
// open), and returns the bytes inline so the first render needs no second round
// trip. Requires the pod to be alive; handleGetStaticPage serves the snapshot
// after the pod is gone, which is the whole point of capturing.
func (s *appServer) handleCaptureStaticPage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.staticPages == nil {
		recordStaticPage("capture", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "static page store not configured")
		return
	}
	sessionID := r.PathValue("session_id")
	info, podName, herr := s.resolveSessionPodForRead(r.Context(), user, sessionID)
	if herr != nil {
		recordStaticPage("capture", staticPageResolveResult(herr.status))
		writeError(w, herr.status, herr.msg)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		recordStaticPage("capture", "bad_request")
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	if !isHTMLPath(filePath) {
		recordStaticPage("capture", "bad_request")
		writeError(w, http.StatusBadRequest, "only .html/.htm files can be rendered as a page")
		return
	}
	absPath, err := safeWorkspacePath(filePath)
	if err != nil {
		recordStaticPage("capture", "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"head", "-c", fmt.Sprintf("%d", maxRawBytes), "--", absPath})
	if err != nil {
		recordStaticPage("capture", "exec_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	relPath := workspaceRelPath(absPath)
	now := time.Now()
	expiresAt := now.Add(staticPageTTL)
	if err := s.staticPages.Upsert(r.Context(), pgstore.StaticPageSnapshot{
		SessionScope: s.sessionScope,
		SessionID:    sessionID,
		RelPath:      relPath,
		OwnerEmail:   info.Owner,
		ContentType:  "text/html; charset=utf-8",
		Bytes:        out,
		ByteSize:     len(out),
		ExpiresAt:    expiresAt,
	}); err != nil {
		recordStaticPage("capture", "store_error")
		writeError(w, http.StatusInternalServerError, "store snapshot: "+err.Error())
		return
	}
	recordStaticPage("capture", "ok")
	writeJSON(w, http.StatusOK, staticPageResponse{
		Path:        relPath,
		ContentType: "text/html; charset=utf-8",
		ByteSize:    len(out),
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
		CreatedAt:   now.UTC().Format(time.RFC3339),
		Text:        string(out),
	})
}

// handleGetStaticPage serves a previously captured snapshot. It deliberately
// does NOT require the session pod — the durable snapshot outlives the pod for
// its TTL. Ownership is still enforced via the session read gate.
func (s *appServer) handleGetStaticPage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.staticPages == nil {
		recordStaticPage("read", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "static page store not configured")
		return
	}
	sessionID := r.PathValue("session_id")
	_, status, err := s.authorizeSessionRead(r.Context(), user, sessionID)
	if err != nil {
		recordStaticPage("read", staticPageResolveResult(status))
		writeError(w, status, err.Error())
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		recordStaticPage("read", "bad_request")
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	absPath, err := safeWorkspacePath(filePath)
	if err != nil {
		recordStaticPage("read", "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	relPath := workspaceRelPath(absPath)
	snap, err := s.staticPages.Get(r.Context(), s.sessionScope, sessionID, relPath)
	if errors.Is(err, pgstore.ErrStaticPageSnapshotNotFound) {
		recordStaticPage("read", "not_found")
		writeError(w, http.StatusNotFound, "no live snapshot for this page — open it from the session, or it may have expired")
		return
	}
	if err != nil {
		recordStaticPage("read", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordStaticPage("read", "ok")
	writeJSON(w, http.StatusOK, staticPageResponse{
		Path:        relPath,
		ContentType: snap.ContentType,
		ByteSize:    snap.ByteSize,
		ExpiresAt:   snap.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt:   snap.CreatedAt.UTC().Format(time.RFC3339),
		Text:        string(snap.Bytes),
	})
}

func staticPageResolveResult(status int) string {
	switch status {
	case http.StatusNotFound:
		return "not_found"
	case http.StatusForbidden:
		return "denied"
	case http.StatusServiceUnavailable:
		return "pod_unavailable"
	default:
		return "store_error"
	}
}
