import { authedFetch } from "./auth";

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
  sessionId?: string;
  pagePath?: string;
  pageSearch?: string;
  source?: string;
  anchor?: string;
  requestId?: string;
  beforeCursor?: string;
  afterCursor?: string;
  reason?: string;
  key?: string;
  firstGroupKey?: string;
  lastGroupKey?: string;
  previousFirstGroupKey?: string;
  previousLastGroupKey?: string;
  targetEdge?: string;
  navInFlight?: string;
  signal?: number;
  status?: number;
  durationMs?: number;
  eventCount?: number;
  canonicalEventCount?: number;
  foundOldest?: boolean;
  foundNewest?: boolean;
  hasPrevCursor?: boolean;
  hasNextCursor?: boolean;
  clearRealtime?: boolean;
  atBottom?: boolean;
  hasScrollParent?: boolean;
  scrollTop?: number;
  scrollHeight?: number;
  clientHeight?: number;
  bottomDistance?: number;
  entries?: number;
  previousEntries?: number;
  entriesDelta?: number;
  groups?: number;
  previousGroups?: number;
  groupsDelta?: number;
  visibleRowsAdded?: number;
  visibleRowsRemoved?: number;
  messages?: number;
  previousMessages?: number;
  messagesDelta?: number;
  reasoning?: number;
  meta?: number;
  backgroundTasks?: number;
  thinkingGroups?: number;
  toolGroups?: number;
  toolEntries?: number;
  activityGroups?: number;
  activeActivityGroups?: number;
  durableActiveActivityGroups?: number;
  activityEntries?: number;
  turnActivityShells?: number;
  durableActiveTurnActivityShells?: number;
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
    sessionId: metricString(detail.sessionId),
    pagePath: currentPagePath(),
    pageSearch: currentPageSearch(),
    source: metricString(detail.source),
    anchor: metricString(detail.anchor),
    requestId: metricString(detail.requestId),
    beforeCursor: metricString(detail.beforeCursor),
    afterCursor: metricString(detail.afterCursor),
    reason: metricString(detail.reason),
    key: metricString(detail.key),
    firstGroupKey: metricString(detail.firstGroupKey),
    lastGroupKey: metricString(detail.lastGroupKey),
    previousFirstGroupKey: metricString(detail.previousFirstGroupKey),
    previousLastGroupKey: metricString(detail.previousLastGroupKey),
    targetEdge: metricString(detail.targetEdge),
    navInFlight: metricString(detail.navInFlight),
    signal: metricNumber(detail.signal),
    status: metricNumber(detail.status),
    durationMs: metricNumber(detail.durationMs),
    eventCount: metricNumber(detail.eventCount),
    canonicalEventCount: metricNumber(detail.canonicalEventCount),
    foundOldest:
      typeof detail.foundOldest === "boolean" ? detail.foundOldest : undefined,
    foundNewest:
      typeof detail.foundNewest === "boolean" ? detail.foundNewest : undefined,
    hasPrevCursor:
      typeof detail.hasPrevCursor === "boolean" ? detail.hasPrevCursor : undefined,
    hasNextCursor:
      typeof detail.hasNextCursor === "boolean" ? detail.hasNextCursor : undefined,
    clearRealtime:
      typeof detail.clearRealtime === "boolean" ? detail.clearRealtime : undefined,
    atBottom: typeof detail.atBottom === "boolean" ? detail.atBottom : undefined,
    hasScrollParent:
      typeof detail.hasScrollParent === "boolean" ? detail.hasScrollParent : undefined,
    scrollTop: metricNumber(detail.scrollTop),
    scrollHeight: metricNumber(detail.scrollHeight),
    clientHeight: metricNumber(detail.clientHeight),
    bottomDistance: metricNumber(detail.bottomDistance),
    entries: metricNumber(detail.entries),
    previousEntries: metricNumber(detail.previousEntries),
    entriesDelta: metricNumber(detail.entriesDelta),
    groups: metricNumber(detail.groups),
    previousGroups: metricNumber(detail.previousGroups),
    groupsDelta: metricNumber(detail.groupsDelta),
    visibleRowsAdded: metricNumber(detail.visibleRowsAdded),
    visibleRowsRemoved: metricNumber(detail.visibleRowsRemoved),
    messages: metricNumber(detail.messages),
    previousMessages: metricNumber(detail.previousMessages),
    messagesDelta: metricNumber(detail.messagesDelta),
    reasoning: metricNumber(detail.reasoning),
    meta: metricNumber(detail.meta),
    backgroundTasks: metricNumber(detail.backgroundTasks),
    thinkingGroups: metricNumber(detail.thinkingGroups),
    toolGroups: metricNumber(detail.toolGroups),
    toolEntries: metricNumber(detail.toolEntries),
    activityGroups: metricNumber(detail.activityGroups),
    activeActivityGroups: metricNumber(detail.activeActivityGroups),
    durableActiveActivityGroups: metricNumber(detail.durableActiveActivityGroups),
    activityEntries: metricNumber(detail.activityEntries),
    turnActivityShells: metricNumber(detail.turnActivityShells),
    durableActiveTurnActivityShells: metricNumber(detail.durableActiveTurnActivityShells),
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
  if (typeof fetch !== "function") return;
  authedFetch(METRICS_ENDPOINT, {
    method: "POST",
    headers: {
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

function currentPagePath(): string {
  if (typeof window === "undefined") return "";
  return window.location.pathname.slice(0, 160);
}

function currentPageSearch(): string {
  if (typeof window === "undefined") return "";
  return window.location.search.slice(0, 240);
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
