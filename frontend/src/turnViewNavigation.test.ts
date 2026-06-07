import { test, expect } from "vitest";

import { turnViewTurnNavigation } from "./turnViewNavigation.ts";

const TURNS = ["t1", "t2", "t3", "t4"];

test("no turns → everything inert, unlabeled, null targets", () => {
  const s = turnViewTurnNavigation([], null);
  expect(s.count).toBe(0);
  expect(s.position).toBe(0);
  expect(s.label).toBe("");
  expect(s.canFirst).toBe(false);
  expect(s.canPrev).toBe(false);
  expect(s.canNext).toBe(false);
  expect(s.canLast).toBe(false);
  expect(s.firstTurnId).toBe(null);
  expect(s.prevTurnId).toBe(null);
  expect(s.nextTurnId).toBe(null);
  expect(s.lastTurnId).toBe(null);
});

test("a middle turn → all four directions actionable, targeting neighbours/ends", () => {
  const s = turnViewTurnNavigation(TURNS, "t2");
  expect(s.position).toBe(2);
  expect(s.count).toBe(4);
  expect(s.label).toBe("turn 2 of 4");
  expect(s.canFirst).toBe(true);
  expect(s.canPrev).toBe(true);
  expect(s.canNext).toBe(true);
  expect(s.canLast).toBe(true);
  expect(s.firstTurnId).toBe("t1");
  expect(s.prevTurnId).toBe("t1");
  expect(s.nextTurnId).toBe("t3");
  expect(s.lastTurnId).toBe("t4");
});

test("the first turn → back/first disabled with inert targets, forward enabled", () => {
  const s = turnViewTurnNavigation(TURNS, "t1");
  expect(s.position).toBe(1);
  expect(s.canFirst).toBe(false);
  expect(s.canPrev).toBe(false);
  expect(s.prevTurnId).toBe("t1"); // inert: equals the current turn
  expect(s.firstTurnId).toBe("t1");
  expect(s.canNext).toBe(true);
  expect(s.nextTurnId).toBe("t2");
  expect(s.canLast).toBe(true);
  expect(s.lastTurnId).toBe("t4");
});

test("the last turn (the default landing) → forward/last disabled and inert", () => {
  const s = turnViewTurnNavigation(TURNS, "t4");
  expect(s.position).toBe(4);
  expect(s.canNext).toBe(false);
  expect(s.nextTurnId).toBe("t4"); // inert
  expect(s.canLast).toBe(false);
  expect(s.lastTurnId).toBe("t4"); // inert (equals current = last)
  expect(s.canPrev).toBe(true);
  expect(s.prevTurnId).toBe("t3");
  expect(s.canFirst).toBe(true);
  expect(s.firstTurnId).toBe("t1");
});

test("a null / unknown selection anchors on the latest turn (matches the view fallback)", () => {
  const nullSel = turnViewTurnNavigation(TURNS, null);
  expect(nullSel.position).toBe(4);
  expect(nullSel.label).toBe("turn 4 of 4");
  expect(nullSel.canNext).toBe(false);
  expect(nullSel.canLast).toBe(false);
  expect(nullSel.canPrev).toBe(true);

  const stale = turnViewTurnNavigation(TURNS, "does-not-exist");
  expect(stale.position).toBe(4);
  expect(stale.canLast).toBe(false);
  expect(stale.prevTurnId).toBe("t3");
});

test("a single turn → both ends disabled, the lone turn is every target", () => {
  const s = turnViewTurnNavigation(["only"], "only");
  expect(s.position).toBe(1);
  expect(s.count).toBe(1);
  expect(s.label).toBe("turn 1 of 1");
  expect(s.canFirst).toBe(false);
  expect(s.canPrev).toBe(false);
  expect(s.canNext).toBe(false);
  expect(s.canLast).toBe(false);
  expect(s.firstTurnId).toBe("only");
  expect(s.prevTurnId).toBe("only");
  expect(s.nextTurnId).toBe("only");
  expect(s.lastTurnId).toBe("only");
});
