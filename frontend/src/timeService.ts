import { useCallback, useSyncExternalStore } from "react";

// Singleton time source for components rendering relative-time labels
// ("5s", "2m", "3h"). The previous design held `nowMs` as App-root
// `useState` and ticked it from a `setInterval`, which cascaded a full
// re-render through the 13K-line App tree on every tick — observed as
// 5+ second `correlation=idle` long-task blocks in
// `tank_client_long_task_duration_seconds`. The fix is to move the
// clock outside React state and let each consumer subscribe at the
// granularity it actually renders at.
//
// Implementation: one shared `setInterval` ticking at 1 Hz feeds a
// useSyncExternalStore. Each consumer's `getSnapshot` returns the
// current bucket at its requested granularity (seconds or minutes), so
// React's built-in snapshot-equality short-circuit skips re-renders
// unless that consumer's own bucket boundary has been crossed. A row
// showing "5m" re-renders once a minute; a row showing the second-
// resolution boot timer re-renders once a second. The App root
// re-renders zero times for clock ticks.
//
// The interval only runs while at least one consumer is mounted, so a
// signed-out / styleguide page pays nothing.
//
// Migration guard: `frontend/src/migrationPolicy.test.ts` asserts that
// no `setInterval`-driven `useState` setter pattern exists at App-root
// scope. Re-introducing the cascading pattern is a counted regression.

const TICK_INTERVAL_MS = 1_000;

const listeners = new Set<() => void>();
let tickHandle: ReturnType<typeof setInterval> | null = null;

function ensureTickerRunning(): void {
  if (tickHandle !== null) return;
  if (typeof window === "undefined") return;
  tickHandle = setInterval(() => {
    for (const listener of listeners) listener();
  }, TICK_INTERVAL_MS);
}

function stopTickerIfIdle(): void {
  if (tickHandle !== null && listeners.size === 0) {
    clearInterval(tickHandle);
    tickHandle = null;
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  ensureTickerRunning();
  return () => {
    listeners.delete(listener);
    stopTickerIfIdle();
  };
}

// Test-only hooks so unit tests can drive the ticker deterministically
// without owning the global setInterval. Production code uses `subscribe`
// indirectly through the hooks below.
export function __timeServiceTickForTest(): void {
  for (const listener of listeners) listener();
}

export function __timeServiceListenerCountForTest(): number {
  return listeners.size;
}

export function __timeServiceTickerRunningForTest(): boolean {
  return tickHandle !== null;
}

export function __timeServiceSubscribeForTest(listener: () => void): () => void {
  return subscribe(listener);
}

export function __timeServiceResetForTest(): void {
  listeners.clear();
  if (tickHandle !== null) {
    clearInterval(tickHandle);
    tickHandle = null;
  }
}

// useRelativeSeconds returns the number of full seconds elapsed since
// `startedAtMs`, or `null` when `startedAtMs` is null/NaN. Consumers
// using this hook re-render once per second while mounted — but only
// the calling component, not its parent.
export function useRelativeSeconds(startedAtMs: number | null): number | null {
  const getSnapshot = useCallback(() => {
    if (startedAtMs === null || !Number.isFinite(startedAtMs)) return null;
    return Math.max(0, Math.floor((Date.now() - startedAtMs) / 1000));
  }, [startedAtMs]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

// useRelativeMinutes returns the number of full minutes elapsed since
// `startedAtMs`, or `null` when `startedAtMs` is null/NaN. The
// snapshot bucket is the minute count so the consumer re-renders at
// most once per minute even though the ticker fires every second.
export function useRelativeMinutes(startedAtMs: number | null): number | null {
  const getSnapshot = useCallback(() => {
    if (startedAtMs === null || !Number.isFinite(startedAtMs)) return null;
    return Math.max(0, Math.floor((Date.now() - startedAtMs) / 60_000));
  }, [startedAtMs]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
