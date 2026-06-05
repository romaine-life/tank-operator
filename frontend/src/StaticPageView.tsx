import { useEffect, useState } from "react";

import { authedFetch } from "./auth";

/**
 * StaticPageView renders an agent-authored HTML file from the session
 * workspace as a live page — the common case is an LLM-generated diagram
 * (inline HTML/CSS, sometimes a CDN charting lib).
 *
 * SECURITY MODEL — read before touching the iframe:
 * The page's bytes are untrusted (an agent wrote them; prompt-injection or
 * plain carelessness both apply). We stay on the main `tank.romaine.life`
 * origin, where the auth.romaine.life JWT lives in localStorage, so the only
 * thing that makes this safe is the iframe `sandbox` WITHOUT
 * `allow-same-origin`: the document runs in an opaque origin and cannot read
 * this app's localStorage/cookies or script the surrounding page. The Tank
 * chrome around the frame lives OUTSIDE the sandbox, so the rendered page
 * cannot spoof or escape it. Never add `allow-same-origin`.
 */

type LoadState =
  | { status: "loading" }
  | { status: "ready"; html: string }
  | { status: "error"; message: string };

export default function StaticPageView({
  sessionId,
  path,
  onClose,
}: {
  sessionId: string;
  path: string;
  onClose: () => void;
}) {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [chromeCollapsed, setChromeCollapsed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    // Fetch the full bytes through the authenticated raw endpoint (8 MiB cap),
    // not the text/content endpoint which truncates at 256 KiB. The bytes
    // never touch a top-level navigation, so the bearer header rides along on
    // this fetch exactly like every other file read.
    void authedFetch(
      `/api/sessions/${encodeURIComponent(sessionId)}/files/raw?path=${encodeURIComponent(path)}`,
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return res.text();
      })
      .then((html) => {
        if (!cancelled) setState({ status: "ready", html });
      })
      .catch((err) => {
        if (!cancelled) {
          setState({ status: "error", message: String((err as Error)?.message ?? err) });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId, path]);

  const fileName = path.split("/").pop() || path;

  return (
    <div className="run-static-page" aria-label="Rendered page">
      {chromeCollapsed ? (
        <button
          type="button"
          className="run-static-page-restore"
          title="Show page details"
          onClick={() => setChromeCollapsed(false)}
        >
          ▾ Tank
        </button>
      ) : (
        <div className="run-static-page-bar">
          <div className="run-static-page-bar-left">
            <span className="run-static-page-badge">Tank</span>
            <span className="run-static-page-title" title={path}>
              {fileName}
            </span>
            <span className="run-static-page-sub">
              Rendered from session {sessionId} · sandboxed preview
            </span>
          </div>
          <div className="run-static-page-bar-actions">
            <button
              type="button"
              className="run-static-page-btn"
              title="Collapse this bar"
              onClick={() => setChromeCollapsed(true)}
            >
              Collapse ▴
            </button>
            <button type="button" className="run-static-page-btn" onClick={onClose}>
              ✕ Back to files
            </button>
          </div>
        </div>
      )}

      <div className="run-static-page-body">
        {state.status === "loading" ? (
          <div className="run-files-status">
            <span>Loading page…</span>
          </div>
        ) : state.status === "error" ? (
          <div className="run-files-status run-files-error">
            <span>Couldn’t render this page: {state.message}</span>
          </div>
        ) : (
          <iframe
            className="run-static-page-frame"
            title={fileName}
            // SECURITY: no `allow-same-origin` — opaque origin, no token access.
            sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"
            srcDoc={state.html}
          />
        )}
      </div>
    </div>
  );
}
