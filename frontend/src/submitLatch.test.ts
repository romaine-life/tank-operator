import { test, expect } from "vitest";

import { conversationRunIsActive, decideFollowupSubmit } from "./submitLatch";

test("conversationRunIsActive matches the durable in-flight statuses", () => {
  expect(conversationRunIsActive("submitted")).toBe(true);
  expect(conversationRunIsActive("streaming")).toBe(true);
  expect(conversationRunIsActive("needs_input")).toBe(true);
  expect(conversationRunIsActive("stopping")).toBe(true);
  expect(conversationRunIsActive("ready")).toBe(false);
  expect(conversationRunIsActive("stopped")).toBe(false);
  expect(conversationRunIsActive("error")).toBe(false);
});

test("submit gate queues while durable state is active", () => {
  expect(decideFollowupSubmit({
          running: false,
          durableRunStatus: "streaming",
          hasLocalRun: false,
          localRunHasDurableTerminal: false,
        })).toEqual({ action: "queue", reason: "durable_active" });
});

test("submit gate submits when no run is active", () => {
  expect(decideFollowupSubmit({
          running: false,
          durableRunStatus: "ready",
          hasLocalRun: false,
          localRunHasDurableTerminal: false,
        })).toEqual({ action: "submit" });
});

test("submit gate recovers stale running with no local run", () => {
  expect(decideFollowupSubmit({
          running: true,
          durableRunStatus: "ready",
          hasLocalRun: false,
          localRunHasDurableTerminal: false,
        })).toEqual({ action: "submit", staleReason: "running_without_local_run" });
});

test("submit gate recovers stale local run after durable terminal", () => {
  expect(decideFollowupSubmit({
          running: true,
          durableRunStatus: "ready",
          hasLocalRun: true,
          localRunHasDurableTerminal: true,
        })).toEqual({ action: "submit", staleReason: "local_run_after_durable_terminal" });
});

test("submit gate keeps optimistic local run pending until durable state catches up", () => {
  expect(decideFollowupSubmit({
          running: true,
          durableRunStatus: "ready",
          hasLocalRun: true,
          localRunHasDurableTerminal: false,
        })).toEqual({ action: "queue", reason: "local_run_pending" });
});
