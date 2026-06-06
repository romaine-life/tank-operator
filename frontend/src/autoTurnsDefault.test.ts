import assert from "node:assert/strict";
import { test } from "node:test";

import {
  AUTO_TURNS_USER_MESSAGE_THRESHOLD,
  shouldAutoDefaultToTurns,
} from "./autoTurnsDefault.ts";

// Regression guard: the threshold is the one tunable knob for this behavior.
// If a future change moves it, this test should be the deliberate place that
// records the new value.
test("threshold is pinned at 8 user back-and-forths", () => {
  assert.equal(AUTO_TURNS_USER_MESSAGE_THRESHOLD, 8);
});

test("stays on the main transcript below the threshold", () => {
  assert.equal(shouldAutoDefaultToTurns(0), false);
  assert.equal(shouldAutoDefaultToTurns(1), false);
  assert.equal(shouldAutoDefaultToTurns(7), false);
});

test("defaults to Turns at and above the threshold", () => {
  assert.equal(shouldAutoDefaultToTurns(8), true);
  assert.equal(shouldAutoDefaultToTurns(9), true);
  assert.equal(shouldAutoDefaultToTurns(40), true);
});

test("treats missing / non-finite / negative counts as not-yet-substantial", () => {
  assert.equal(shouldAutoDefaultToTurns(undefined), false);
  assert.equal(shouldAutoDefaultToTurns(null), false);
  assert.equal(shouldAutoDefaultToTurns(Number.NaN), false);
  assert.equal(shouldAutoDefaultToTurns(Number.POSITIVE_INFINITY), false);
  assert.equal(shouldAutoDefaultToTurns(-3), false);
});
