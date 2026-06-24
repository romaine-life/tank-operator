import { hasInternalAuthConfig, internalBearerToken } from "./internalAuth.js";

// uploadTranscriptSnapshot ships one whole-file JSONL snapshot to the
// orchestrator-internal transcript-snapshot endpoint. It mirrors
// reportRuntimeConfig's auth shape (read the pod's internal auth credential
// from disk per call; the orchestrator validates it against the live pod
// identity). The file bytes ride as the raw request body so a multi-MB
// transcript is not base64-inflated; restore metadata rides as ASCII headers.
//
// Return contract:
//   true  -> stored durably
//   false -> not configured / not eligible (caller counts this as "skipped",
//            NOT "error", and must not advance its dedup cursor so a later
//            retry can succeed once storage is configured)
// Throws on a real transport/HTTP failure so the caller counts an error.
function trimTrailingSlashes(value) {
  return String(value || "").replace(/\/+$/, "");
}

export async function uploadTranscriptSnapshot(cfg, snap) {
  const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
  if (!baseURL || !hasInternalAuthConfig(cfg) || !cfg.sessionId) {
    return false;
  }
  if (!snap || !snap.bytes || !snap.sdkSessionId) {
    return false;
  }
  const token = await internalBearerToken(cfg);
  const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/transcript-snapshot`;
  const response = await fetch(url, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/x-ndjson",
      // encodeURIComponent keeps these header values ASCII-safe; the server
      // decodes the rel-path. The rel-path is stored verbatim so restore
      // (Stage 2) materializes the file at the exact SDK-expected location.
      "X-Tank-Transcript-Sdk-Session-Id": encodeURIComponent(String(snap.sdkSessionId)),
      "X-Tank-Transcript-Rel-Path": encodeURIComponent(String(snap.relPath || "")),
      "X-Tank-Transcript-Sdk-Version": encodeURIComponent(String(snap.sdkVersion || "")),
      "X-Tank-Transcript-Mtime-Ms": String(Math.max(0, Math.floor(Number(snap.mtimeMs) || 0))),
    },
    body: snap.bytes,
  });
  // 503 = orchestrator has no transcript storage configured yet (first-install
  // ordering, same degraded-stub shape as the other stores). Treat as skip.
  if (response.status === 503) {
    return false;
  }
  if (!response.ok) {
    throw new Error(`transcript snapshot upload failed: ${response.status}`);
  }
  return true;
}
