import assert from "node:assert/strict";
import test from "node:test";

import {
  resolveThinkingInsertIndex,
  type ThinkingPlacementGroup,
} from "./transcriptThinkingPlacement.ts";

function group(orderKey: string, includesTurn: boolean): ThinkingPlacementGroup {
  return { orderKey, includesTurn };
}

// Regression for the new-session first-turn bug: "Session is loading." /
// "Session is ready." are durable session.status rows with no turnId. The
// running placeholder must sort BELOW them (their order keys precede the
// turn's activity), not above them just because the user message is the only
// turn-tagged row.
test("placeholder sorts below untagged session.status notices on a new session's first turn", () => {
  const groups = [
    group("001", true), // user message (turn T)
    group("002", false), // "Session is loading."
    group("003", false), // "Session is ready."
  ];
  // First activity row arrives after "ready", so the shell's live tail is 004.
  const index = resolveThinkingInsertIndex(groups, "004", 3);
  assert.equal(index, 3);
});

// #732 invariant preserved: an AskUserQuestion awaiting-input payload stays
// anchored to a later order key (the asking turn's key plus a
// `~awaiting_input` suffix). The placeholder must not overtake it.
test("placeholder sorts below a later AskUserQuestion awaiting-input payload for the same turn", () => {
  const groups = [
    group("001", true), // user message (turn T)
    group("005~awaiting_input", true), // awaiting-input payload (turn T)
  ];
  // Shell's compacted tail is the tool's raw key 005; the standalone handoff
  // sorts after it via the suffix, so the placeholder must land after both.
  const index = resolveThinkingInsertIndex(groups, "005", 2);
  assert.equal(index, 2);
});

test("placeholder lands after the user message on a plain mid-turn with no other rows", () => {
  const groups = [group("001", true)];
  const index = resolveThinkingInsertIndex(groups, "004", 1);
  assert.equal(index, 1);
});

// The live tail respects order keys in both directions: an untagged row with a
// HIGHER key than the turn's tail stays below the placeholder.
test("placeholder does not jump below an untagged row whose order key exceeds the tail", () => {
  const groups = [
    group("001", true), // user message (turn T)
    group("003", false), // some later untagged notice
  ];
  // Shell tail is 002 — between the two rows.
  const index = resolveThinkingInsertIndex(groups, "002", 2);
  assert.equal(index, 1);
});

test("multiple turn-tagged rows union into the live tail", () => {
  const groups = [
    group("001", true), // user message (turn T)
    group("002", false), // untagged notice
    group("006", true), // a later turn-tagged row
  ];
  // Shell tail (004) is older than the turn-tagged row at 006; tail unions to 006.
  const index = resolveThinkingInsertIndex(groups, "004", 1);
  assert.equal(index, 3);
});

// Keyless fallback: when no durable order key exists anywhere (local-only
// optimistic rows the server has not confirmed), anchor to the latest
// turn-tagged group rather than the shell's stream position.
test("keyless rows fall back to the latest turn-tagged group", () => {
  const groups = [
    group("", true), // user message, not yet confirmed (no order key)
    group("", false),
  ];
  const index = resolveThinkingInsertIndex(groups, "", 5);
  assert.equal(index, 1);
});

// Keyless and no turn-tagged group: use the clamped shell stream position.
test("keyless rows with no turn-tagged group use the clamped fallback index", () => {
  const groups = [group("", false)];
  assert.equal(resolveThinkingInsertIndex(groups, "", 3), 1); // clamped to length
  assert.equal(resolveThinkingInsertIndex([], "", 0), 0);
});

// When a durable tail exists but precedes every group's key (degenerate), fall
// back rather than splicing at the very front.
test("a tail older than every group key falls back to the clamped index", () => {
  const groups = [group("010", true), group("011", false)];
  const index = resolveThinkingInsertIndex(groups, "005", 1);
  assert.equal(index, 1);
});
