import { test } from "node:test";
import assert from "node:assert/strict";

import { extractWakeup } from "./wakeup.js";

test("extracts ScheduleWakeup tool_use from assistant content", () => {
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [
        { type: "text", text: "I'll check back later." },
        {
          type: "tool_use",
          id: "toolu_x",
          name: "ScheduleWakeup",
          input: { delaySeconds: 300, prompt: "check the deploy" },
        },
      ],
    },
  } as any);
  assert.ok(got);
  assert.equal(got!.delayMs, 300_000);
  assert.equal(got!.prompt, "check the deploy");
  assert.equal(got!.providerItemID, "toolu_x");
});

test("returns null for assistant without tool_use", () => {
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [{ type: "text", text: "hi" }],
    },
  } as any);
  assert.equal(got, null);
});

test("ignores other tool_use blocks", () => {
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [
        { type: "tool_use", id: "x", name: "Bash", input: { command: "ls" } },
      ],
    },
  } as any);
  assert.equal(got, null);
});

test("rejects ScheduleWakeup with missing prompt", () => {
  // Scheduling an empty prompt would loop the agent on itself.
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [
        {
          type: "tool_use",
          id: "x",
          name: "ScheduleWakeup",
          input: { delaySeconds: 60 },
        },
      ],
    },
  } as any);
  assert.equal(got, null);
});

test("rejects ScheduleWakeup without provider item id", () => {
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [
        {
          type: "tool_use",
          name: "ScheduleWakeup",
          input: { delaySeconds: 60, prompt: "check later" },
        },
      ],
    },
  } as any);
  assert.equal(got, null);
});

test("case-insensitive tool name match", () => {
  const got = extractWakeup({
    type: "assistant",
    uuid: "abc",
    session_id: "s1",
    parent_tool_use_id: null,
    message: {
      role: "assistant",
      content: [
        {
          type: "tool_use",
          id: "x",
          name: "schedulewakeup",
          input: { delaySeconds: 1, prompt: "hi" },
        },
      ],
    },
  } as any);
  assert.ok(got);
  assert.equal(got!.delayMs, 1000);
  assert.equal(got!.providerItemID, "x");
});

test("returns null for non-assistant message types", () => {
  for (const t of ["user", "system", "result", "stream_event"]) {
    assert.equal(extractWakeup({ type: t } as any), null);
  }
});
