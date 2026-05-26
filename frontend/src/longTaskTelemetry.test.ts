import { afterEach, beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  flushLongTaskMetricsForTest,
  ingestLongTaskEntryForTest,
  isLongTaskDebugEnabled,
  noteSessionSwitch,
  noteTankEvent,
  noteUserScroll,
  setActiveSessionMode,
  stopLongTaskObserverForTest,
} from "./longTaskTelemetry";

let fakeStorage: Record<string, string>;
let consoleLogs: Array<[string, unknown[]]>;
let origConsoleLog: typeof console.log;
let fetchCalls: Array<{ input: RequestInfo | URL; init?: RequestInit }>;
let origFetch: typeof fetch;
let fakeNow: number;
let origPerformance: typeof performance | undefined;

function setupBrowserEnv(): void {
  fakeStorage = {};
  consoleLogs = [];
  fetchCalls = [];
  fakeNow = 1_000;

  (globalThis as { localStorage?: unknown }).localStorage = {
    getItem(key: string) {
      return Object.prototype.hasOwnProperty.call(fakeStorage, key)
        ? fakeStorage[key]
        : null;
    },
    setItem(key: string, value: string) {
      fakeStorage[key] = value;
    },
    removeItem(key: string) {
      delete fakeStorage[key];
    },
    clear() {
      fakeStorage = {};
    },
    length: 0,
    key() {
      return null;
    },
  };

  origPerformance = globalThis.performance;
  (globalThis as { performance: { now: () => number } }).performance = {
    now: () => fakeNow,
  };

  origConsoleLog = console.log;
  console.log = (message: string, ...args: unknown[]) => {
    consoleLogs.push([message, args]);
  };
  origFetch = fetch;
  (globalThis as { fetch: typeof fetch }).fetch = (async (input, init) => {
    fetchCalls.push({ input, init });
    return new Response("{}", { status: 202 });
  }) as typeof fetch;

  (globalThis as { window?: unknown }).window = {
    location: { pathname: "/sessions/188", search: "?session=188" },
    setTimeout: (() => 1) as typeof window.setTimeout,
    clearTimeout: () => undefined,
    addEventListener: () => undefined,
  };
}

beforeEach(() => {
  setupBrowserEnv();
});

afterEach(() => {
  stopLongTaskObserverForTest();
  console.log = origConsoleLog;
  (globalThis as { fetch: typeof fetch }).fetch = origFetch;
  delete (globalThis as { localStorage?: unknown }).localStorage;
  delete (globalThis as { window?: unknown }).window;
  if (origPerformance) {
    (globalThis as { performance: typeof performance }).performance = origPerformance;
  } else {
    delete (globalThis as { performance?: unknown }).performance;
  }
});

function fakeLongTaskEntry(
  duration: number,
  startTime: number,
  name = "self",
): PerformanceEntry {
  return {
    duration,
    startTime,
    name,
    entryType: "longtask",
    toJSON: () => ({ duration, startTime, name }),
  } as unknown as PerformanceEntry;
}

test("long-task debug logs are off by default", () => {
  assert.equal(isLongTaskDebugEnabled(), false);
  ingestLongTaskEntryForTest(fakeLongTaskEntry(120, 500));
  assert.equal(consoleLogs.length, 0);
});

test("long-task debug logs fire when tankDebug includes long-tasks", () => {
  fakeStorage.tankDebug = "session-list,long-tasks";
  assert.equal(isLongTaskDebugEnabled(), true);
  ingestLongTaskEntryForTest(fakeLongTaskEntry(120, 500));
  assert.equal(consoleLogs.length, 1);
  assert.equal(consoleLogs[0]?.[0], "[tank/long-tasks] long-task");
});

test("entries below the 50ms threshold are dropped", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  ingestLongTaskEntryForTest(fakeLongTaskEntry(40, 500));
  flushLongTaskMetricsForTest();
  assert.equal(fetchCalls.length, 0);
});

test("entries are batched to the Prometheus ingestion endpoint", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");
  ingestLongTaskEntryForTest(fakeLongTaskEntry(180, 600, "self"));
  flushLongTaskMetricsForTest();

  assert.equal(fetchCalls.length, 1);
  assert.equal(fetchCalls[0]?.input, "/api/client-metrics/long-tasks");
  assert.equal(fetchCalls[0]?.init?.method, "POST");
  assert.equal(
    new Headers(fetchCalls[0]?.init?.headers).get("Authorization"),
    "Bearer token-123",
  );
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  assert.equal(event.durationMs, 180);
  assert.equal(event.startMs, 600);
  assert.equal(event.sessionMode, "claude_gui");
  assert.equal(event.attribution, "self");
  assert.equal(event.pagePath, "/sessions/188");
});

test("correlation hints record deltas to recent tank-event / session-switch / scroll", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");

  fakeNow = 1_000;
  noteTankEvent();
  fakeNow = 1_050;
  noteSessionSwitch();
  fakeNow = 1_080;
  noteUserScroll();
  // Long task starts at t=1100ms, runs 250ms.
  ingestLongTaskEntryForTest(fakeLongTaskEntry(250, 1_100));
  flushLongTaskMetricsForTest();

  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  assert.equal(event.sinceTankEventMs, 100);
  assert.equal(event.sinceSessionSwitchMs, 50);
  assert.equal(event.sinceScrollMs, 20);
});

test("correlation deltas are null when the signal hasn't fired yet", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");
  ingestLongTaskEntryForTest(fakeLongTaskEntry(150, 500));
  flushLongTaskMetricsForTest();

  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  assert.equal(event.sinceTankEventMs, null);
  assert.equal(event.sinceSessionSwitchMs, null);
  assert.equal(event.sinceScrollMs, null);
});

test("batched entries flush when they hit the batch cap without waiting for the timer", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");
  for (let i = 0; i < 40; i += 1) {
    ingestLongTaskEntryForTest(fakeLongTaskEntry(60, 100 + i));
  }
  // No explicit flush call; the 40th entry should have tripped the cap.
  assert.equal(fetchCalls.length, 1);
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  assert.equal(payload.events.length, 40);
});
