// Chat transcript scroll telemetry. Enable per browser with
// `localStorage.tankDebug = "chat-scroll"` or a comma-separated list that
// includes `chat-scroll`.
//
// This intentionally mirrors sessionListTelemetry's zero-cost-by-default
// console surface. It is for diagnosing user-visible scroll trust failures
// without making browser-local position a product source of truth.

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "chat-scroll";
const CONSOLE_PREFIX = "[tank/chat-scroll]";

export function isChatScrollDebugEnabled(): boolean {
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

export function logChatScrollEvent(
  event: string,
  detail: Record<string, unknown> = {},
): void {
  if (!isChatScrollDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} ${event}`, detail);
}
