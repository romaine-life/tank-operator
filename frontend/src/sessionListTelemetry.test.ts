import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";

import {
  logSessionListEvent,
  logSessionListSnapshot,
} from "./sessionListTelemetry";

// node:test runs without jsdom; the telemetry module needs
// `localStorage`. Stub it per-test so the no-op vs. enabled paths
// are deterministic regardless of node version.
let fakeStorage: Record<string, string>;
let consoleLogs: Array<[string, unknown[]]>;
let origConsoleLog: typeof console.log;

beforeEach(() => {
  fakeStorage = {};
  consoleLogs = [];

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
});

afterEach(() => {
  console.log = origConsoleLog;
  delete (globalThis as { localStorage?: unknown }).localStorage;
});

test("debug logs are no-ops when tankDebug is unset", () => {
  logSessionListEvent({ type: "session.row_update", session_id: "8" });
  logSessionListSnapshot({ tip: "42", sessionCount: 3, source: "initial" });
  assert.equal(consoleLogs.length, 0, "debug-gated logs must not fire without the flag");
});

test("debug logs fire when tankDebug includes session-list", () => {
  fakeStorage["tankDebug"] = "other-tag,session-list,more";
  logSessionListEvent({ type: "session.row_update", session_id: "8" });
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
