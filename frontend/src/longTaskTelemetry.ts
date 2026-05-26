import { authedFetch } from "./auth";

// Browser long-task probe. Wraps PerformanceObserver({ type: "longtask" })
// to surface main-thread blocks ≥50 ms — the input-blocking failure mode
// that produces "clicks aren't responding" without burning sustained CPU
// or memory. Pairs with server-bucketed Prometheus labels in
// backend-go/cmd/tank-operator/handlers_client_metrics_long_tasks.go so
// the SPA never controls cardinality directly.
//
// Correlation hints (last tank-event, last session-switch, last scroll)
// let the server bucket each long task into a single attribution label
// without leaking high-cardinality context. Without correlation a
// duration histogram only tells us "things are slow"; with correlation
// it tells us "things are slow specifically when an SSE event burst
// lands," which is the actionable signal.
//
// Console logging is opt-in via `localStorage.tankDebug = "long-tasks"`
// (comma-separated list including that token), mirroring the
// chat-scroll and session-events probes.

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "long-tasks";
const CONSOLE_PREFIX = "[tank/long-tasks]";
const METRICS_ENDPOINT = "/api/client-metrics/long-tasks";
const MAX_BATCH_EVENTS = 40;
const FLUSH_DELAY_MS = 1_000;
// Drop entries below this threshold so an upstream PerformanceObserver
// change (the spec is moving toward "long-animation-frame" which
// surfaces shorter blocks) doesn't accidentally widen the metric
// surface beyond what we agreed to ingest.
const MIN_REPORTED_DURATION_MS = 50;

interface LongTaskMetricPayload {
  durationMs: number;
  startMs: number;
  sessionMode: string;
  // Milliseconds since the most recent of each correlation signal.
  // null means "never observed in this pageload."
  sinceTankEventMs: number | null;
  sinceSessionSwitchMs: number | null;
  sinceScrollMs: number | null;
  // PerformanceLongTaskTiming.name is one of a closed enum
  // ("self" / "same-origin-ancestor" / "same-origin-descendant" /
  // "same-origin" / "cross-origin-ancestor" / "cross-origin-descendant"
  // / "cross-origin-unreachable" / "multiple-contexts" / "unknown").
  // We pass it through; the server buckets to "self" / "other".
  attribution: string;
  pagePath: string;
}

let pendingMetrics: LongTaskMetricPayload[] = [];
let flushTimer: number | null = null;
let observer: PerformanceObserver | null = null;
let currentSessionMode = "unknown";

// Module-level correlation timestamps. Updated by note*() hooks called
// from the SSE handler, session-switch effect, and scroll listener.
// performance.now() to share a clock with PerformanceObserver entries.
let lastTankEventMs: number | null = null;
let lastSessionSwitchMs: number | null = null;
let lastScrollMs: number | null = null;

export function noteTankEvent(): void {
  if (typeof performance === "undefined") return;
  lastTankEventMs = performance.now();
}

export function noteSessionSwitch(): void {
  if (typeof performance === "undefined") return;
  lastSessionSwitchMs = performance.now();
}

export function noteUserScroll(): void {
  if (typeof performance === "undefined") return;
  lastScrollMs = performance.now();
}

// setActiveSessionMode lets the app shell push the currently-visible
// session's mode so correlation-attributed long tasks land under the
// right bucket. Called whenever the active pane changes; cleared back
// to "unknown" when no session pane is visible.
export function setActiveSessionMode(mode: string): void {
  currentSessionMode = clampString(mode) || "unknown";
}

export function isLongTaskDebugEnabled(): boolean {
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

export function startLongTaskObserver(): boolean {
  if (typeof window === "undefined") return false;
  if (typeof PerformanceObserver === "undefined") return false;
  if (observer) return true;
  // PerformanceObserver.supportedEntryTypes is the right capability
  // probe; Firefox doesn't ship longtask. The probe degrades silently
  // there — the server just won't see any data from that browser.
  const supported = (PerformanceObserver as unknown as {
    supportedEntryTypes?: ReadonlyArray<string>;
  }).supportedEntryTypes;
  if (Array.isArray(supported) && !supported.includes("longtask")) {
    return false;
  }
  try {
    observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        ingestLongTaskEntry(entry);
      }
    });
    observer.observe({ type: "longtask", buffered: true });
  } catch {
    observer = null;
    return false;
  }
  return true;
}

export function stopLongTaskObserverForTest(): void {
  if (observer) {
    try {
      observer.disconnect();
    } catch {
      // best-effort
    }
    observer = null;
  }
  currentSessionMode = "unknown";
  pendingMetrics = [];
  if (flushTimer !== null && typeof window !== "undefined") {
    if (typeof window.clearTimeout === "function") {
      window.clearTimeout(flushTimer);
    }
    flushTimer = null;
  }
  lastTankEventMs = null;
  lastSessionSwitchMs = null;
  lastScrollMs = null;
}

export function flushLongTaskMetricsForTest(): void {
  if (flushTimer !== null && typeof window !== "undefined") {
    if (typeof window.clearTimeout === "function") {
      window.clearTimeout(flushTimer);
    }
    flushTimer = null;
  }
  flushLongTaskMetrics();
}

// Exposed for tests so the observer-side path can be exercised
// without depending on the browser actually scheduling a long task.
export function ingestLongTaskEntryForTest(entry: PerformanceEntry): void {
  ingestLongTaskEntry(entry);
}

function ingestLongTaskEntry(entry: PerformanceEntry): void {
  const duration = Math.round(entry.duration);
  if (!Number.isFinite(duration) || duration < MIN_REPORTED_DURATION_MS) {
    return;
  }
  const startMs = Math.round(entry.startTime);
  const attribution =
    typeof entry.name === "string" && entry.name ? entry.name : "unknown";
  const payload: LongTaskMetricPayload = {
    durationMs: duration,
    startMs,
    sessionMode: currentSessionMode,
    sinceTankEventMs: deltaSince(lastTankEventMs, entry.startTime),
    sinceSessionSwitchMs: deltaSince(lastSessionSwitchMs, entry.startTime),
    sinceScrollMs: deltaSince(lastScrollMs, entry.startTime),
    attribution: clampString(attribution) || "unknown",
    pagePath: currentPagePath(),
  };
  pendingMetrics.push(payload);
  if (isLongTaskDebugEnabled()) {
    console.log(`${CONSOLE_PREFIX} long-task`, payload);
  }
  if (pendingMetrics.length >= MAX_BATCH_EVENTS) {
    flushLongTaskMetrics();
    return;
  }
  scheduleFlush();
}

function scheduleFlush(): void {
  if (typeof window === "undefined" || flushTimer !== null) return;
  flushTimer = window.setTimeout(() => {
    flushTimer = null;
    flushLongTaskMetrics();
  }, FLUSH_DELAY_MS);
}

function flushLongTaskMetrics(): void {
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

function deltaSince(eventMs: number | null, atMs: number): number | null {
  if (eventMs === null) return null;
  const delta = Math.round(atMs - eventMs);
  if (!Number.isFinite(delta) || delta < 0) return null;
  return delta;
}

function clampString(value: unknown): string {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, 80);
}

function currentPagePath(): string {
  if (typeof window === "undefined") return "";
  return window.location.pathname.slice(0, 160);
}

if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flushLongTaskMetrics);
}
