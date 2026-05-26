import assert from "node:assert/strict";
import test from "node:test";

import { isTurnActivityActive } from "./turnActivityState.ts";

test("active turn id is authoritative when present", () => {
  assert.equal(isTurnActivityActive("turn-2", "turn-2"), true);
  assert.equal(isTurnActivityActive("turn-1", "turn-2"), false);
});

test("stale active transcript shell cannot mark a turn active without an active turn id", () => {
  assert.equal(isTurnActivityActive("turn-2", null), false);
});

test("blank ids are never active", () => {
  assert.equal(isTurnActivityActive("", "turn-2"), false);
  assert.equal(isTurnActivityActive("turn-2", ""), false);
});
