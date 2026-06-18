import { test, expect } from "vitest";

import {
  evaluateTurnActivityReconcile,
  type TurnActivityReconcileInput,
} from "./turnActivityLoadReconcile.ts";
import { type TurnActivityLoadStatus } from "./turnActivityState.ts";

function decide(overrides: Partial<TurnActivityReconcileInput>) {
  return evaluateTurnActivityReconcile({
    active: true,
    hasSelectedTurn: true,
    status: undefined,
    ...overrides,
  }).action;
}

test("a hidden / non-Turns pane never loads", () => {
  // The tabs view keeps non-routed session panes mounted; they must not load.
  expect(decide({ active: false, status: undefined })).toBe("idle");
  expect(decide({ active: false, status: "unloaded" })).toBe("idle");
  expect(decide({ active: false, status: "loaded" })).toBe("idle");
});

test("no selected turn is idle", () => {
  expect(decide({ hasSelectedTurn: false, status: undefined })).toBe("idle");
});

test("absent load state for a selected turn loads — the strand class", () => {
  // `undefined` is "no entry in the turn-id-keyed map": exactly what the
  // deep-link / route-resolver / default-latest selection paths leave behind.
  expect(decide({ status: undefined })).toBe("load");
});

test("an explicitly unloaded selected turn loads", () => {
  expect(decide({ status: "unloaded" })).toBe("load");
});

test("a loading turn is left alone — no duplicate load, no hot loop", () => {
  expect(decide({ status: "loading" })).toBe("idle");
});

test("a loaded turn is the desired terminal state", () => {
  expect(decide({ status: "loaded" })).toBe("idle");
});

test("an errored turn is terminal — Retry / re-select re-drives, never auto-retry", () => {
  expect(decide({ status: "error" })).toBe("idle");
});

test("regression guard: a visible Turns pane with a selected, non-terminal, not-loading turn ALWAYS loads", () => {
  // This is the strand-without-recovery class the level-triggered gate exists
  // to make impossible. If a future refactor lets either of these resolve to
  // "idle", the dead-refresh strand is back.
  const strandStatuses: Array<TurnActivityLoadStatus | undefined> = [
    undefined,
    "unloaded",
  ];
  for (const status of strandStatuses) {
    expect(decide({ active: true, hasSelectedTurn: true, status })).toBe(
      "load",
    );
  }
});
