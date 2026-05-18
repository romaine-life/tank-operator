import assert from "node:assert/strict";
import test from "node:test";

import {
  normalizeSessionActivity,
  orderKeyAfter,
  sessionActivityChips,
  sessionActivityDotStatus,
  sessionActivityStatusLabel,
  shouldRingForActivityTransition,
  type ConversationActivityStatus,
  type SessionActivitySummary,
} from "./sessionActivity";

function summary(
  status: ConversationActivityStatus,
  patch: Partial<SessionActivitySummary> = {},
): SessionActivitySummary {
  return {
    session_id: "63",
    status,
    last_order_key: null,
    unread_count: 0,
    needs_input: status === "needs_input",
    failed: status === "error",
    active_turn_id: null,
    updated_at: null,
    ...patch,
  };
}

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

// shouldRingForActivityTransition is the centralized "play turn-complete
// sound?" predicate. The transition table below pins the rule we landed on
// after observing the backend's lifecycle_emitter coalesce fast turns
// (submitted → streaming → ready arrive at the persister in the same fold
// window) into a single activity_changed event with status=ready and no
// prior in the page's session state. The predicate's earlier "must have
// working prior" requirement was too narrow and silenced those turns.
//
// New rule: ring when next is a user-turn state (ready | needs_input) AND
// the prior wasn't already a user-turn state. Bootstrap-replay safety is
// the caller's responsibility (snapshot-applied flag + per-session
// last_order_key dedup).
test("shouldRingForActivityTransition: streaming -> ready rings", () => {
  assert.equal(
    shouldRingForActivityTransition(summary("streaming"), summary("ready")),
    true,
  );
});

test("shouldRingForActivityTransition: submitted -> ready rings", () => {
  assert.equal(
    shouldRingForActivityTransition(summary("submitted"), summary("ready")),
    true,
  );
});

test("shouldRingForActivityTransition: streaming -> needs_input rings", () => {
  assert.equal(
    shouldRingForActivityTransition(summary("streaming"), summary("needs_input")),
    true,
  );
});

test("shouldRingForActivityTransition: no prior -> ready rings (fast-turn coalesce case)", () => {
  // The backend's lifecycle_emitter re-folds last 50 chat events on every
  // emit, and for a fast turn all three chat events (submitted, started,
  // completed) land before the first emit-handler reads, so it emits a
  // single activity_changed with status=ready. The page hasn't seen a prior
  // for this session yet, but this transition IS a turn completion and
  // must ring. Outer bootstrap-applied/dedup gates protect this from
  // double-ringing on SSE catchup replays.
  assert.equal(
    shouldRingForActivityTransition(undefined, summary("ready")),
    true,
  );
});

test("shouldRingForActivityTransition: no prior -> needs_input rings", () => {
  // Same reasoning as the no-prior → ready case: approval-required terminal
  // is also a "your turn" state.
  assert.equal(
    shouldRingForActivityTransition(undefined, summary("needs_input")),
    true,
  );
});

test("shouldRingForActivityTransition: error -> ready rings (recovery)", () => {
  // A new turn succeeded after a prior failure. Agent went from a
  // not-ready-for-input state to a ready-for-input state — that's the
  // signal we want to surface.
  assert.equal(
    shouldRingForActivityTransition(summary("error"), summary("ready")),
    true,
  );
});

test("shouldRingForActivityTransition: streaming -> stopped does NOT ring", () => {
  // Stop is user-initiated; the user pressed the button, they know it stopped.
  assert.equal(
    shouldRingForActivityTransition(summary("streaming"), summary("stopped")),
    false,
  );
});

test("shouldRingForActivityTransition: streaming -> stopping does NOT ring", () => {
  // Stop in progress; same reasoning as stopped — user-initiated.
  assert.equal(
    shouldRingForActivityTransition(summary("streaming"), summary("stopping")),
    false,
  );
});

test("shouldRingForActivityTransition: stopping -> ready does NOT ring", () => {
  // The user pressed Stop; the agent winding down and returning to ready is
  // expected. Don't treat it as a "your turn" event.
  assert.equal(
    shouldRingForActivityTransition(summary("stopping"), summary("ready")),
    false,
  );
});

test("shouldRingForActivityTransition: stopped -> ready does NOT ring", () => {
  // Same as stopping → ready: the user already knows they stopped the agent.
  assert.equal(
    shouldRingForActivityTransition(summary("stopped"), summary("ready")),
    false,
  );
});

test("shouldRingForActivityTransition: streaming -> error does NOT ring", () => {
  // Matches today's working path: finalizeSdkRun only rang on terminal=done.
  // Errors are visible in-pane; not surfacing as audio keeps the sound
  // semantically narrow ("your turn now").
  assert.equal(
    shouldRingForActivityTransition(summary("streaming"), summary("error")),
    false,
  );
});

test("shouldRingForActivityTransition: ready -> ready (no-op) does NOT ring", () => {
  assert.equal(
    shouldRingForActivityTransition(summary("ready"), summary("ready")),
    false,
  );
});

test("shouldRingForActivityTransition: needs_input -> needs_input does NOT ring", () => {
  // Two approval requests in a row — already in "your turn", don't re-ring.
  assert.equal(
    shouldRingForActivityTransition(summary("needs_input"), summary("needs_input")),
    false,
  );
});

test("shouldRingForActivityTransition: ready -> needs_input does NOT ring", () => {
  // Both are user-turn states; agent didn't transition OUT of user-turn first.
  assert.equal(
    shouldRingForActivityTransition(summary("ready"), summary("needs_input")),
    false,
  );
});

test("shouldRingForActivityTransition: ready -> streaming does NOT ring", () => {
  // User submitted a new turn; agent is now working. Not "your turn."
  assert.equal(
    shouldRingForActivityTransition(summary("ready"), summary("streaming")),
    false,
  );
});

test("shouldRingForActivityTransition: no prior -> streaming does NOT ring", () => {
  // First sighting but the new state is "agent working" — nothing for the
  // user to act on yet.
  assert.equal(
    shouldRingForActivityTransition(undefined, summary("streaming")),
    false,
  );
});

test("shouldRingForActivityTransition: no prior -> error does NOT ring", () => {
  // First sighting but the new state is a failure — error toast / pill
  // covers visibility; sound stays scoped to user-action transitions.
  assert.equal(
    shouldRingForActivityTransition(undefined, summary("error")),
    false,
  );
});

test("orderKeyAfter: numeric compare across digit boundary", () => {
  // The bug this guards against: lexicographic string compare says
  // "100" < "99", which made the sound-dedup gate suppress every
  // transition that crossed a power-of-ten ledger order_key boundary.
  assert.equal(orderKeyAfter("100", "99"), true);
  assert.equal(orderKeyAfter("99", "100"), false);
  assert.equal(orderKeyAfter("1000", "999"), true);
  assert.equal(orderKeyAfter("999", "1000"), false);
});

test("orderKeyAfter: same value is not after", () => {
  assert.equal(orderKeyAfter("42", "42"), false);
});

test("orderKeyAfter: handles BIGSERIAL values past Number.MAX_SAFE_INTEGER", () => {
  // Long-running ledger forward compatibility — BIGSERIAL is int64.
  assert.equal(
    orderKeyAfter("9223372036854775806", "9223372036854775805"),
    true,
  );
});

test("orderKeyAfter: garbage input falls back to string compare", () => {
  // Defensive: backend always emits decimal strings, but we shouldn't
  // throw if some test or future schema change hands us a non-numeric.
  assert.equal(orderKeyAfter("zzz", "aaa"), true);
  assert.equal(orderKeyAfter("aaa", "zzz"), false);
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
