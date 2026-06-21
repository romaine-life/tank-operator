import { test, expect } from "vitest";

import {
  isActivityOnlyMainTranscriptEntry,
  isReasoningTranscriptEntry,
} from "./transcriptReasoningPlacement.ts";

// Promotion-only invariant (docs/features/transcript/contract.md): reasoning is
// Turn-activity material and must never settle in the main transcript. This gate
// is the frontend's single home for that classification; the main-transcript
// grouper consults it so a reasoning row is dropped from settled groups while it
// still renders inside Turn activity and the Turns view.

test("reasoning is activity-only material — never a settled main-transcript row", () => {
  expect(isReasoningTranscriptEntry({ kind: "reasoning" })).toBe(true);
  expect(isActivityOnlyMainTranscriptEntry({ kind: "reasoning" })).toBe(true);
});

test("settled conversation kinds are not activity-only", () => {
  for (const kind of [
    "message",
    "meta",
    "tool",
    "turn_activity",
    "background_task",
  ]) {
    expect(isActivityOnlyMainTranscriptEntry({ kind })).toBe(false);
  }
  expect(isReasoningTranscriptEntry({ kind: "message" })).toBe(false);
});

test("nullish / kindless entries are not activity-only (defensive)", () => {
  expect(isActivityOnlyMainTranscriptEntry(null)).toBe(false);
  expect(isActivityOnlyMainTranscriptEntry(undefined)).toBe(false);
  expect(isActivityOnlyMainTranscriptEntry({})).toBe(false);
});
