import { afterEach, beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  readHomeSelectedRepos,
  writeHomeSelectedRepos,
} from "./homeRepos";

let fakeStorage: Record<string, string>;

beforeEach(() => {
  fakeStorage = {};
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
});

afterEach(() => {
  delete (globalThis as { localStorage?: unknown }).localStorage;
});

test("home repo defaults round-trip through localStorage", () => {
  writeHomeSelectedRepos([
    "  romaine-life/tank-operator  ",
    "NelsonG6/Tank-Operator",
    "romaine-life/infra-bootstrap",
    "romaine-life/mcp-tank-operator",
    "openai/codex",
    "example/fifth",
    "example/sixth",
  ]);

  assert.deepEqual(readHomeSelectedRepos(), [
    "romaine-life/tank-operator",
    "romaine-life/infra-bootstrap",
    "romaine-life/mcp-tank-operator",
    "openai/codex",
    "example/fifth",
  ]);
});

test("home repo defaults ignore malformed storage", () => {
  fakeStorage["tank.homeSelectedRepos"] = "not-json";
  assert.deepEqual(readHomeSelectedRepos(), []);
});
