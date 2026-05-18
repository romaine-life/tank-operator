import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";

import {
  logSessionListEvent,
  logSessionListSnapshot,
  notePlaceholderSynthesized,
} from "./sessionListTelemetry";
import type { SessionListEvent } from "./sessionListEvents";

// node:test runs without jsdom; the telemetry module needs
// `localStorage` and `fetch`. Stub them per-test so the no-op vs.
// enabled paths are deterministic regardless of node version.
let fakeStorage: Record<string, string>;
let fetchCalls: Array<{ url: string; body: unknown }>;
let consoleLogs: Array<[string, unknown[]]>;
let consoleWarns: Array<[string, unknown[]]>;
let origConsoleLog: typeof console.log;
let origConsoleWarn: typeof console.warn;

beforeEach(() => {
  fakeStorage = {};
  fetchCalls = [];
  consoleLogs = [];
  consoleWarns = [];

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

  (globalThis as { fetch?: unknown }).fetch = async (
    input: unknown,
    init?: { body?: unknown },
  ) => {
    fetchCalls.push({
      url: typeof input === "string" ? input : String(input),
      body: init?.body,
    });
    return new Response(null, { status: 204 });
  };

  origConsoleLog = console.log;
  origConsoleWarn = console.warn;
  console.log = (message: string, ...args: unknown[]) => {
    consoleLogs.push([message, args]);
  };
  console.warn = (message: string, ...args: unknown[]) => {
    consoleWarns.push([message, args]);
  };
});

afterEach(() => {
  console.log = origConsoleLog;
  console.warn = origConsoleWarn;
  delete (globalThis as { fetch?: unknown }).fetch;
  delete (globalThis as { localStorage?: unknown }).localStorage;
});

function makeEvent(): SessionListEvent {
  return {
    order_key: "42",
    email: "u@example.com",
    session_scope: "default",
    session_id: "8",
    type: "session.pod_terminating",
    event_id: "pod_terminating:uid:0",
    occurred_at: "2026-05-18T00:00:00Z",
    payload: { status: "Failed" },
  };
}

test("debug logs are no-ops when tankDebug is unset", () => {
  logSessionListEvent(makeEvent());
  logSessionListSnapshot({ tip: "42", sessionCount: 3, source: "initial" });
  assert.equal(consoleLogs.length, 0, "debug-gated logs must not fire without the flag");
});

test("debug logs fire when tankDebug includes session-list", () => {
  fakeStorage["tankDebug"] = "other-tag,session-list,more";
  logSessionListEvent(makeEvent());
  logSessionListSnapshot({ tip: "42", sessionCount: 3, source: "initial" });
  assert.ok(
    consoleLogs.some(([msg]) => msg.includes("event")),
    "event log must fire when flag is enabled",
  );
  assert.ok(
    consoleLogs.some(([msg]) => msg.includes("snapshot")),
    "snapshot log must fire when flag is enabled",
  );
});

test("notePlaceholderSynthesized always beacons regardless of debug flag", async () => {
  // No tankDebug flag set — beacon must still fire.
  notePlaceholderSynthesized(makeEvent());
  // Console warn always fires.
  assert.equal(consoleWarns.length, 1, "placeholder synthesis must always warn");
  // Best-effort fetch; resolve microtasks before assertion.
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(fetchCalls.length, 1, "placeholder beacon must POST to /api/debug/client-metric");
  assert.equal(fetchCalls[0].url, "/api/debug/client-metric");
  const body = JSON.parse(String(fetchCalls[0].body));
  assert.equal(
    body.name,
    "session_list.placeholder_synthesized",
    "beacon name must match the server-side allowlist entry",
  );
});

test("notePlaceholderSynthesized swallows fetch errors silently", async () => {
  (globalThis as { fetch?: unknown }).fetch = async () => {
    throw new Error("network down");
  };
  // Must not throw.
  notePlaceholderSynthesized(makeEvent());
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(consoleWarns.length, 1, "warn fires before the fetch attempt");
});
