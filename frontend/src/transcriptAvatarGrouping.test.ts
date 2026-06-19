import { expect, test } from "vitest";

import { isTranscriptMessageAvatarContinuation } from "./transcriptAvatarGrouping";

function message(fields: Record<string, unknown> = {}) {
  return {
    kind: "message",
    role: "assistant",
    time: "2026-06-15T08:00:00.000Z",
    ...fields,
  };
}

test("tool groups are transparent for assistant avatar continuation", () => {
  const groups = [
    { kind: "message", entry: message({ id: "before" }) },
    { kind: "tools", entries: [{ kind: "tool", id: "tool-1" }] },
    {
      kind: "message",
      entry: message({
        id: "after",
        time: "2026-06-15T08:01:00.000Z",
      }),
    },
  ];

  expect(isTranscriptMessageAvatarContinuation(groups, 2)).toBe(true);
});

test("turn activity shells are transparent for assistant avatar continuation", () => {
  const groups = [
    { kind: "message", entry: message({ id: "before" }) },
    { kind: "activity", id: "turn-activity-turn-1", turnId: "turn-1" },
    {
      kind: "message",
      entry: message({
        id: "after",
        time: "2026-06-15T08:01:00.000Z",
      }),
    },
  ];

  expect(isTranscriptMessageAvatarContinuation(groups, 2)).toBe(true);
});

test("system message groups remain an avatar boundary", () => {
  const groups = [
    { kind: "message", entry: message({ id: "before" }) },
    {
      kind: "message_group",
      entries: [
        message({
          id: "system",
          role: "system",
          text: "Session is ready.",
        }),
      ],
    },
    {
      kind: "message",
      entry: message({
        id: "after",
        time: "2026-06-15T08:01:00.000Z",
      }),
    },
  ];

  expect(isTranscriptMessageAvatarContinuation(groups, 2)).toBe(false);
});

test("meta rows remain an avatar boundary", () => {
  const groups = [
    { kind: "message", entry: message({ id: "before" }) },
    { kind: "meta", entry: { kind: "meta", id: "failed" } },
    {
      kind: "message",
      entry: message({
        id: "after",
        time: "2026-06-15T08:01:00.000Z",
      }),
    },
  ];

  expect(isTranscriptMessageAvatarContinuation(groups, 2)).toBe(false);
});
