import { test, expect } from "vitest";

import {
  evaluateTurnActivityReconcile,
  resolveDisplayedActivityTurn,
  type TurnActivityReconcileInput,
} from "./turnActivityLoadReconcile.ts";
import { type TurnActivityLoadStatus } from "./turnActivityState.ts";

function decide(overrides: Partial<TurnActivityReconcileInput>) {
  return evaluateTurnActivityReconcile({
    active: true,
    hasDisplayedTurn: true,
    status: undefined,
    ...overrides,
  }).action;
}

// --- the gate -------------------------------------------------------------

test("a hidden / non-Turns pane never loads", () => {
  // The tabs view keeps non-routed session panes mounted; they must not load.
  expect(decide({ active: false, status: undefined })).toBe("idle");
  expect(decide({ active: false, status: "unloaded" })).toBe("idle");
  expect(decide({ active: false, status: "loaded" })).toBe("idle");
});

test("no displayed turn is idle", () => {
  expect(decide({ hasDisplayedTurn: false, status: undefined })).toBe("idle");
});

test("absent load state for a displayed turn loads — the strand class", () => {
  // `undefined` is "no entry in the turn-id-keyed map": exactly what the
  // deep-link / route-resolver / default-latest selection paths leave behind
  // for the displayed turn.
  expect(decide({ status: undefined })).toBe("load");
});

test("an explicitly unloaded displayed turn loads", () => {
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

test("regression guard: a visible Turns pane with a displayed, non-terminal, not-loading turn ALWAYS loads", () => {
  // This is the strand-without-recovery class the level-triggered gate exists
  // to make impossible. If a future refactor lets either of these resolve to
  // "idle", the dead-refresh strand is back.
  const strandStatuses: Array<TurnActivityLoadStatus | undefined> = [
    undefined,
    "unloaded",
  ];
  for (const status of strandStatuses) {
    expect(decide({ active: true, hasDisplayedTurn: true, status })).toBe(
      "load",
    );
  }
});

// --- the displayed-turn resolver ------------------------------------------

const turns = [
  { turnId: "t1" },
  { turnId: "t2" },
  { turnId: "t3" }, // latest
];

test("displayed turn is the exact match when the selected id is in the directory", () => {
  expect(resolveDisplayedActivityTurn(turns, "t2")).toEqual({ turnId: "t2" });
});

test("displayed turn falls back to the latest when the selected id is NOT in the directory", () => {
  // The dead-refresh divergence: a deep-link / refresh keeps an unresolved id
  // in effectiveSelectedTurnId that is not (yet) in the windowed directory.
  // The body shows the latest turn — so the latest turn is what must load.
  expect(resolveDisplayedActivityTurn(turns, "t-not-in-window")).toEqual({
    turnId: "t3",
  });
});

test("displayed turn falls back to the latest when no turn is selected", () => {
  expect(resolveDisplayedActivityTurn(turns, null)).toEqual({ turnId: "t3" });
});

test("no turns → no displayed turn", () => {
  expect(resolveDisplayedActivityTurn([], "t2")).toBeNull();
  expect(resolveDisplayedActivityTurn([], null)).toBeNull();
});

// --- composed: the actual dead-refresh bug --------------------------------

test("dead-refresh regression: an off-directory selected id still loads the displayed (latest) turn", () => {
  // Reproduces the bug end-to-end at the pure-logic layer. Before the fix, the
  // loader keyed off the selected id (`t-old`, absent from the window) so the
  // displayed latest turn (`t3`) stranded. The fix resolves the displayed turn
  // first, then reconciles THAT turn's load.
  const selectedId = "t-old"; // resolving / windowed-out; not in `turns`
  const displayed = resolveDisplayedActivityTurn(turns, selectedId);
  expect(displayed?.turnId).toBe("t3"); // body shows the latest turn

  const loadsByTurn: Record<string, { status: TurnActivityLoadStatus }> = {};
  // The displayed turn has no load entry → must reconcile to "load".
  const decision = evaluateTurnActivityReconcile({
    active: true,
    hasDisplayedTurn: displayed != null,
    status: displayed ? loadsByTurn[displayed.turnId]?.status : undefined,
  });
  expect(decision.action).toBe("load");

  // Keying off the off-directory selected id instead (the old behavior) would
  // also say "load" but for the WRONG turn — the displayed turn would never be
  // reconciled. Pin that the resolved target is the displayed turn, not the id.
  expect(displayed?.turnId).not.toBe(selectedId);
});
