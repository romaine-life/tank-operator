import assert from "node:assert/strict";
import { test } from "node:test";

import {
  entryMessageFingerprint,
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
  assert.equal(
    entryMessageFingerprint(optimistic),
    entryMessageFingerprint(durable),
    "the local card must collapse onto the durable row once it lands",
  );
});

test("the optimistic skill card is pruned once the durable event arrives", () => {
  // Regression: clicking the test skill button used to render the card twice
  // because the optimistic card's text ("Test skill") never matched the
  // durable row's raw "/test ..." text, so it was never pruned.
  const server = [durableSkillMessage("test", "run-1")];
  const realtime = [optimisticSkillCard("test", "run-1")];

  const pruned = pruneRealtimeEntries(server, realtime);
  assert.equal(pruned.length, 0, "optimistic skill card must be dropped");

  const merged = mergeSdkTranscript(server, realtime);
  const skillRows = merged.filter(
    (e) =>
      (e as Record<string, unknown>).messageKind === "skill-action" ||
      e.display?.kind === "skill_invocation",
  );
  assert.equal(skillRows.length, 1, "exactly one skill card should render");
});

test("distinct invocations of the same skill do not cross-prune", () => {
  // A second /test invocation lands while the first is already a durable
  // server row. The second optimistic card carries a different client nonce,
  // so it must survive until its own durable event arrives.
  const server = [durableSkillMessage("test", "run-1")];
  const realtime = [optimisticSkillCard("test", "run-2")];

  const pruned = pruneRealtimeEntries(server, realtime);
  assert.equal(pruned.length, 1, "the second invocation must not be dropped");
  assert.equal(pruned[0].clientNonce, "run-2");
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
  assert.equal(pruneRealtimeEntries(server, realtime).length, 0);
});
