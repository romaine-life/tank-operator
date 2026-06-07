import { test, expect } from "vitest";

import {
  turnActivityGroupIsActive,
  turnActivityShellIsDurablyActive,
} from "./turnActivityState.ts";

test("durable active turn activity shell remains active without client active turn id", () => {
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "active", active: true },
          "turn-1",
          null,
        )).toBe(true);
});

test("client active turn id keeps locally-compacted active activity active", () => {
  expect(turnActivityGroupIsActive(undefined, "turn-1", "turn-1")).toBe(true);
});

test("completed turn activity shell is not active without a matching active turn", () => {
  expect(turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "completed", active: false })).toBe(false);
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "completed", active: false },
          "turn-1",
          null,
        )).toBe(false);
});

test("needs-input turn activity shell is a handoff, not active-running UI", () => {
  expect(turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "needs_input", active: true })).toBe(false);
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "needs_input", active: true },
          "turn-1",
          "turn-1",
        )).toBe(false);
});
