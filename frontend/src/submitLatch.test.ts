import assert from "node:assert/strict";
import test from "node:test";

import { conversationRunIsActive, decideFollowupSubmit } from "./submitLatch";

test("conversationRunIsActive matches the durable in-flight statuses", () => {
  assert.equal(conversationRunIsActive("submitted"), true);
  assert.equal(conversationRunIsActive("streaming"), true);
  assert.equal(conversationRunIsActive("needs_input"), true);
  assert.equal(conversationRunIsActive("stopping"), true);
  assert.equal(conversationRunIsActive("ready"), false);
  assert.equal(conversationRunIsActive("stopped"), false);
  assert.equal(conversationRunIsActive("error"), false);
});

test("submit gate queues while durable state is active", () => {
  assert.deepEqual(
    decideFollowupSubmit({
      running: false,
      durableRunStatus: "streaming",
      hasLocalRun: false,
      localRunHasDurableTerminal: false,
    }),
    { action: "queue", reason: "durable_active" },
  );
});

test("submit gate submits when no run is active", () => {
  assert.deepEqual(
    decideFollowupSubmit({
      running: false,
      durableRunStatus: "ready",
      hasLocalRun: false,
      localRunHasDurableTerminal: false,
    }),
    { action: "submit" },
  );
});

test("submit gate recovers stale running with no local run", () => {
  assert.deepEqual(
    decideFollowupSubmit({
      running: true,
      durableRunStatus: "ready",
      hasLocalRun: false,
      localRunHasDurableTerminal: false,
    }),
    { action: "submit", staleReason: "running_without_local_run" },
  );
});

test("submit gate recovers stale local run after durable terminal", () => {
  assert.deepEqual(
    decideFollowupSubmit({
      running: true,
      durableRunStatus: "ready",
      hasLocalRun: true,
      localRunHasDurableTerminal: true,
    }),
    { action: "submit", staleReason: "local_run_after_durable_terminal" },
  );
});

test("submit gate keeps optimistic local run pending until durable state catches up", () => {
  assert.deepEqual(
    decideFollowupSubmit({
      running: true,
      durableRunStatus: "ready",
      hasLocalRun: true,
      localRunHasDurableTerminal: false,
    }),
    { action: "queue", reason: "local_run_pending" },
  );
});
