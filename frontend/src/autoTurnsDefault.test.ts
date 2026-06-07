import { test, expect } from "vitest";

import {
  AUTO_TURNS_USER_MESSAGE_THRESHOLD,
  shouldAutoDefaultToTurns,
} from "./autoTurnsDefault.ts";

// Regression guard: the threshold is the one tunable knob for this behavior.
// If a future change moves it, this test should be the deliberate place that
// records the new value.
test("threshold is pinned at 8 user back-and-forths", () => {
  expect(AUTO_TURNS_USER_MESSAGE_THRESHOLD).toBe(8);
});

test("stays on the main transcript below the threshold", () => {
  expect(shouldAutoDefaultToTurns(0)).toBe(false);
  expect(shouldAutoDefaultToTurns(1)).toBe(false);
  expect(shouldAutoDefaultToTurns(7)).toBe(false);
});

test("defaults to Turns at and above the threshold", () => {
  expect(shouldAutoDefaultToTurns(8)).toBe(true);
  expect(shouldAutoDefaultToTurns(9)).toBe(true);
  expect(shouldAutoDefaultToTurns(40)).toBe(true);
});

test("treats missing / non-finite / negative counts as not-yet-substantial", () => {
  expect(shouldAutoDefaultToTurns(undefined)).toBe(false);
  expect(shouldAutoDefaultToTurns(null)).toBe(false);
  expect(shouldAutoDefaultToTurns(Number.NaN)).toBe(false);
  expect(shouldAutoDefaultToTurns(Number.POSITIVE_INFINITY)).toBe(false);
  expect(shouldAutoDefaultToTurns(-3)).toBe(false);
});
