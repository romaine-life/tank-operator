import { afterEach, beforeEach, test } from "node:test";
import assert from "node:assert/strict";

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
  assert.equal(isChatScrollDebugEnabled(), false);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  assert.equal(consoleLogs.length, 0);
  assert.equal(fakeStorage["tank.chatScrollEvents"], undefined);
});

test("chat scroll debug logs fire when tankDebug includes chat-scroll", () => {
  fakeStorage.tankDebug = "session-list,chat-scroll";
  assert.equal(isChatScrollDebugEnabled(), true);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  assert.equal(consoleLogs.length, 1);
  assert.equal(consoleLogs[0]?.[0], "[tank/chat-scroll] timeline-loaded");
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
    source: "keyboard",
    anchor: "oldest",
    key: "Home",
    targetEdge: "oldest",
    atBottom: false,
    bottomDistance: 240,
  });
  flushChatScrollMetricsForTest();

  assert.equal(fetchCalls.length, 1);
  assert.equal(fetchCalls[0]?.input, "/api/client-metrics/chat-scroll");
  assert.equal(fetchCalls[0]?.init?.method, "POST");
  assert.equal(new Headers(fetchCalls[0]?.init?.headers).get("Authorization"), "Bearer token-123");
  const payload = JSON.parse(String(fetchCalls[0]?.init?.body)) as {
    events: Array<Record<string, unknown>>;
  };
  const event = payload.events[0]!;
  assert.equal(event.event, "at-bottom-change");
  assert.equal(event.sessionMode, "codex_gui");
  assert.equal(event.sessionId, "101");
  assert.equal(event.pagePath, "/sessions/101");
  assert.equal(event.pageSearch, "?session=101");
  assert.equal(event.source, "keyboard");
  assert.equal(event.anchor, "oldest");
  assert.equal(event.key, "Home");
  assert.equal(event.targetEdge, "oldest");
});
