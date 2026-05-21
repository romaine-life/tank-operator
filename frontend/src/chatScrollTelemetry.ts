import { getStoredToken } from "./auth";

// Chat transcript scroll telemetry. Console logging is opt-in per browser with
// `localStorage.tankDebug = "chat-scroll"` or a comma-separated list that
// includes `chat-scroll`.
//
// Production observability is Prometheus-backed: semantic browser events are
// batched to the orchestrator, which owns the bounded metric labels.

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "chat-scroll";
const CONSOLE_PREFIX = "[tank/chat-scroll]";
const METRICS_ENDPOINT = "/api/client-metrics/chat-scroll";
const MAX_BATCH_EVENTS = 40;
const FLUSH_DELAY_MS = 1_000;
const MAX_DETAIL_KEYS = 60;
const MAX_STRING_LENGTH = 260;

interface ChatScrollMetricPayload {
  event: string;
  surface: string;
  sessionMode: string;
  atBottom?: boolean;
  hasScrollParent?: boolean;
  scrollTop?: number;
  scrollHeight?: number;
  clientHeight?: number;
  bottomDistance?: number;
  entries?: number;
  groups?: number;
  messages?: number;
  toolGroups?: number;
}

let pendingMetrics: ChatScrollMetricPayload[] = [];
let flushTimer: number | null = null;

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
  const sanitized = sanitizeDetail(detail);
  enqueueChatScrollMetric(event, sanitized);
  if (!isChatScrollDebugEnabled()) return;
  console.log(`${CONSOLE_PREFIX} ${event}`, sanitized);
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

export function flushChatScrollMetricsForTest(): void {
  if (flushTimer !== null && typeof window !== "undefined") {
    if (typeof window.clearTimeout === "function") {
      window.clearTimeout(flushTimer);
    }
    flushTimer = null;
  }
  flushChatScrollMetrics();
}

function enqueueChatScrollMetric(
  event: string,
  detail: Record<string, unknown>,
): void {
  if (typeof window === "undefined") return;
  pendingMetrics.push({
    event,
    surface: metricString(detail.surface) || inferMetricSurface(),
    sessionMode: metricString(detail.sessionMode) || "unknown",
    atBottom: typeof detail.atBottom === "boolean" ? detail.atBottom : undefined,
    hasScrollParent:
      typeof detail.hasScrollParent === "boolean" ? detail.hasScrollParent : undefined,
    scrollTop: metricNumber(detail.scrollTop),
    scrollHeight: metricNumber(detail.scrollHeight),
    clientHeight: metricNumber(detail.clientHeight),
    bottomDistance: metricNumber(detail.bottomDistance),
    entries: metricNumber(detail.entries),
    groups: metricNumber(detail.groups),
    messages: metricNumber(detail.messages),
    toolGroups: metricNumber(detail.toolGroups),
  });
  if (pendingMetrics.length >= MAX_BATCH_EVENTS) {
    flushChatScrollMetrics();
    return;
  }
  scheduleFlush();
}

function scheduleFlush(): void {
  if (typeof window === "undefined" || flushTimer !== null) return;
  flushTimer = window.setTimeout(() => {
    flushTimer = null;
    flushChatScrollMetrics();
  }, FLUSH_DELAY_MS);
}

function flushChatScrollMetrics(): void {
  if (typeof window === "undefined" || pendingMetrics.length === 0) return;
  const events = pendingMetrics.splice(0, MAX_BATCH_EVENTS);
  let token: string | null = null;
  try {
    token = getStoredToken();
  } catch {
    return;
  }
  if (!token) return;
  if (typeof fetch !== "function") return;
  fetch(METRICS_ENDPOINT, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ events }),
    keepalive: true,
  }).catch(() => undefined);
  if (pendingMetrics.length > 0) scheduleFlush();
}

function inferMetricSurface(): string {
  if (typeof window === "undefined") return "unknown";
  return window.location.pathname.startsWith("/_debug/") ? "debug_lab" : "session";
}

function metricString(value: unknown): string {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, 80);
}

function metricNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
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
    for (const [key, child] of Object.entries(value as Record<string, unknown>).slice(
      0,
      20,
    )) {
      out[key] = sanitizeValue(child, depth + 1);
    }
    return out;
  }
  return String(value);
}

if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flushChatScrollMetrics);
}
