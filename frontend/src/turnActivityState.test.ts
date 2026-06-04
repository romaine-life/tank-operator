import assert from "node:assert/strict";
import { test } from "node:test";

import {
  turnActivityGroupIsActive,
  turnActivityShellIsDurablyActive,
} from "./turnActivityState.ts";

test("durable active turn activity shell remains active without client active turn id", () => {
  assert.equal(
    turnActivityGroupIsActive(
      { turnId: "turn-1", status: "active", active: true },
      "turn-1",
      null,
    ),
    true,
  );
});

test("client active turn id keeps locally-compacted active activity active", () => {
  assert.equal(turnActivityGroupIsActive(undefined, "turn-1", "turn-1"), true);
});

test("completed turn activity shell is not active without a matching active turn", () => {
  assert.equal(
    turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "completed", active: false }),
    false,
  );
  assert.equal(
    turnActivityGroupIsActive(
      { turnId: "turn-1", status: "completed", active: false },
      "turn-1",
      null,
    ),
    false,
  );
});

test("needs-input turn activity shell is a handoff, not active-running UI", () => {
  assert.equal(
    turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "needs_input", active: true }),
    false,
  );
  assert.equal(
    turnActivityGroupIsActive(
      { turnId: "turn-1", status: "needs_input", active: true },
      "turn-1",
      "turn-1",
    ),
    false,
  );
});
