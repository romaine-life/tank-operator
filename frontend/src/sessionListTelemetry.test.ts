import { test, beforeEach, afterEach, expect } from "vitest";
import {
  logSessionListEvent,
  logSessionListSnapshot,
} from "./sessionListTelemetry";

// This pure-logic test runs in the Vitest `node` environment (no jsdom), but
// the telemetry module needs `localStorage`. Stub it per-test so the no-op vs.
// enabled paths are deterministic regardless of node version.
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
  expect(consoleLogs.length, "debug-gated logs must not fire without the flag").toBe(0);
});

test("debug logs fire when tankDebug includes session-list", () => {
  fakeStorage["tankDebug"] = "other-tag,session-list,more";
  logSessionListEvent({ type: "session.row_update", session_id: "8" });
  logSessionListSnapshot({ tip: "42", sessionCount: 3, source: "initial" });
  expect(consoleLogs.some(([msg]) => msg.includes("event")), "event log must fire when flag is enabled").toBeTruthy();
  expect(consoleLogs.some(([msg]) => msg.includes("snapshot")), "snapshot log must fire when flag is enabled").toBeTruthy();
});
