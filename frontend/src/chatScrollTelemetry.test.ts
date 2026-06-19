import { afterEach, beforeEach, test, expect } from "vitest";
import {
  flushChatScrollMetricsForTest,
  isChatScrollDebugEnabled,
  logChatScrollEvent,
} from "./chatScrollTelemetry";

let fakeStorage: Record<string, string>;
let consoleLogs: Array<[string, unknown[]]>;
let origConsoleLog: typeof console.log;
let fetchCalls: Array<{ input: RequestInfo | URL; init?: RequestInit }>;
let origFetch: typeof fetch;

beforeEach(() => {
  fakeStorage = {};
  consoleLogs = [];
  fetchCalls = [];

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

  origConsoleLog = console.log;
  console.log = (message: string, ...args: unknown[]) => {
    consoleLogs.push([message, args]);
  };
  origFetch = fetch;
  (globalThis as { fetch: typeof fetch }).fetch = (async (input, init) => {
    fetchCalls.push({ input, init });
    return new Response("{}", { status: 202 });
  }) as typeof fetch;
});

afterEach(() => {
  console.log = origConsoleLog;
  (globalThis as { fetch: typeof fetch }).fetch = origFetch;
  delete (globalThis as { localStorage?: unknown }).localStorage;
  delete (globalThis as { window?: unknown }).window;
});

test("chat scroll debug logs are off by default", () => {
  expect(isChatScrollDebugEnabled()).toBe(false);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  expect(consoleLogs.length).toBe(0);
  expect(fakeStorage["tank.chatScrollEvents"]).toBe(undefined);
});

test("chat scroll debug logs fire when tankDebug includes chat-scroll", () => {
  fakeStorage.tankDebug = "session-list,chat-scroll";
  expect(isChatScrollDebugEnabled()).toBe(true);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  expect(consoleLogs.length).toBe(1);
  expect(consoleLogs[0]?.[0]).toBe("[tank/chat-scroll] timeline-loaded");
});

test("chat scroll metrics flush to the prometheus ingestion endpoint", () => {
  fakeStorage["auth-romaine-jwt"] = "token-123";
  const listeners: Record<string, EventListener> = {};
  (globalThis as { window?: unknown }).window = {
    location: { pathname: "/sessions/101", search: "?session=101" },
    setTimeout: () => 1,
    addEventListener: (event: string, listener: EventListener) => {
      listeners[event] = listener;
    },
  };
  logChatScrollEvent("at-bottom-change", {
    sessionMode: "codex_gui",
    sessionId: "101",
    routeSessionId: "202",
    selectedTurnId: "turn_abc",
    source: "keyboard",
    anchor: "oldest",
    key: "Home",
    targetEdge: "oldest",
    atBottom: false,
    bottomDistance: 240,
    thinkingGroups: 0,
    turnActivityShells: 1,
    durableActiveActivityGroups: 1,
    durableActiveTurnActivityShells: 1,
  });
  flushChatScrollMetricsForTest();

  expect(fetchCalls.length).toBe(1);
  expect(fetchCalls[0]?.input).toBe("/api/client-metrics/chat-scroll");
  expect(fetchCalls[0]?.init?.method).toBe("POST");
  expect(new Headers(fetchCalls[0]?.init?.headers).get("Authorization")).toBe("Bearer token-123");
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  expect(event.event).toBe("at-bottom-change");
  expect(event.sessionMode).toBe("codex_gui");
  expect(event.sessionId).toBe("101");
  expect(event.routeSessionId).toBe("202");
  expect(event.selectedTurnId).toBe("turn_abc");
  expect(event.pagePath).toBe("/sessions/101");
  expect(event.pageSearch).toBe("?session=101");
  expect(event.source).toBe("keyboard");
  expect(event.anchor).toBe("oldest");
  expect(event.key).toBe("Home");
  expect(event.targetEdge).toBe("oldest");
  expect(event.thinkingGroups).toBe(0);
  expect(event.turnActivityShells).toBe(1);
  expect(event.durableActiveActivityGroups).toBe(1);
  expect(event.durableActiveTurnActivityShells).toBe(1);
});
