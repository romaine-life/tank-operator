import { test, expect } from "vitest";

import {
  SESSION_ACTIVITY_STATUS_LEGEND,
  normalizeSessionActivity,
  orderKeyAfter,
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
    away_error: false,
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

  expect(activity?.session_id).toBe("63");
  expect(activity?.status).toBe("streaming");
  expect(activity?.unread_count).toBe(3);
  expect(activity?.active_turn_id).toBe("turn-1");
});

test("session activity drives sidebar labels and dots", () => {
  const needsInput = normalizeSessionActivity({
    session_id: "63",
    status: "needs_input",
    unread_count: 1,
    needs_input: true,
    failed: false,
  });

  expect(sessionActivityDotStatus("Active", true, needsInput ?? undefined)).toBe("agent-needs-input");
  expect(sessionActivityStatusLabel("Active", true, needsInput ?? undefined)).toBe("Needs input");
});

test("running and unread activity stay on the status dot", () => {
  const streaming = normalizeSessionActivity({
    session_id: "63",
    status: "streaming",
    unread_count: 20,
    needs_input: false,
    failed: false,
    active_turn_id: "turn-1",
  });

  expect(sessionActivityDotStatus("Active", true, streaming ?? undefined)).toBe("agent-working");
  expect(sessionActivityStatusLabel("Active", true, streaming ?? undefined)).toBe("Running");
});

test("claimed activity is a working starting state", () => {
  const claimed = normalizeSessionActivity({
    session_id: "63",
    status: "claimed",
    unread_count: 0,
    needs_input: false,
    failed: false,
    active_turn_id: "turn-1",
  });

  expect(claimed?.status).toBe("claimed");
  expect(sessionActivityDotStatus("Active", true, claimed ?? undefined)).toBe("agent-working");
  expect(sessionActivityStatusLabel("Active", true, claimed ?? undefined)).toBe("Starting");
});

test("stopping status drives Stopping label and agent-stopping dot", () => {
  const stopping = normalizeSessionActivity({
    session_id: "63",
    status: "stopping",
    unread_count: 0,
    needs_input: false,
    failed: false,
    active_turn_id: "turn-1",
  });

  expect(stopping?.status).toBe("stopping");
  expect(sessionActivityDotStatus("Active", true, stopping ?? undefined)).toBe("agent-stopping");
  expect(sessionActivityStatusLabel("Active", true, stopping ?? undefined)).toBe("Stopping");
});

test("session activity legend mirrors sidebar dot mappings", () => {
  const byKey = new Map(SESSION_ACTIVITY_STATUS_LEGEND.map((item) => [item.key, item]));
  const cases: Array<{
    key: string;
    activity: SessionActivitySummary;
    dot: string | null;
  }> = [
    { key: "ready", activity: summary("ready"), dot: "agent-waiting" },
    { key: "running", activity: summary("streaming"), dot: "agent-working" },
    { key: "scheduled", activity: summary("scheduled"), dot: "agent-scheduled" },
    { key: "needs-input", activity: summary("needs_input"), dot: "agent-needs-input" },
    { key: "stopping", activity: summary("stopping"), dot: "agent-stopping" },
    { key: "stopped", activity: summary("stopped"), dot: "agent-stopped" },
    { key: "failed", activity: summary("error"), dot: "agent-error" },
  ];

  expect(byKey.size).toBe(cases.length);
  for (const item of cases) {
    const legend = byKey.get(item.key);
    expect(legend, `missing legend item for ${item.key}`).toBeTruthy();
    if (item.dot) {
      expect(legend.dotStatus).toBe(sessionActivityDotStatus("Active", true, item.activity));
    } else {
      expect(legend.dotStatus).toBe(null);
    }
  }
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
  expect(shouldRingForActivityTransition(summary("streaming"), summary("ready"))).toBe(true);
});

test("shouldRingForActivityTransition: submitted -> ready rings", () => {
  expect(shouldRingForActivityTransition(summary("submitted"), summary("ready"))).toBe(true);
});

test("shouldRingForActivityTransition: streaming -> needs_input rings", () => {
  expect(shouldRingForActivityTransition(summary("streaming"), summary("needs_input"))).toBe(true);
});

test("shouldRingForActivityTransition: no prior -> ready rings (fast-turn coalesce case)", () => {
  // The backend's lifecycle_emitter re-folds last 50 chat events on every
  // emit, and for a fast turn all three chat events (submitted, started,
  // completed) land before the first emit-handler reads, so it emits a
  // single activity_changed with status=ready. The page hasn't seen a prior
  // for this session yet, but this transition IS a turn completion and
  // must ring. Outer bootstrap-applied/dedup gates protect this from
  // double-ringing on SSE catchup replays.
  expect(shouldRingForActivityTransition(undefined, summary("ready"))).toBe(true);
});

test("shouldRingForActivityTransition: no prior -> needs_input rings", () => {
  // Same reasoning as the no-prior → ready case: approval-required terminal
  // is also a "your turn" state.
  expect(shouldRingForActivityTransition(undefined, summary("needs_input"))).toBe(true);
});

test("shouldRingForActivityTransition: error -> ready rings (recovery)", () => {
  // A new turn succeeded after a prior failure. Agent went from a
  // not-ready-for-input state to a ready-for-input state — that's the
  // signal we want to surface.
  expect(shouldRingForActivityTransition(summary("error"), summary("ready"))).toBe(true);
});

test("shouldRingForActivityTransition: streaming -> stopped does NOT ring", () => {
  // Stop is user-initiated; the user pressed the button, they know it stopped.
  expect(shouldRingForActivityTransition(summary("streaming"), summary("stopped"))).toBe(false);
});

test("shouldRingForActivityTransition: streaming -> stopping does NOT ring", () => {
  // Stop in progress; same reasoning as stopped — user-initiated.
  expect(shouldRingForActivityTransition(summary("streaming"), summary("stopping"))).toBe(false);
});

test("shouldRingForActivityTransition: stopping -> ready does NOT ring", () => {
  // The user pressed Stop; a stop-requested turn resolving to ready is expected.
  // Don't treat it as a "your turn" event.
  expect(shouldRingForActivityTransition(summary("stopping"), summary("ready"))).toBe(false);
});

test("shouldRingForActivityTransition: stopped -> ready does NOT ring", () => {
  // Same as stopping → ready: the user already knows they stopped the agent.
  expect(shouldRingForActivityTransition(summary("stopped"), summary("ready"))).toBe(false);
});

test("shouldRingForActivityTransition: streaming -> error does NOT ring", () => {
  // Matches today's working path: finalizeSdkRun only rang on terminal=done.
  // Errors are visible in-pane; not surfacing as audio keeps the sound
  // semantically narrow ("your turn now").
  expect(shouldRingForActivityTransition(summary("streaming"), summary("error"))).toBe(false);
});

test("shouldRingForActivityTransition: ready -> ready (no-op) does NOT ring", () => {
  expect(shouldRingForActivityTransition(summary("ready"), summary("ready"))).toBe(false);
});

test("shouldRingForActivityTransition: needs_input -> needs_input does NOT ring", () => {
  // Two approval requests in a row — already in "your turn", don't re-ring.
  expect(shouldRingForActivityTransition(summary("needs_input"), summary("needs_input"))).toBe(false);
});

test("shouldRingForActivityTransition: ready -> needs_input does NOT ring", () => {
  // Both are user-turn states; agent didn't transition OUT of user-turn first.
  expect(shouldRingForActivityTransition(summary("ready"), summary("needs_input"))).toBe(false);
});

test("shouldRingForActivityTransition: ready -> streaming does NOT ring", () => {
  // User submitted a new turn; agent is now working. Not "your turn."
  expect(shouldRingForActivityTransition(summary("ready"), summary("streaming"))).toBe(false);
});

test("shouldRingForActivityTransition: no prior -> streaming does NOT ring", () => {
  // First sighting but the new state is "agent working" — nothing for the
  // user to act on yet.
  expect(shouldRingForActivityTransition(undefined, summary("streaming"))).toBe(false);
});

test("shouldRingForActivityTransition: no prior -> error does NOT ring", () => {
  // First sighting but the new state is a failure — error toast / pill
  // covers visibility; sound stays scoped to user-action transitions.
  expect(shouldRingForActivityTransition(undefined, summary("error"))).toBe(false);
});

test("orderKeyAfter: numeric compare across digit boundary", () => {
  // The bug this guards against: lexicographic string compare says
  // "100" < "99", which made the sound-dedup gate suppress every
  // transition that crossed a power-of-ten ledger order_key boundary.
  expect(orderKeyAfter("100", "99")).toBe(true);
  expect(orderKeyAfter("99", "100")).toBe(false);
  expect(orderKeyAfter("1000", "999")).toBe(true);
  expect(orderKeyAfter("999", "1000")).toBe(false);
});

test("orderKeyAfter: same value is not after", () => {
  expect(orderKeyAfter("42", "42")).toBe(false);
});

test("orderKeyAfter: handles BIGSERIAL values past Number.MAX_SAFE_INTEGER", () => {
  // Long-running ledger forward compatibility — BIGSERIAL is int64.
  expect(orderKeyAfter("9223372036854775806", "9223372036854775805")).toBe(true);
});

test("orderKeyAfter: garbage input falls back to string compare", () => {
  // Defensive: backend always emits decimal strings, but we shouldn't
  // throw if some test or future schema change hands us a non-numeric.
  expect(orderKeyAfter("zzz", "aaa")).toBe(true);
  expect(orderKeyAfter("aaa", "zzz")).toBe(false);
});

test("non-chat sessions keep pod lifecycle status", () => {
  const activity = normalizeSessionActivity({
    session_id: "12",
    status: "streaming",
    unread_count: 2,
  });

  expect(sessionActivityDotStatus("Pending", false, activity ?? undefined)).toBe("pending");
  expect(sessionActivityStatusLabel("Pending", false, activity ?? undefined)).toBe("Pending");
});

test("normalizes scheduled status (not coerced to ready)", () => {
  const activity = normalizeSessionActivity({
    session_id: "63",
    status: "scheduled",
    unread_count: 0,
    needs_input: false,
    failed: false,
  });

  expect(activity?.status).toBe("scheduled");
  expect(sessionActivityDotStatus("Active", true, activity ?? undefined)).toBe("agent-scheduled");
  expect(sessionActivityStatusLabel("Active", true, activity ?? undefined)).toBe("Scheduled");
});

// The scheduled status is the sibling of needs_input: both are non-terminal
// pause-phases of a live (simulated) turn, decoupled from the backend's turn
// boundaries. They differ only in what resumes them — your input vs the clock —
// so needs_input summons and scheduled does not. The transitions below pin that
// the turn-complete bell stays silent while the agent parks itself and fires
// exactly once when the wake chain truly ends.
test("shouldRingForActivityTransition: streaming -> scheduled does NOT ring (self-parked agent)", () => {
  expect(shouldRingForActivityTransition(summary("streaming"), summary("scheduled"))).toBe(false);
});

test("shouldRingForActivityTransition: ready -> scheduled does NOT ring (armed a wake while idle)", () => {
  expect(shouldRingForActivityTransition(summary("ready"), summary("scheduled"))).toBe(false);
});

test("shouldRingForActivityTransition: no prior -> scheduled does NOT ring", () => {
  expect(shouldRingForActivityTransition(undefined, summary("scheduled"))).toBe(false);
});

test("shouldRingForActivityTransition: scheduled -> ready does NOT ring (cancel/clear)", () => {
  // A direct scheduled -> ready means the timer was cancelled or the user took
  // over the session — not the genuine end-of-chain hand-off, which arrives as
  // streaming -> ready when the woken turn finishes. Don't summon for the cancel.
  expect(shouldRingForActivityTransition(summary("scheduled"), summary("ready"))).toBe(false);
});

test("shouldRingForActivityTransition: away-error rings (broken self-resume)", () => {
  // A scheduled/background continuation the orchestrator could not fire while
  // the session was alive — the agent broke while you were away — rings the
  // same bell as a normal hand-off, even though ordinary errors stay silent.
  expect(shouldRingForActivityTransition(summary("scheduled"), summary("error", { away_error: true }))).toBe(true);
  expect(shouldRingForActivityTransition(summary("streaming"), summary("error", { away_error: true }))).toBe(true);
});

test("shouldRingForActivityTransition: ring user-turn set stays exactly {ready, needs_input} (guard)", () => {
  // Migration guard: the summon set must not silently grow. A new status (e.g.
  // scheduled) must NOT join the user-turn set — entering any non-{ready,
  // needs_input} status from working must stay silent. The one separate ring
  // path is away_error, pinned by its own tests above.
  const all: ConversationActivityStatus[] = [
    "ready",
    "submitted",
    "claimed",
    "streaming",
    "needs_input",
    "scheduled",
    "stopping",
    "stopped",
    "error",
  ];
  for (const next of all) {
    const rings = shouldRingForActivityTransition(summary("streaming"), summary(next));
    const expected = next === "ready" || next === "needs_input";
    expect(rings, `working -> ${next} should ${expected ? "" : "not "}ring`).toBe(expected);
  }
});

test("shouldRingForActivityTransition: away-error does not re-ring when already error", () => {
  expect(shouldRingForActivityTransition(
          summary("error", { away_error: true }),
          summary("error", { away_error: true }),
        )).toBe(false);
});

test("shouldRingForActivityTransition: scheduled -> needs_input rings (woke and asked you)", () => {
  expect(shouldRingForActivityTransition(summary("scheduled"), summary("needs_input"))).toBe(true);
});
