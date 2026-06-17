import { authedFetch, getStoredToken } from "./auth";

// Per-session transcript-row SSE telemetry. The candidate-B (zombie SSE)
// and candidate-C (row-drop) stethoscope on the browser side.
// Console logging is opt-in per browser with localStorage.tankDebug
// = "session-events" (or a comma-separated list including that token).
//
// Production observability is Prometheus-backed: semantic browser
// events batch to the orchestrator at
// POST /api/client-metrics/session-events-stream, which owns the
// bounded metric labels (per docs/observability.md cardinality rules).

const DEBUG_STORAGE_KEY = "tankDebug";
const DEBUG_TOKEN = "session-events";
const CONSOLE_PREFIX = "[tank/session-events]";
const METRICS_ENDPOINT = "/api/client-metrics/session-events-stream";
const MAX_BATCH_EVENTS = 60;
const FLUSH_DELAY_MS = 1_000;

// Closed-enum bounded labels. Mirrors the allowlist on the server side
// in handlers_client_metrics_session_events.go — keep them in sync.
export type SessionEventStreamMetricName =
  | "opened"
  | "ready"
  | "transcript_rows_received"
  | "transcript_rows_applied"
  | "stream_silent_while_running"
  | "terminal_matched_by_turn_id"
  | "terminal_local_run_mismatch"
  | "queued_followup_blocked_after_terminal"
  | "stale_running_blocked_submit"
  | "turn_activity_load_started"
  | "turn_activity_load_succeeded"
  | "turn_activity_load_failed"
  | "turn_activity_load_timed_out"
  | "turn_activity_load_stale"
  // Behavior-free watchdog: the activity body stayed on "Loading activity..."
  // past the stuck threshold. `_unloaded` = no load ever started (the strand);
  // `_loading` = a load was in the loading state past the threshold (slow/hung).
  | "turn_activity_stuck_unloaded"
  | "turn_activity_stuck_loading"
  | "turn_activity_refresh_failed"
  | "turn_activity_refresh_gave_up"
  | "turn_activity_refresh_recovered"
  | "turn_activity_collapse_applied"
  | "turn_activity_collapse_projection_mismatch"
  | "turn_number_unavailable_target"
  | "resync_required"
  | "stream_error"
  | "closed_unmount"
  | "closed_error"
  | "reconnect_scheduled";

interface SessionEventStreamMetricPayload {
  event: SessionEventStreamMetricName;
  sessionMode: string;
  eventType?: string;
  idleSeconds?: number;
  whileRunning?: boolean;
}

let pendingMetrics: SessionEventStreamMetricPayload[] = [];
let flushTimer: number | null = null;

export function isSessionEventStreamDebugEnabled(): boolean {
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

export function logSessionEventStreamEvent(
  event: SessionEventStreamMetricName,
  detail: {
    sessionMode: string;
    eventType?: string;
    idleSeconds?: number;
    whileRunning?: boolean;
  },
): void {
  enqueue({
    event,
    sessionMode: clampString(detail.sessionMode) || "unknown",
    eventType: detail.eventType ? clampString(detail.eventType) : undefined,
    idleSeconds:
      typeof detail.idleSeconds === "number" && Number.isFinite(detail.idleSeconds) && detail.idleSeconds >= 0
        ? detail.idleSeconds
        : undefined,
    whileRunning: typeof detail.whileRunning === "boolean" ? detail.whileRunning : undefined,
  });
  if (isSessionEventStreamDebugEnabled()) {
    // Console-side trace mirrors the prom event for the rare cases
    // where the operator is sitting at a browser tab during the bug
    // and wants per-event ordering without round-tripping through
    // /metrics scrape intervals.
    console.log(`${CONSOLE_PREFIX} ${event}`, detail);
  }
}

export function flushSessionEventStreamMetricsForTest(): void {
  if (flushTimer !== null && typeof window !== "undefined") {
    if (typeof window.clearTimeout === "function") {
      window.clearTimeout(flushTimer);
    }
    flushTimer = null;
  }
  flush();
}

function enqueue(payload: SessionEventStreamMetricPayload): void {
  if (typeof window === "undefined") return;
  pendingMetrics.push(payload);
  if (pendingMetrics.length >= MAX_BATCH_EVENTS) {
    flush();
    return;
  }
  scheduleFlush();
}

function scheduleFlush(): void {
  if (typeof window === "undefined" || flushTimer !== null) return;
  flushTimer = window.setTimeout(() => {
    flushTimer = null;
    flush();
  }, FLUSH_DELAY_MS);
}

function flush(): void {
  if (typeof window === "undefined" || pendingMetrics.length === 0) return;
  const events = pendingMetrics.splice(0, MAX_BATCH_EVENTS);
  if (typeof fetch !== "function") return;
  if (!getStoredToken()) return;
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

function clampString(value: unknown): string {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, 80);
}

if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flush);
}

// SilenceWatchdog wraps a 30s "I haven't seen projected rows" timer per
// SSE consumer. The caller resets the timer on every transcript-row batch and on
// open; if the timer fires while a turn is in flight, the watchdog
// emits a stream_silent_while_running metric with the observed idle
// duration. Designed to be cheap (one setTimeout per session pane,
// reset on every event) and harmless (no behavior change, no
// reconnect) so it never masks the underlying bug.
export interface SilenceWatchdogOptions {
  sessionMode: string;
  /** Threshold above which the watchdog publishes a silent metric. */
  idleThresholdMs?: number;
  /** Returns true when a turn is currently in flight on this pane. */
  isRunning: () => boolean;
  /** Hook for tests to stub window.setTimeout. */
  setTimeoutFn?: typeof window.setTimeout;
  /** Hook for tests to stub window.clearTimeout. */
  clearTimeoutFn?: typeof window.clearTimeout;
  /** Hook for tests to stub Date.now(). */
  now?: () => number;
  /** Hook for tests to capture emit calls without touching the global queue. */
  emit?: (event: SessionEventStreamMetricName, detail: {
    sessionMode: string;
    idleSeconds: number;
    whileRunning: boolean;
  }) => void;
}

export interface SilenceWatchdog {
  /** Restarts the silence countdown. Call on stream open + each event. */
  reset(): void;
  /** Cancels the watchdog. Call when the stream closes. */
  stop(): void;
}

export function createSilenceWatchdog(options: SilenceWatchdogOptions): SilenceWatchdog {
  const threshold = options.idleThresholdMs ?? 30_000;
  const setTimeoutFn = options.setTimeoutFn ?? window.setTimeout.bind(window);
  const clearTimeoutFn = options.clearTimeoutFn ?? window.clearTimeout.bind(window);
  const nowFn = options.now ?? Date.now;
  const emit =
    options.emit ??
    ((event, detail) =>
      logSessionEventStreamEvent(event, detail));
  let timer: ReturnType<typeof setTimeoutFn> | null = null;
  let startedAt = nowFn();
  let stopped = false;

  function arm(): void {
    if (stopped) return;
    timer = setTimeoutFn(() => {
      timer = null;
      if (stopped) return;
      const idleSeconds = (nowFn() - startedAt) / 1000;
      const whileRunning = options.isRunning();
      emit("stream_silent_while_running", {
        sessionMode: options.sessionMode,
        idleSeconds,
        whileRunning,
      });
      // Re-arm so a continuously-silent stream keeps producing
      // observations every threshold; without re-arming a long stall
      // would emit exactly one point and look like a brief blip on
      // the histogram.
      startedAt = nowFn();
      arm();
    }, threshold);
  }

  return {
    reset(): void {
      if (stopped) return;
      if (timer !== null) clearTimeoutFn(timer);
      startedAt = nowFn();
      arm();
    },
    stop(): void {
      stopped = true;
      if (timer !== null) {
        clearTimeoutFn(timer);
        timer = null;
      }
    },
  };
}
