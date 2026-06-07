import { test, expect } from "vitest";

import {
  entryMessageFingerprint,
  mergeProjectedTranscriptRowUpdates,
  mergeSdkTranscript,
  pruneRealtimeEntries,
} from "./transcriptMerge.ts";
import type { TranscriptEntry } from "./App.tsx";

// The optimistic skill card the SPA appends locally the instant the user
// clicks a skill button (e.g. the "test" flask). text is the action label;
// messageKind/skillName mark it as a skill action; localOnly + clientNonce tie
// it to the in-flight run.
function optimisticSkillCard(skill: string, nonce: string): TranscriptEntry {
  return {
    id: `skill-action-${skill}-1`,
    kind: "message",
    role: "user",
    text: `${skill.charAt(0).toUpperCase()}${skill.slice(1)} skill`,
    time: "2026-05-29T18:17:00.000Z",
    messageKind: "skill-action",
    skillName: skill,
    transcriptSource: "realtime",
    localOnly: true,
    clientNonce: nonce,
  } as TranscriptEntry;
}

// The durable projection the server emits for the same submission: text is the
// raw "/test ..." prompt, and the skill identity rides on display.skill_name.
function durableSkillMessage(skill: string, nonce: string): TranscriptEntry {
  return {
    id: `turn-${nonce}:user`,
    kind: "message",
    role: "user",
    text: `/${skill}\n\nInitial message type: make code changes and immediately run the test skill for this.`,
    time: "2026-05-29T18:17:00.500Z",
    transcriptSource: "server",
    clientNonce: nonce,
    display: { kind: "skill_invocation", skill_name: skill },
  } as TranscriptEntry;
}

test("optimistic skill card and durable projection share a fingerprint", () => {
  const optimistic = optimisticSkillCard("test", "run-1");
  const durable = durableSkillMessage("test", "run-1");
  expect(entryMessageFingerprint(optimistic), "the local card must collapse onto the durable row once it lands").toBe(entryMessageFingerprint(durable));
});

test("the optimistic skill card is pruned once the durable event arrives", () => {
  // Regression: clicking the test skill button used to render the card twice
  // because the optimistic card's text ("Test skill") never matched the
  // durable row's raw "/test ..." text, so it was never pruned.
  const server = [durableSkillMessage("test", "run-1")];
  const realtime = [optimisticSkillCard("test", "run-1")];

  const pruned = pruneRealtimeEntries(server, realtime);
  expect(pruned.length, "optimistic skill card must be dropped").toBe(0);

  const merged = mergeSdkTranscript(server, realtime);
  const skillRows = merged.filter(
    (e) =>
      (e as Record<string, unknown>).messageKind === "skill-action" ||
      e.display?.kind === "skill_invocation",
  );
  expect(skillRows.length, "exactly one skill card should render").toBe(1);
});

test("distinct invocations of the same skill do not cross-prune", () => {
  // A second /test invocation lands while the first is already a durable
  // server row. The second optimistic card carries a different client nonce,
  // so it must survive until its own durable event arrives.
  const server = [durableSkillMessage("test", "run-1")];
  const realtime = [optimisticSkillCard("test", "run-2")];

  const pruned = pruneRealtimeEntries(server, realtime);
  expect(pruned.length, "the second invocation must not be dropped").toBe(1);
  expect(pruned[0].clientNonce).toBe("run-2");
});

test("plain user message dedup is unchanged", () => {
  const server: TranscriptEntry[] = [
    {
      id: "turn-9:user",
      kind: "message",
      role: "user",
      text: "hello world",
      time: "2026-05-29T18:00:00.000Z",
      transcriptSource: "server",
      clientNonce: "run-9",
    } as TranscriptEntry,
  ];
  const realtime: TranscriptEntry[] = [
    {
      id: "local-9",
      kind: "message",
      role: "user",
      text: "hello world",
      time: "2026-05-29T18:00:00.000Z",
      transcriptSource: "realtime",
      localOnly: true,
      clientNonce: "run-9",
    } as TranscriptEntry,
  ];
  expect(pruneRealtimeEntries(server, realtime).length).toBe(0);
});

test("projected transcript live updates append after a non-tail window", () => {
  const historicalWindow: TranscriptEntry[] = [
    {
      id: "turn-1:user",
      kind: "message",
      role: "user",
      text: "older question",
      orderKey: "0001:user",
      time: "2026-06-02T18:00:00.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];
  const liveRows: TranscriptEntry[] = [
    {
      id: "turn-9:assistant",
      kind: "message",
      role: "assistant",
      text: "live answer",
      orderKey: "0009:assistant",
      time: "2026-06-02T18:10:00.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];

  const merged = mergeProjectedTranscriptRowUpdates(historicalWindow, liveRows);

  expect(merged.map((entry) => entry.id), "post-cursor SSE rows must render even when the bootstrapped window was not found_newest").toEqual(["turn-1:user", "turn-9:assistant"]);
});

test("projected transcript compaction shell replaces compacted rows", () => {
  const current: TranscriptEntry[] = [
    {
      id: "turn-7:tool:1",
      kind: "meta",
      orderKey: "0007:tool:1",
      turnId: "turn-7",
      meta: { title: "Tool call", detail: "first tick" },
      time: "2026-06-02T18:20:00.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
    {
      id: "turn-7:tool:2",
      kind: "meta",
      orderKey: "0007:tool:2",
      turnId: "turn-7",
      meta: { title: "Tool call", detail: "second tick" },
      time: "2026-06-02T18:20:01.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];
  const updates: TranscriptEntry[] = [
    {
      id: "turn-7:activity",
      kind: "turn_activity",
      orderKey: "0007:activity",
      turnId: "turn-7",
      activityIds: ["turn-7:tool:1", "turn-7:tool:2"],
      activity: {
        turnId: "turn-7",
        status: "active",
        active: true,
        startOrderKey: "0007:start",
        compactedEntryIds: ["turn-7:tool:1", "turn-7:tool:2"],
      },
      time: "2026-06-02T18:20:02.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];

  const merged = mergeProjectedTranscriptRowUpdates(current, updates);

  expect(merged.map((entry) => entry.id)).toEqual(["turn-7:activity"]);
});

test("projected transcript terminal rows remove stale active shells", () => {
  const current: TranscriptEntry[] = [
    {
      id: "turn-8:activity",
      kind: "turn_activity",
      orderKey: "0008:activity",
      turnId: "turn-8",
      activity: {
        turnId: "turn-8",
        status: "active",
        active: true,
        startOrderKey: "0008:start",
      },
      time: "2026-06-02T18:25:00.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];
  const updates: TranscriptEntry[] = [
    {
      id: "turn-8:completed",
      kind: "meta",
      orderKey: "0008:completed",
      turnId: "turn-8",
      turnTerminalStatus: "completed",
      meta: { title: "Turn completed" },
      time: "2026-06-02T18:26:00.000Z",
      transcriptSource: "server",
    } as TranscriptEntry,
  ];

  const merged = mergeProjectedTranscriptRowUpdates(current, updates);

  expect(merged.map((entry) => entry.id)).toEqual(["turn-8:completed"]);
});
