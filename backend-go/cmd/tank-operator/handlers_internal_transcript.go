package main

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/transcriptstore"
)

// maxTranscriptSnapshotBytes caps a single transcript upload. Transcripts are
// typically single-digit MB; tens of MB for monster sessions. 256 MiB is a
// generous backstop against a runaway/garbage body, not an expected size.
const maxTranscriptSnapshotBytes = 256 << 20

// sdkSessionIDPattern guards the blob key path segment derived from the
// caller-supplied SDK session id. The SDK names transcripts <uuid>.jsonl; this
// rejects path traversal / separators while allowing the uuid charset.
var sdkSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// handleInternalSessionTranscriptSnapshot receives a whole-file JSONL snapshot
// from a session pod's claude-runner and stores it durably. Capture is
// additive and best-effort: when storage is not configured the endpoint
// answers 503 so the runner counts a skip and retries, never an error.
//
// Auth mirrors the runtime-config endpoint: requireInternalSessionPodCaller
// validates the projected SA token via TokenReview + live pod lookup and binds
// the caller to its own session id.
func (s *appServer) handleInternalSessionTranscriptSnapshot(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		recordTranscriptUpload("bad_request")
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if sessionID != caller.SessionID {
		recordTranscriptUpload("forbidden")
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.transcripts == nil {
		recordTranscriptUpload("not_configured")
		writeError(w, http.StatusServiceUnavailable, "transcript storage not configured")
		return
	}

	sdkSessionID := decodeHeader(r.Header.Get("X-Tank-Transcript-Sdk-Session-Id"))
	if !sdkSessionIDPattern.MatchString(sdkSessionID) {
		recordTranscriptUpload("bad_request")
		writeError(w, http.StatusBadRequest, "invalid sdk session id")
		return
	}
	relPath := decodeHeader(r.Header.Get("X-Tank-Transcript-Rel-Path"))
	sdkVersion := decodeHeader(r.Header.Get("X-Tank-Transcript-Sdk-Version"))
	mtimeMs := strings.TrimSpace(r.Header.Get("X-Tank-Transcript-Mtime-Ms"))

	r.Body = http.MaxBytesReader(w, r.Body, maxTranscriptSnapshotBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		recordTranscriptUpload("read_error")
		writeError(w, http.StatusBadRequest, "failed to read transcript body")
		return
	}
	if len(data) == 0 {
		recordTranscriptUpload("bad_request")
		writeError(w, http.StatusBadRequest, "empty transcript body")
		return
	}

	snap := transcriptstore.Snapshot{
		Bytes:       data,
		ContentType: "application/x-ndjson",
		Metadata: sanitizeBlobMetadata(map[string]string{
			"tank_session_id": sessionID,
			"sdk_session_id":  sdkSessionID,
			"rel_path":        relPath,
			"sdk_version":     sdkVersion,
			"mtime_ms":        mtimeMs,
			"owner_email":     caller.Email,
		}),
	}
	key := transcriptBlobKey(caller.Email, sessionID, sdkSessionID)
	if err := s.transcripts.Put(r.Context(), key, snap); err != nil {
		recordTranscriptUpload("error")
		writeError(w, http.StatusInternalServerError, "failed to store transcript snapshot")
		return
	}
	recordTranscriptUpload("ok")
	w.WriteHeader(http.StatusNoContent)
}

// handleInternalSessionResumeTranscript streams a dead session's captured
// transcript back to its resurrected pod's runner. The runner (a freshly
// created, authenticated session pod) sends the dead source session id in a
// header; the orchestrator re-verifies the source is owned by the same caller
// before returning anything. Restore metadata (sdk session id, rel-path,
// version) rides as headers so the runner materializes the file at the exact
// SDK-expected path and gates resume on version.
func (s *appServer) handleInternalSessionResumeTranscript(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID != caller.SessionID {
		writeError(w, http.StatusForbidden, "session target does not match caller pod")
		return
	}
	if s.transcripts == nil {
		writeError(w, http.StatusServiceUnavailable, "transcript storage not configured")
		return
	}
	if s.mgr == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager unavailable")
		return
	}
	sourceID := strings.TrimSpace(r.Header.Get("X-Tank-Resurrect-Source-Session-Id"))
	if sourceID == "" {
		writeError(w, http.StatusBadRequest, "missing resurrect source session id")
		return
	}
	// Ownership is authoritative here: the pod supplied the source id from its
	// env, but the orchestrator confirms the caller owns that source session
	// (any visibility — a resurrected source is typically soft-deleted/dead).
	if _, _, err := s.mgr.GetRegisteredByOwnerAnyVisibility(r.Context(), caller.Email, sourceID); err != nil {
		writeError(w, http.StatusNotFound, "source session not found")
		return
	}
	snap, found, err := s.transcripts.Latest(r.Context(), transcriptBlobPrefix(caller.Email, sourceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read transcript")
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	setMetaHeader := func(header, key string) {
		if v := snap.Metadata[key]; v != "" {
			w.Header().Set(header, url.QueryEscape(v))
		}
	}
	setMetaHeader("X-Tank-Transcript-Sdk-Session-Id", "sdk_session_id")
	setMetaHeader("X-Tank-Transcript-Rel-Path", "rel_path")
	setMetaHeader("X-Tank-Transcript-Sdk-Version", "sdk_version")
	contentType := snap.ContentType
	if contentType == "" {
		contentType = "application/x-ndjson"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snap.Bytes)
}

// transcriptBlobPrefix is the blob key namespace for one session's snapshots:
// transcriptBlobKey joins prefix + "<sdkSessionId>.jsonl".
func transcriptBlobPrefix(email, sessionID string) string {
	return blobSegment(email) + "/" + blobSegment(sessionID) + "/"
}

func decodeHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if dec, err := url.QueryUnescape(raw); err == nil {
		return dec
	}
	return raw
}

// transcriptBlobKey namespaces snapshots by owner and session. The SDK session
// id is a UUID (already unique); the owner/session prefix groups a user's
// sessions for listing and retention.
func transcriptBlobKey(email, sessionID, sdkSessionID string) string {
	return path.Join(blobSegment(email), blobSegment(sessionID), sdkSessionID+".jsonl")
}

// blobSegment lowercases and reduces a value to a safe single path segment.
func blobSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "unknown"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// sanitizeBlobMetadata keeps Azure blob metadata values ASCII-printable and
// bounded. Empty values are dropped. Keys here are already valid identifiers.
func sanitizeBlobMetadata(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		var b strings.Builder
		for _, r := range v {
			if r >= 0x20 && r < 0x7f {
				b.WriteRune(r)
			}
		}
		clean := b.String()
		if len(clean) > 1024 {
			clean = clean[:1024]
		}
		if clean != "" {
			out[k] = clean
		}
	}
	return out
}
