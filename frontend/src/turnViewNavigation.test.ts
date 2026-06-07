import assert from "node:assert/strict";
import { test } from "node:test";

import { turnViewTurnNavigation } from "./turnViewNavigation.ts";

const TURNS = ["t1", "t2", "t3", "t4"];

test("no turns → everything inert, unlabeled, null targets", () => {
  const s = turnViewTurnNavigation([], null);
  assert.equal(s.count, 0);
  assert.equal(s.position, 0);
  assert.equal(s.label, "");
  assert.equal(s.canFirst, false);
  assert.equal(s.canPrev, false);
  assert.equal(s.canNext, false);
  assert.equal(s.canLast, false);
  assert.equal(s.firstTurnId, null);
  assert.equal(s.prevTurnId, null);
  assert.equal(s.nextTurnId, null);
  assert.equal(s.lastTurnId, null);
});

test("a middle turn → all four directions actionable, targeting neighbours/ends", () => {
  const s = turnViewTurnNavigation(TURNS, "t2");
  assert.equal(s.position, 2);
  assert.equal(s.count, 4);
  assert.equal(s.label, "turn 2 of 4");
  assert.equal(s.canFirst, true);
  assert.equal(s.canPrev, true);
  assert.equal(s.canNext, true);
  assert.equal(s.canLast, true);
  assert.equal(s.firstTurnId, "t1");
  assert.equal(s.prevTurnId, "t1");
  assert.equal(s.nextTurnId, "t3");
  assert.equal(s.lastTurnId, "t4");
});

test("the first turn → back/first disabled with inert targets, forward enabled", () => {
  const s = turnViewTurnNavigation(TURNS, "t1");
  assert.equal(s.position, 1);
  assert.equal(s.canFirst, false);
  assert.equal(s.canPrev, false);
  assert.equal(s.prevTurnId, "t1"); // inert: equals the current turn
  assert.equal(s.firstTurnId, "t1");
  assert.equal(s.canNext, true);
  assert.equal(s.nextTurnId, "t2");
  assert.equal(s.canLast, true);
  assert.equal(s.lastTurnId, "t4");
});

test("the last turn (the default landing) → forward/last disabled and inert", () => {
  const s = turnViewTurnNavigation(TURNS, "t4");
  assert.equal(s.position, 4);
  assert.equal(s.canNext, false);
  assert.equal(s.nextTurnId, "t4"); // inert
  assert.equal(s.canLast, false);
  assert.equal(s.lastTurnId, "t4"); // inert (equals current = last)
  assert.equal(s.canPrev, true);
  assert.equal(s.prevTurnId, "t3");
  assert.equal(s.canFirst, true);
  assert.equal(s.firstTurnId, "t1");
});

test("a null / unknown selection anchors on the latest turn (matches the view fallback)", () => {
  const nullSel = turnViewTurnNavigation(TURNS, null);
  assert.equal(nullSel.position, 4);
  assert.equal(nullSel.label, "turn 4 of 4");
  assert.equal(nullSel.canNext, false);
  assert.equal(nullSel.canLast, false);
  assert.equal(nullSel.canPrev, true);

  const stale = turnViewTurnNavigation(TURNS, "does-not-exist");
  assert.equal(stale.position, 4);
  assert.equal(stale.canLast, false);
  assert.equal(stale.prevTurnId, "t3");
});

test("a single turn → both ends disabled, the lone turn is every target", () => {
  const s = turnViewTurnNavigation(["only"], "only");
  assert.equal(s.position, 1);
  assert.equal(s.count, 1);
  assert.equal(s.label, "turn 1 of 1");
  assert.equal(s.canFirst, false);
  assert.equal(s.canPrev, false);
  assert.equal(s.canNext, false);
  assert.equal(s.canLast, false);
  assert.equal(s.firstTurnId, "only");
  assert.equal(s.prevTurnId, "only");
  assert.equal(s.nextTurnId, "only");
  assert.equal(s.lastTurnId, "only");
});
