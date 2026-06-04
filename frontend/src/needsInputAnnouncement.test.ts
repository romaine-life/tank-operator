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
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: "interrupted" }),
    "settled",
  );
  assert.equal(
    needsInputAnnouncementState({ answered: false, turnTerminalStatus: "failed" }),
    "settled",
  );
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
