import { afterEach, beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  isChatScrollDebugEnabled,
  logChatScrollEvent,
} from "./chatScrollTelemetry";

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

test("chat scroll debug logs are off by default", () => {
  assert.equal(isChatScrollDebugEnabled(), false);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  assert.equal(consoleLogs.length, 0);
});

test("chat scroll debug logs fire when tankDebug includes chat-scroll", () => {
  fakeStorage.tankDebug = "session-list,chat-scroll";
  assert.equal(isChatScrollDebugEnabled(), true);
  logChatScrollEvent("timeline-loaded", { sessionId: "101" });
  assert.equal(consoleLogs.length, 1);
  assert.equal(consoleLogs[0]?.[0], "[tank/chat-scroll] timeline-loaded");
});
