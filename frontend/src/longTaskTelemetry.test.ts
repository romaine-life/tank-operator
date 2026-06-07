import { afterEach, beforeEach, test, expect } from "vitest";
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
  expect(isLongTaskDebugEnabled()).toBe(false);
  ingestLongTaskEntryForTest(fakeLongTaskEntry(120, 500));
  expect(consoleLogs.length).toBe(0);
});

test("long-task debug logs fire when tankDebug includes long-tasks", () => {
  fakeStorage.tankDebug = "session-list,long-tasks";
  expect(isLongTaskDebugEnabled()).toBe(true);
  ingestLongTaskEntryForTest(fakeLongTaskEntry(120, 500));
  expect(consoleLogs.length).toBe(1);
  expect(consoleLogs[0]?.[0]).toBe("[tank/long-tasks] long-task");
});

test("entries below the 50ms threshold are dropped", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  ingestLongTaskEntryForTest(fakeLongTaskEntry(40, 500));
  flushLongTaskMetricsForTest();
  expect(fetchCalls.length).toBe(0);
});

test("entries are batched to the Prometheus ingestion endpoint", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");
  ingestLongTaskEntryForTest(fakeLongTaskEntry(180, 600, "self"));
  flushLongTaskMetricsForTest();

  expect(fetchCalls.length).toBe(1);
  expect(fetchCalls[0]?.input).toBe("/api/client-metrics/long-tasks");
  expect(fetchCalls[0]?.init?.method).toBe("POST");
  expect(new Headers(fetchCalls[0]?.init?.headers).get("Authorization")).toBe("Bearer token-123");
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  expect(event.durationMs).toBe(180);
  expect(event.startMs).toBe(600);
  expect(event.sessionMode).toBe("claude_gui");
  expect(event.attribution).toBe("self");
  expect(event.pagePath).toBe("/sessions/188");
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
  expect(event.sinceTankEventMs).toBe(100);
  expect(event.sinceSessionSwitchMs).toBe(50);
  expect(event.sinceScrollMs).toBe(20);
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
  expect(event.sinceTankEventMs).toBe(null);
  expect(event.sinceSessionSwitchMs).toBe(null);
  expect(event.sinceScrollMs).toBe(null);
});

test("batched entries flush when they hit the batch cap without waiting for the timer", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  setActiveSessionMode("claude_gui");
  for (let i = 0; i < 40; i += 1) {
    ingestLongTaskEntryForTest(fakeLongTaskEntry(60, 100 + i));
  }
  // No explicit flush call; the 40th entry should have tripped the cap.
  expect(fetchCalls.length).toBe(1);
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  expect(payload.events.length).toBe(40);
});
