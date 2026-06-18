// Turn-activity load reconciliation.
//
// The Turns view shows one selected turn's activity body, loaded lazily through
// the per-turn `/activity` endpoint (`server_turn_activity_v3`) into a local,
// turn-id-keyed React state map. This module owns the single rule for *when a
// load should be running for the selected turn*, kept pure and unit-tested.
//
// The rule is deliberately **level-triggered** (reconcile desired-vs-actual
// whenever the selection or its load state changes) rather than edge-triggered
// (start a load only from specific user gestures), because the edge-triggered
// design it replaces stranded:
//
//   The selected turn id is set from several independent paths — explicit
//   gestures (select a turn, open a turn/page), the live-run follow, the
//   deep-link route match, the route-number resolver, and the "default to the
//   latest turn" fallback. Only the gesture/run paths also *started* a load.
//   The deep-link match, the route resolver, and the default-latest fallback
//   set selectedTurnId and left the activity load state `unloaded` (or absent
//   from the map entirely) with nothing in flight. The body then rendered
//   "Loading activity…" forever: a visible spinner with no in-flight request
//   and no edge left to re-fire — only a remount or an explicit click
//   recovered it. Production telemetry confirmed this is the dominant
//   "dead refresh": `turn-activity-selected-loading-stranded` (reason=absent)
//   fires while `turn-activity-selected-loading-slow` (a genuinely hung load)
//   stays at ~0, and the load lifecycle shows started≈succeeded with no
//   failed/timed-out tail. The strand was never a slow load; it was a load
//   that never started.
//
// This mirrors the directory-load reconcile design: a visible Turns pane with a
// selected, loadable turn ALWAYS has a load running until that turn reaches a
// terminal state, so no selection path — present or future — can strand.
// Concurrent-load dedup is enforced downstream by `turnActivityShouldStartLoad`
// (it refuses to start a second load while one is loading, or to reload an
// already-loaded page); this gate only decides desired intent. The truth table
// (turnActivityLoadReconcile.test.ts) pins that a visible Turns pane with a
// selected, non-terminal, not-loading turn ALWAYS resolves to "load", so a
// future change cannot quietly reintroduce the strand-without-recovery path.
//
// See docs/features/transcript-navigation/contract.md → "Live Behavior" and
// "Failure And Recovery".

import type { TurnActivityLoadStatus } from "./turnActivityState";

export type TurnActivityReconcileInput = {
  // The pane is on-screen AND the Turns surface is its active tab. A hidden
  // pane (the tabs view keeps non-routed session panes mounted) or a pane
  // parked on another tab does not eagerly load — it reconciles when it next
  // becomes the visible Turns surface. This is also why the stranded/mismatch
  // telemetry is gated on visibility: a hidden pane reading the live URL is not
  // a user-visible strand.
  active: boolean;
  // A turn is selected. False for an empty session, or before the route/default
  // selection has resolved a turn id. No selection → nothing to load.
  hasSelectedTurn: boolean;
  // The selected turn's load status, or `undefined` when the turn-id-keyed
  // load-state map has no entry for it yet. `undefined` is the `absent` strand
  // class: the selection paths that never started a load leave exactly this
  // (the map has no key), distinct from an explicit `unloaded` reset.
  status: TurnActivityLoadStatus | undefined;
};

export type TurnActivityReconcileDecision =
  | { action: "idle" }
  | { action: "load" };

// evaluateTurnActivityReconcile is the level-triggered desired-vs-actual rule.
// Desired state: a visible Turns pane with a selected turn has that turn's
// activity loaded (or genuinely in flight, or terminally errored). Pure, so the
// reconcile effect can call it on every relevant render and the truth table is
// directly testable.
//
//   active=false              → idle (hidden / non-Turns pane does not load)
//   no selected turn          → idle (nothing to load)
//   status=loaded             → idle (desired state reached)
//   status=error              → idle (terminal; Retry / re-select / a selection
//                                      change re-drives — never an auto-retry
//                                      hot loop against a failing endpoint)
//   status=loading            → idle (a load is genuinely running; a hung one
//                                      self-heals to `error` via the per-load
//                                      AbortController timeout, then Retry)
//   status=absent | unloaded  → LOAD (the strand class: a turn was selected but
//                                      no load was ever started for it)
export function evaluateTurnActivityReconcile(
  input: TurnActivityReconcileInput,
): TurnActivityReconcileDecision {
  const { active, hasSelectedTurn, status } = input;
  if (!active) return { action: "idle" };
  if (!hasSelectedTurn) return { action: "idle" };
  if (status === "loaded" || status === "error" || status === "loading") {
    return { action: "idle" };
  }
  // `undefined` (absent from the map) | "unloaded": a selected turn whose load
  // was never started. This is the dead-refresh strand — start the load.
  return { action: "load" };
}
