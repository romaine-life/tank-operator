import { test, expect } from "vitest";

import {
  cachedTurnActivityRefreshRequests,
  isAlwaysVisibleTurnDetailEntry,
} from "./turnActivityCache.ts";
import type { TranscriptEntry } from "./App.tsx";

function row(partial: Partial<TranscriptEntry> & { id: string }): TranscriptEntry {
  return partial as TranscriptEntry;
}

test("requests a live Turns refresh when a cached turn receives a shell update", () => {
  const requests = cachedTurnActivityRefreshRequests(
    { "turn-1": [] },
    [
      row({
        id: "turn-activity-turn-1",
        kind: "turn_activity",
        turnId: "turn-1",
        orderKey: "001",
        activity: { turnId: "turn-1", endOrderKey: "009" },
      }),
    ],
  );

  expect(Array.from(requests.entries())).toEqual([["turn-1", "009"]]);
});

test("does not seed uncached turns from the live stream", () => {
  const requests = cachedTurnActivityRefreshRequests(
    {},
    [
      row({
        id: "turn-activity-turn-1",
        kind: "turn_activity",
        turnId: "turn-1",
        orderKey: "001",
        activity: { turnId: "turn-1", endOrderKey: "009" },
      }),
    ],
  );

  expect(requests.size).toBe(0);
});

test("coalesces multiple streamed rows for the same cached turn to the newest cursor", () => {
  const requests = cachedTurnActivityRefreshRequests(
    { "turn-1": [row({ id: "tool-1", kind: "tool", turnId: "turn-1" })] },
    [
      row({ id: "tool-1", kind: "tool", turnId: "turn-1", orderKey: "004" }),
      row({
        id: "turn-activity-turn-1",
        kind: "turn_activity",
        turnId: "turn-1",
        orderKey: "001",
        activity: { turnId: "turn-1", endOrderKey: "006" },
      }),
    ],
  );

  expect(Array.from(requests.entries())).toEqual([["turn-1", "006"]]);
});

test("ignores durable user messages because they are not part of the Turns activity body", () => {
  const requests = cachedTurnActivityRefreshRequests(
    { "turn-1": [] },
    [
      row({
        id: "turn-1:user",
        kind: "message",
        role: "user",
        turnId: "turn-1",
        orderKey: "001",
      }),
    ],
  );

  expect(requests.size).toBe(0);
});

test("refreshes cached turn activity for turn-only background wake prompts", () => {
  const requests = cachedTurnActivityRefreshRequests(
    { "turn-1": [] },
    [
      row({
        id: "turn-1:wake-prompt",
        kind: "message",
        role: "user",
        authorKind: "system",
        turnId: "turn-1",
        turnOnly: true,
        wakePrompt: true,
        orderKey: "011",
      }),
    ],
  );

  expect(Array.from(requests.entries())).toEqual([["turn-1", "011"]]);
});

test("keeps the system-user background wake prompt visible when activity is collapsed", () => {
  const wakePrompt = row({
    id: "turn-1:wake-prompt",
    kind: "message",
    role: "user",
    authorKind: "system",
    turnId: "turn-1",
    turnOnly: true,
    wakePrompt: true,
  });
  // No final ids: the wake prompt must still survive the collapse filter, so it
  // is not buried with ordinary tool noise on a completed continuation turn.
  expect(isAlwaysVisibleTurnDetailEntry(wakePrompt, new Set())).toBe(true);
});

test("keeps the final assistant answer visible and collapses ordinary activity", () => {
  const finalAnswer = row({
    id: "turn-1:item:final",
    kind: "message",
    role: "assistant",
    turnId: "turn-1",
  });
  const toolCall = row({ id: "turn-1:item:tool", kind: "tool", turnId: "turn-1" });
  const plainUser = row({
    id: "turn-1:user",
    kind: "message",
    role: "user",
    turnId: "turn-1",
  });
  const finalIds = new Set([finalAnswer.id]);
  expect(isAlwaysVisibleTurnDetailEntry(finalAnswer, finalIds)).toBe(true);
  expect(isAlwaysVisibleTurnDetailEntry(toolCall, finalIds)).toBe(false);
  expect(isAlwaysVisibleTurnDetailEntry(plainUser, finalIds)).toBe(false);
});

test("treats an empty cached activity body as loaded", () => {
  const requests = cachedTurnActivityRefreshRequests(
    { "turn-1": [] },
    [
      row({
        id: "turn-1:item:assistant",
        kind: "message",
        role: "assistant",
        turnId: "turn-1",
        orderKey: "010",
      }),
    ],
  );

  expect(Array.from(requests.entries())).toEqual([["turn-1", "010"]]);
});
