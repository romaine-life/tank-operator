import assert from "node:assert/strict";
import { test } from "node:test";

import {
  compactCompletedTurnEntries,
  type CompactableTranscriptEntry,
} from "./turnCompaction.ts";

type Entry = CompactableTranscriptEntry & {
  kind: "message" | "tool" | "reasoning" | "meta" | "background_task";
  text?: string;
};

function entry(
  id: string,
  kind: Entry["kind"],
  fields: Partial<Entry> = {},
): Entry {
  return {
    id,
    kind,
    turnId: "turn-1",
    ...fields,
  };
}

test("leaves active turns expanded", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user" }),
    entry("tool", "tool"),
    entry("final", "message", { role: "assistant" }),
  ], true);

  assert.deepEqual(groups.map((group) => group.kind), ["entry", "entry", "entry"]);
});

test("groups active turn activity before a final-answer candidate exists", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user" }),
    entry("tool", "tool"),
  ], true, "turn-1");

  assert.deepEqual(
    groups.map((group) =>
      group.kind === "activity"
        ? ["activity", group.active, group.entries.map((activityEntry) => activityEntry.id)]
        : ["entry", group.entry.id],
    ),
    [
      ["entry", "user"],
      ["activity", true, ["tool"]],
    ],
  );
});

test("keeps trailing active assistant text in activity as provisional output", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user" }),
    entry("tool", "tool"),
    entry("final", "message", { role: "assistant" }),
  ], true, "turn-1");

  assert.deepEqual(
    groups.map((group) =>
      group.kind === "activity"
        ? [
            "activity",
            group.entries.map((activityEntry) => activityEntry.id),
            group.compactedEntryIds,
          ]
        : ["entry", group.entry.id],
    ),
    [
      ["entry", "user"],
      ["activity", ["tool", "final"], ["tool", "final"]],
    ],
  );
});

test("does not move active assistant text from transcript into activity when later work arrives", () => {
  const beforeLaterWork = compactCompletedTurnEntries([
    entry("user", "message", { role: "user" }),
    entry("note", "message", { role: "assistant" }),
  ], true, "turn-1");
  const afterLaterWork = compactCompletedTurnEntries([
    entry("user", "message", { role: "user" }),
    entry("note", "message", { role: "assistant" }),
    entry("tool", "tool"),
  ], true, "turn-1");

  assert.deepEqual(
    beforeLaterWork.map((group) =>
      group.kind === "activity"
        ? ["activity", group.entries.map((activityEntry) => activityEntry.id)]
        : ["entry", group.entry.id],
    ),
    [
      ["entry", "user"],
      ["activity", ["note"]],
    ],
  );
  assert.deepEqual(
    afterLaterWork.map((group) =>
      group.kind === "activity"
        ? ["activity", group.entries.map((activityEntry) => activityEntry.id)]
        : ["entry", group.entry.id],
    ),
    [
      ["entry", "user"],
      ["activity", ["note", "tool"]],
    ],
  );
});

test("duplicates completed final assistant answer into activity without compacting the settled row", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "completed" }),
    entry("note", "message", { role: "assistant", turnTerminalStatus: "completed" }),
    entry("tool", "tool", { turnTerminalStatus: "completed" }),
    entry("final", "message", { role: "assistant", turnTerminalStatus: "completed" }),
  ], true);

  assert.deepEqual(
    groups.map((group) =>
      group.kind === "activity"
        ? [
            "activity",
            group.entries.map((activityEntry) => activityEntry.id),
            group.compactedEntryIds,
          ]
        : ["entry", group.entry.id],
    ),
    [
      ["entry", "user"],
      ["activity", ["note", "tool", "final"], ["note", "tool"]],
      ["entry", "final"],
    ],
  );
});

test("keeps trailing assistant response blocks visible together", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "completed" }),
    entry("tool", "tool", { turnTerminalStatus: "completed" }),
    entry("final-a", "message", { role: "assistant", turnTerminalStatus: "completed" }),
    entry("final-b", "message", { role: "assistant", turnTerminalStatus: "completed" }),
  ], true);

  assert.deepEqual(
    groups.map((group) => (group.kind === "activity" ? "activity" : group.entry.id)),
    ["user", "activity", "final-a", "final-b"],
  );
  const activity = groups.find((group) => group.kind === "activity");
  assert.equal(activity?.kind, "activity");
  if (activity?.kind === "activity") {
    assert.deepEqual(
      activity.entries.map((activityEntry) => activityEntry.id),
      ["tool", "final-a", "final-b"],
    );
    assert.deepEqual(activity.compactedEntryIds, ["tool"]);
  }
});

test("folds background task rows into completed turn activity", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "completed" }),
    entry("task", "background_task", { turnTerminalStatus: "completed" }),
    entry("final", "message", { role: "assistant", turnTerminalStatus: "completed" }),
  ], true);

  const activity = groups.find((group) => group.kind === "activity");
  assert.equal(activity?.kind, "activity");
  if (activity?.kind === "activity") {
    assert.deepEqual(activity.entries.map((activityEntry) => activityEntry.id), ["task", "final"]);
    assert.deepEqual(activity.compactedEntryIds, ["task"]);
  }
});

test("does not create an activity row for completed turns with only a final answer", () => {
  const groups = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "completed" }),
    entry("final", "message", { role: "assistant", turnTerminalStatus: "completed" }),
  ], true);

  assert.deepEqual(groups.map((group) => group.kind), ["entry", "entry"]);
});

test("does not fold failed turns or turns without final assistant text", () => {
  const failed = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "failed" }),
    entry("tool", "tool", { turnTerminalStatus: "failed" }),
  ], true);
  const toolOnlyCompleted = compactCompletedTurnEntries([
    entry("user", "message", { role: "user", turnTerminalStatus: "completed" }),
    entry("tool", "tool", { turnTerminalStatus: "completed" }),
  ], true);

  assert.deepEqual(failed.map((group) => group.kind), ["entry", "entry"]);
  assert.deepEqual(toolOnlyCompleted.map((group) => group.kind), ["entry", "entry"]);
});
