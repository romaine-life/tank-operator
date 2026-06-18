import { test, expect } from "vitest";

import {
  conversationRunIsActive,
  decideFollowupSubmit,
  describeRunBlock,
} from "./submitLatch";

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

test("describeRunBlock: a stop in progress wins over any durable status", () => {
  expect(
    describeRunBlock({
      durableActivityStatus: "streaming",
      runStatus: "stopping",
      hasLocalRun: true,
      localRunHasTerminal: false,
    }).kind,
  ).toBe("stopping");
  expect(
    describeRunBlock({
      durableActivityStatus: "stopping",
      runStatus: "running",
      hasLocalRun: true,
      localRunHasTerminal: false,
    }).kind,
  ).toBe("stopping");
});

test("describeRunBlock: needs_input is distinct from active work", () => {
  const d = describeRunBlock({
    durableActivityStatus: "needs_input",
    runStatus: "running",
    hasLocalRun: true,
    localRunHasTerminal: false,
  });
  expect(d.kind).toBe("agent-needs-input");
  expect(d.dotStatus).toBe("agent-needs-input");
  expect(d.label).toBe("Needs input");
});

test("describeRunBlock: a self-parked agent reads as scheduled", () => {
  const d = describeRunBlock({
    durableActivityStatus: "scheduled",
    runStatus: "running",
    hasLocalRun: true,
    localRunHasTerminal: false,
  });
  expect(d.kind).toBe("scheduled");
  expect(d.dotStatus).toBe("agent-scheduled");
});

test("describeRunBlock: submitted/claimed/streaming all read as agent working", () => {
  for (const status of ["submitted", "claimed", "streaming"] as const) {
    expect(
      describeRunBlock({
        durableActivityStatus: status,
        runStatus: "running",
        hasLocalRun: true,
        localRunHasTerminal: false,
      }).kind,
    ).toBe("agent-working");
  }
});

test("describeRunBlock: durable idle + local latch + no terminal = settling", () => {
  expect(
    describeRunBlock({
      durableActivityStatus: "ready",
      runStatus: "running",
      hasLocalRun: true,
      localRunHasTerminal: false,
    }).kind,
  ).toBe("settling");
});

test("describeRunBlock: durable idle + terminal landed = reconciling (refresh-needed)", () => {
  const d = describeRunBlock({
    durableActivityStatus: "ready",
    runStatus: "running",
    hasLocalRun: true,
    localRunHasTerminal: true,
  });
  expect(d.kind).toBe("reconciling");
  // The latch-lag tail is not an error — it reuses the calm working dot.
  expect(d.dotStatus).toBe("agent-working");
});

test("describeRunBlock: a null durable status falls through to settling", () => {
  expect(
    describeRunBlock({
      durableActivityStatus: null,
      runStatus: "running",
      hasLocalRun: false,
      localRunHasTerminal: false,
    }).kind,
  ).toBe("settling");
});

test("describeRunBlock: every emitted dotStatus has a status-dot CSS class", () => {
  // Guards against a bespoke dot class with no color rule. These four are the
  // members of the sessionActivity dot vocabulary that describeRunBlock emits;
  // each has a `.status-dot.status-*` rule in index.css.
  const allowed = new Set([
    "agent-working",
    "agent-needs-input",
    "agent-stopping",
    "agent-scheduled",
  ]);
  const cases = [
    { durableActivityStatus: "streaming" as const, runStatus: "running" as const },
    { durableActivityStatus: "needs_input" as const, runStatus: "running" as const },
    { durableActivityStatus: "scheduled" as const, runStatus: "running" as const },
    { durableActivityStatus: "stopping" as const, runStatus: "running" as const },
    { durableActivityStatus: "ready" as const, runStatus: "running" as const },
    { durableActivityStatus: null, runStatus: "running" as const },
  ];
  for (const c of cases) {
    const d = describeRunBlock({
      ...c,
      hasLocalRun: true,
      localRunHasTerminal: true,
    });
    expect(allowed.has(d.dotStatus)).toBe(true);
  }
});
