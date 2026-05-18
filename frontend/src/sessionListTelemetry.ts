// Sidebar session-list client-side telemetry. After
// docs/session-list-redesign.md Phase 3 the surface is just the
// debug-gated console transcript: enable per browser by setting
// `localStorage.tankDebug = "session-list"` (comma-separated tokens
// accepted; this checks for "session-list" specifically). Once
// enabled, every snapshot fetch, every applied row update, every
// cursor advance, and every stream signal prints to the console
// under `[tank/session-list]`. Off by default — zero perf cost when
// not enabled.
//
// The Phase 2 client-metric beacon was retired in Phase 3 alongside
// the entire placeholder-synthesis branch in the sidebar reducer —
// the row-update wire makes synthesis unreachable by construction
// (the wire always carries the full row; there is nothing to
// synthesize). The related backend counter and its alert are
// removed in the same PR.
//
// Helpers are best-effort: they swallow localStorage errors so a
// misconfigured browser can never affect the reducer's correctness
// path.

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

// logSessionListEvent prints a short summary of one applied wire
// frame. Signature is intentionally loose so the row-update wire's
// SessionRowUpdatePayload and any future shape can both pass through
// without the telemetry types bleeding into the consumer.
export function logSessionListEvent(event: {
  type?: string;
  session_id?: string;
  session_scope?: string;
  order_key?: string;
}): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} event`, event);
}

export function logSessionListStreamSignal(args: {
  signal: "resync_required" | "stream-error" | "ready";
  detail?: Record<string, unknown>;
}): void {
  if (!isDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} stream`, args);
}
