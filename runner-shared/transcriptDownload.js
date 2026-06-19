import { readFile } from "node:fs/promises";

// fetchResumeTranscript asks the orchestrator for THIS (resurrected) session's
// resume transcript. The orchestrator resolves the dead source session from
// `resurrected_from`, authorizes by owner, and streams the captured JSONL back
// with restore metadata in headers. Returns null when there is nothing to
// resume (404) so the runner falls back to a normal fresh start.
function trimTrailingSlashes(value) {
  return String(value || "").replace(/\/+$/, "");
}

function decodeHeader(res, name) {
  const raw = res.headers.get(name) || "";
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

export async function fetchResumeTranscript(cfg, sourceSessionId) {
  const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
  const tokenPath = cfg.operatorTokenPath || "";
  const source = String(sourceSessionId || "").trim();
  if (!baseURL || !tokenPath || !cfg.sessionId || !source) {
    return null;
  }
  const token = (await readFile(tokenPath, "utf8")).trim();
  const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/resume-transcript`;
  const res = await fetch(url, {
    method: "GET",
    headers: {
      Authorization: `Bearer ${token}`,
      "X-Tank-Resurrect-Source-Session-Id": source,
    },
  });
  if (res.status === 404) {
    return null;
  }
  if (!res.ok) {
    throw new Error(`resume transcript fetch failed: ${res.status}`);
  }
  const bytes = Buffer.from(await res.arrayBuffer());
  return {
    sdkSessionId: decodeHeader(res, "X-Tank-Transcript-Sdk-Session-Id"),
    relPath: decodeHeader(res, "X-Tank-Transcript-Rel-Path"),
    sdkVersion: decodeHeader(res, "X-Tank-Transcript-Sdk-Version"),
    bytes,
  };
}
