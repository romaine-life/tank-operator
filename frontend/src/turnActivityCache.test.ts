import assert from "node:assert/strict";
import test from "node:test";

import { cachedTurnActivityRefreshRequests } from "./turnActivityCache.ts";
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

  assert.deepEqual(Array.from(requests.entries()), [["turn-1", "009"]]);
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

  assert.equal(requests.size, 0);
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

  assert.deepEqual(Array.from(requests.entries()), [["turn-1", "006"]]);
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

  assert.equal(requests.size, 0);
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

  assert.deepEqual(Array.from(requests.entries()), [["turn-1", "010"]]);
});
