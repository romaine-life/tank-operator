import assert from "node:assert/strict";
import test from "node:test";

import {
  normalizeSessionActivity,
  sessionActivityChips,
  sessionActivityDotStatus,
  sessionActivityStatusLabel,
} from "./sessionActivity";

test("normalizes backend session activity summaries", () => {
  const activity = normalizeSessionActivity({
    session_id: "63",
    status: "streaming",
    last_order_key: "001",
    unread_count: 3.9,
    needs_input: false,
    failed: false,
    active_turn_id: "turn-1",
    updated_at: "2026-05-12T00:00:00Z",
  });

  assert.equal(activity?.session_id, "63");
  assert.equal(activity?.status, "streaming");
  assert.equal(activity?.unread_count, 3);
  assert.equal(activity?.active_turn_id, "turn-1");
});

test("session activity drives sidebar labels and dots", () => {
  const needsInput = normalizeSessionActivity({
    session_id: "63",
    status: "needs_input",
    unread_count: 1,
    needs_input: true,
    failed: false,
  });

  assert.equal(sessionActivityDotStatus("Active", true, needsInput ?? undefined), "agent-needs-input");
  assert.equal(sessionActivityStatusLabel("Active", true, needsInput ?? undefined), "Needs input");
  assert.deepEqual(
    sessionActivityChips(needsInput ?? undefined).map((chip) => chip.label),
    ["input", "1 new"],
  );
});

test("stopping status drives Stopping label, agent-stopping dot, and stopping chip", () => {
  const stopping = normalizeSessionActivity({
    session_id: "63",
    status: "stopping",
    unread_count: 0,
    needs_input: false,
    failed: false,
    active_turn_id: "turn-1",
  });

  assert.equal(stopping?.status, "stopping");
  assert.equal(sessionActivityDotStatus("Active", true, stopping ?? undefined), "agent-stopping");
  assert.equal(sessionActivityStatusLabel("Active", true, stopping ?? undefined), "Stopping");
  assert.deepEqual(
    sessionActivityChips(stopping ?? undefined).map((chip) => ({ label: chip.label, tone: chip.tone })),
    [{ label: "stopping", tone: "stopping" }],
  );
});

test("non-chat sessions keep pod lifecycle status", () => {
  const activity = normalizeSessionActivity({
    session_id: "12",
    status: "streaming",
    unread_count: 2,
  });

  assert.equal(sessionActivityDotStatus("Pending", false, activity ?? undefined), "pending");
  assert.equal(sessionActivityStatusLabel("Pending", false, activity ?? undefined), "Pending");
});
