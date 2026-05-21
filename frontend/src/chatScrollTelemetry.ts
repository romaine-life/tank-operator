// Chat transcript scroll telemetry. Console logging is opt-in per browser with
// `localStorage.tankDebug = "chat-scroll"` or a comma-separated list that
// includes `chat-scroll`.
//
// The bounded local ledger is always written. It gives the app an after-the-
// fact diagnostic surface for user-visible scroll trust failures without
// making browser-local position a product source of truth.

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "chat-scroll";
const CONSOLE_PREFIX = "[tank/chat-scroll]";
const LEDGER_STORAGE_KEY = "tank.chatScrollEvents";
const LEDGER_EVENT_NAME = "tank:chat-scroll-telemetry";
const MAX_LEDGER_RECORDS = 500;
const MAX_DETAIL_KEYS = 60;
const MAX_STRING_LENGTH = 260;

export interface ChatScrollTelemetryRecord {
  id: string;
  at: string;
  event: string;
  detail: Record<string, unknown>;
  path?: string;
}

let volatileLedger: ChatScrollTelemetryRecord[] = [];
let sequence = 0;

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

export function setChatScrollDebugEnabled(enabled: boolean): void {
  try {
    const tokens = new Set(
      (localStorage.getItem(DEBUG_STORAGE_KEY) ?? "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
    );
    if (enabled) tokens.add(DEBUG_TOKEN);
    else tokens.delete(DEBUG_TOKEN);
    if (tokens.size === 0) {
      localStorage.removeItem(DEBUG_STORAGE_KEY);
    } else {
      localStorage.setItem(DEBUG_STORAGE_KEY, [...tokens].join(","));
    }
  } catch {
    // localStorage can be unavailable in hardened/private contexts.
  }
}

export function logChatScrollEvent(
  event: string,
  detail: Record<string, unknown> = {},
): void {
  const record = appendChatScrollRecord(event, detail);
  if (!isChatScrollDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} ${event}`, record.detail);
}

export function readChatScrollEvents(): ChatScrollTelemetryRecord[] {
  const persisted = readPersistedLedger();
  return persisted.length > 0 ? persisted : volatileLedger;
}

export function clearChatScrollEvents(): void {
  volatileLedger = [];
  try {
    localStorage.removeItem(LEDGER_STORAGE_KEY);
  } catch {
    // localStorage can be unavailable in hardened/private contexts.
  }
  emitChatScrollRecordEvent(null);
}

export function chatScrollElementSnapshot(
  element: HTMLElement | null,
): Record<string, unknown> {
  if (!element) return { hasScrollParent: false };
  const scrollTop = Math.round(element.scrollTop);
  const scrollHeight = Math.round(element.scrollHeight);
  const clientHeight = Math.round(element.clientHeight);
  return {
    hasScrollParent: true,
    scrollTop,
    scrollHeight,
    clientHeight,
    bottomDistance: Math.max(0, scrollHeight - scrollTop - clientHeight),
  };
}

function appendChatScrollRecord(
  event: string,
  detail: Record<string, unknown>,
): ChatScrollTelemetryRecord {
  sequence += 1;
  const record: ChatScrollTelemetryRecord = {
    id: `${Date.now()}-${sequence}`,
    at: new Date().toISOString(),
    event,
    detail: sanitizeDetail(detail),
    path: typeof window !== "undefined" ? window.location.pathname : undefined,
  };
  const nextLedger = [...readChatScrollEvents(), record].slice(-MAX_LEDGER_RECORDS);
  volatileLedger = nextLedger;
  try {
    localStorage.setItem(LEDGER_STORAGE_KEY, JSON.stringify(nextLedger));
  } catch {
    // Keep the in-memory ledger when localStorage quota/access fails.
  }
  emitChatScrollRecordEvent(record);
  return record;
}

function readPersistedLedger(): ChatScrollTelemetryRecord[] {
  try {
    const raw = localStorage.getItem(LEDGER_STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(isChatScrollTelemetryRecord).slice(-MAX_LEDGER_RECORDS);
  } catch {
    return [];
  }
}

function isChatScrollTelemetryRecord(value: unknown): value is ChatScrollTelemetryRecord {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.id === "string" &&
    typeof candidate.at === "string" &&
    typeof candidate.event === "string" &&
    Boolean(candidate.detail) &&
    typeof candidate.detail === "object" &&
    !Array.isArray(candidate.detail)
  );
}

function sanitizeDetail(detail: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(detail).slice(0, MAX_DETAIL_KEYS)) {
    out[key] = sanitizeValue(value, 0);
  }
  return out;
}

function sanitizeValue(value: unknown, depth: number): unknown {
  if (value == null) return value;
  if (typeof value === "number") return Number.isFinite(value) ? value : String(value);
  if (typeof value === "boolean") return value;
  if (typeof value === "string") {
    return value.length <= MAX_STRING_LENGTH
      ? value
      : `${value.slice(0, MAX_STRING_LENGTH)}...`;
  }
  if (Array.isArray(value)) {
    if (depth >= 2) return `[array:${value.length}]`;
    return value.slice(0, 20).map((item) => sanitizeValue(item, depth + 1));
  }
  if (typeof value === "object") {
    if (depth >= 2) return "[object]";
    const out: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value as Record<string, unknown>).slice(0, 20)) {
      out[key] = sanitizeValue(child, depth + 1);
    }
    return out;
  }
  return String(value);
}

function emitChatScrollRecordEvent(record: ChatScrollTelemetryRecord | null): void {
  if (typeof window === "undefined") return;
  try {
    window.dispatchEvent(new CustomEvent(LEDGER_EVENT_NAME, { detail: record }));
  } catch {
    // CustomEvent can be blocked in unusual embedded contexts.
  }
}
