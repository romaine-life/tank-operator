import { test } from "node:test";
import assert from "node:assert/strict";

import {
  needsInputAnnouncementState,
  needsInputAnnouncementIsSettled,
} from "./needsInputAnnouncement";

test("unanswered question on a live turn is 'waiting'", () => {
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: undefined }),
    "waiting",
  );
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: null }),
    "waiting",
  );
});

test("answered question is 'answered' regardless of turn terminal status", () => {
  assert.equal(
    needsInputAnnouncementState({ answered: true, turnTerminalStatus: undefined }),
    "answered",
  );
  assert.equal(
    needsInputAnnouncementState({ answered: true, turnTerminalStatus: "completed" }),
    "answered",
  );
  // A question the user answered stays "answered" even if the owning turn is
  // later interrupted or fails for an unrelated reason — the handoff itself
  // was satisfied, so it must not regress to the inert "settled" copy.
  assert.equal(
    needsInputAnnouncementState({ answered: true, turnTerminalStatus: "interrupted" }),
    "answered",
  );
  assert.equal(
    needsInputAnnouncementState({ answered: true, turnTerminalStatus: "failed" }),
    "answered",
  );
});

test("unanswered question whose turn ended is 'settled' (nothing is being waited on)", () => {
  // The reported bug: the user declined to answer and stopped the turn.
  // turn.interrupted lands, the question stays unanswered, and the row must
  // settle instead of keeping the "Claude is waiting on you" active state.
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: "interrupted" }),
    "settled",
  );
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: "failed" }),
    "settled",
  );
  // A turn that completes without the question ever being answered is also
  // settled — the agent moved on, so the handoff is no longer pending.
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: "completed" }),
    "settled",
  );
});

test("needsInputAnnouncementIsSettled separates the muted states from the active one", () => {
  assert.equal(needsInputAnnouncementIsSettled("waiting"), false);
  assert.equal(needsInputAnnouncementIsSettled("answered"), true);
  assert.equal(needsInputAnnouncementIsSettled("settled"), true);
});
