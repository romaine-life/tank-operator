// Sidebar session-list client-side telemetry. Two surfaces:
//
//  1. Debug-gated console transcript. Enable per browser by setting
//     `localStorage.tankDebug = "session-list"` (comma-separated tokens
//     are accepted; this checks for "session-list" specifically). Once
//     enabled, every snapshot fetch, every applied SSE event, and every
//     cursor advance prints to the console under `[tank/session-list]`.
//     Off by default — zero perf cost when not enabled.
//
//  2. Always-on placeholder-synthesis beacon. The reducer's
//     applyPodStatusEvent branch that synthesizes a Session row for an
//     unknown id is supposed to fire only in a narrow live-event race
//     window. Post-tank-operator#525 the cold-open replay-from-zero is
//     gone and the Reader.List pod-fallback excludes invisible rows, so
//     the synthesis branch should be effectively cold in production.
//     Any fire posts to /api/debug/client-metric which increments
//     `tank_session_list_client_placeholder_synthesized_total` — a
//     non-zero rate in steady state is the smoking-gun signal that a
//     resurrection path snuck back in.
//
// Both helpers are best-effort: they swallow localStorage/fetch errors
// so a misconfigured browser or transient network glitch can never
// affect the reducer's correctness path.

import type { SessionListEvent } from "./sessionListEvents";

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "session-list";
const CONSOLE_PREFIX = "[tank/session-list]";

function isDebugEnabled(): boolean {
  try {
    const raw = localStorage.getItem(DEBUG_STORAGE_KEY) ?? "";
    return raw
      .split(",")
      .map((s) => s.trim())
      .includes(DEBUG_TOKEN);
  } catch {
    // Private mode or storage-disabled browsers — treat as off.
    return false;
  }
}

export function logSessionListSnapshot(args: {
  tip: string | null;
  sessionCount: number;
  source: "initial" | "resync";
}): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} snapshot`, args);
}

export function logSessionListSseOpen(cursor: string | null): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} sse open`, { cursor });
}

export function logSessionListEvent(event: SessionListEvent): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} event`, {
    type: event.type,
    session_id: event.session_id,
    scope: event.session_scope,
    order_key: event.order_key,
  });
}

export function logSessionListStreamSignal(args: {
  signal: "resync_required" | "stream-error" | "ready";
  detail?: Record<string, unknown>;
}): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} stream`, args);
}

// notePlaceholderSynthesized fires unconditionally — this is the
// regression-detection signal for the post-#525 invariant that
// placeholder synthesis should be cold in production. Both the console
// warning AND the server-side counter beacon fire regardless of the
// debug flag. The fetch is fire-and-forget; failures (auth, network,
// abort) are swallowed so the reducer is never blocked by telemetry.
export function notePlaceholderSynthesized(event: SessionListEvent): void {
  console.warn(`${CONSOLE_PREFIX} placeholder synthesized`, {
    type: event.type,
    session_id: event.session_id,
    scope: event.session_scope,
    order_key: event.order_key,
  });
  try {
    void fetch("/api/debug/client-metric", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      // Allowlisted name; the server rejects any other value with 400
      // so we don't leak arbitrary client strings into Prometheus.
      body: JSON.stringify({ name: "session_list.placeholder_synthesized" }),
      keepalive: true,
    }).catch(() => {
      // swallow — telemetry must not affect rendering
    });
  } catch {
    // swallow
  }
}
