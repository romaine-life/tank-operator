import assert from "node:assert/strict";
import test from "node:test";

import {
  extractScheduleWakeups,
  isAssistantPlannerTextStep,
  isNativeScheduleWakeResponse,
  scheduleAckGraceMs,
} from "./wakeup.js";

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

test("classifies Antigravity schedule acknowledgement versus native wake text", () => {
  const ack = {
    step_index: 4,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    content: "I have set the wake timer.",
  };
  const nativeWake = {
    step_index: 5,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    content: "  I am awake from the timer.  ",
  };
  const scheduleCall = {
    step_index: 3,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [{ name: "schedule", args: {} }],
  };

  assert.equal(isAssistantPlannerTextStep(ack), true);
  assert.equal(isAssistantPlannerTextStep(scheduleCall), false);
  assert.equal(
    isNativeScheduleWakeResponse(nativeWake, ["I am awake from the timer."]),
    true,
  );
  assert.equal(
    isNativeScheduleWakeResponse(ack, ["I am awake from the timer."]),
    false,
  );
});

test("schedule acknowledgement grace is bounded below the native timer", () => {
  assert.equal(scheduleAckGraceMs(0), 100);
  assert.equal(scheduleAckGraceMs(200), 100);
  assert.equal(scheduleAckGraceMs(2_000), 500);
  assert.equal(scheduleAckGraceMs(15_000), 1_000);
});
