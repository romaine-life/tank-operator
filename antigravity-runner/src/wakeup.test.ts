import assert from "node:assert/strict";
import test from "node:test";

import { extractScheduleWakeups } from "./wakeup.js";

test("extracts Antigravity schedule tool calls as durable wake intents", () => {
  const wakeups = extractScheduleWakeups({
    step_index: 17,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [
      {
        name: "schedule",
        args: {
          DurationSeconds: "15",
          Prompt: "Check whether the build finished.",
          toolSummary: "Wait for build",
        },
      },
    ],
  });

  assert.deepEqual(wakeups, [
    {
      delayMs: 15_000,
      prompt: "Check whether the build finished.",
      providerItemID: "tool-17-0",
    },
  ]);
});

test("ignores malformed schedule calls instead of registering broken wakes", () => {
  const wakeups = extractScheduleWakeups({
    step_index: 3,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [
      { name: "schedule", args: { DurationSeconds: "-1", Prompt: "later" } },
      { name: "schedule", args: { DurationSeconds: "5", Prompt: "" } },
      { name: "run_command", args: { CommandLine: "sleep 5" } },
    ],
  });

  assert.deepEqual(wakeups, []);
});
