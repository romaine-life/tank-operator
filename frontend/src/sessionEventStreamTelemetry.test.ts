import assert from "node:assert/strict";
import test from "node:test";

import { createSilenceWatchdog } from "./sessionEventStreamTelemetry";

// SilenceWatchdog is the candidate-B detector on the browser side.
// These tests pin its behavior so a future refactor cannot quietly
// turn the metric off (the watchdog must publish observations while
// silence persists, must respect the whileRunning predicate, and
// must stop cleanly on stop()).

test("watchdog publishes a metric after idleThresholdMs", () => {
  let nowMs = 1_000;
  const scheduled: Array<{ fn: () => void; delay: number; handle: number }> = [];
  let nextHandle = 1;
  const setTimeoutFn = ((fn: () => void, delay: number) => {
    const handle = nextHandle++;
    scheduled.push({ fn, delay, handle });
    return handle;
  }) as unknown as typeof window.setTimeout;
  const clearTimeoutFn = ((handle: number) => {
    const i = scheduled.findIndex((s) => s.handle === handle);
    if (i >= 0) scheduled.splice(i, 1);
  }) as unknown as typeof window.clearTimeout;
  const emits: Array<{ idleSeconds: number; whileRunning: boolean }> = [];
  const watchdog = createSilenceWatchdog({
    sessionMode: "claude_gui",
    idleThresholdMs: 30_000,
    isRunning: () => true,
    setTimeoutFn,
    clearTimeoutFn,
    now: () => nowMs,
    emit: (_event, detail) => emits.push({ idleSeconds: detail.idleSeconds, whileRunning: detail.whileRunning }),
  });

  watchdog.reset();
  assert.equal(scheduled.length, 1, "reset should schedule one timer");
  assert.equal(scheduled[0]!.delay, 30_000);

  // Advance clock + fire timer.
  nowMs = 31_000;
  const fired = scheduled.shift()!;
  fired.fn();

  assert.equal(emits.length, 1);
  assert.equal(emits[0]!.whileRunning, true);
  assert.ok(emits[0]!.idleSeconds >= 30 && emits[0]!.idleSeconds < 31);

  // Re-arm after fire — silence still ongoing means we keep observing.
  assert.equal(scheduled.length, 1, "watchdog should re-arm after firing");

  watchdog.stop();
});

test("watchdog reset clears the pending timer", () => {
  const scheduled: Array<{ fn: () => void; delay: number; handle: number }> = [];
  let nextHandle = 1;
  const setTimeoutFn = ((fn: () => void, delay: number) => {
    const handle = nextHandle++;
    scheduled.push({ fn, delay, handle });
    return handle;
  }) as unknown as typeof window.setTimeout;
  const clearTimeoutFn = ((handle: number) => {
    const i = scheduled.findIndex((s) => s.handle === handle);
    if (i >= 0) scheduled.splice(i, 1);
  }) as unknown as typeof window.clearTimeout;
  const watchdog = createSilenceWatchdog({
    sessionMode: "claude_gui",
    idleThresholdMs: 30_000,
    isRunning: () => true,
    setTimeoutFn,
    clearTimeoutFn,
    now: () => 0,
    emit: () => assert.fail("should not fire after reset clears"),
  });
  watchdog.reset();
  assert.equal(scheduled.length, 1);
  watchdog.reset(); // clears and re-arms
  assert.equal(scheduled.length, 1, "outstanding timer should be cleared on re-reset");
  watchdog.stop();
  assert.equal(scheduled.length, 0, "stop() should clear the timer");
});

test("watchdog reports whileRunning=false outside a turn", () => {
  let nowMs = 0;
  const scheduled: Array<{ fn: () => void; delay: number }> = [];
  const setTimeoutFn = ((fn: () => void, delay: number) => {
    scheduled.push({ fn, delay });
    return scheduled.length;
  }) as unknown as typeof window.setTimeout;
  const clearTimeoutFn = ((_handle: number) => undefined) as unknown as typeof window.clearTimeout;
  const emits: Array<{ whileRunning: boolean }> = [];
  const watchdog = createSilenceWatchdog({
    sessionMode: "claude_gui",
    idleThresholdMs: 30_000,
    isRunning: () => false,
    setTimeoutFn,
    clearTimeoutFn,
    now: () => nowMs,
    emit: (_event, detail) => emits.push({ whileRunning: detail.whileRunning }),
  });
  watchdog.reset();
  nowMs = 31_000;
  scheduled[0]!.fn();
  assert.equal(emits.length, 1);
  assert.equal(emits[0]!.whileRunning, false, "still emits metric — operator reads idleSeconds for both states");
  watchdog.stop();
});
