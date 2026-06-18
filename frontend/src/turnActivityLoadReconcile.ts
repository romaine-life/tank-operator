// Turn-activity load reconciliation.
//
// The Turns view shows one selected turn's activity body, loaded lazily through
// the per-turn `/activity` endpoint (`server_turn_activity_v3`) into a local,
// turn-id-keyed React state map. This module owns the two pure rules the body
// depends on, kept here so they have one definition and a directly-testable
// truth table:
//
//   1. `resolveDisplayedActivityTurn` — WHICH turn the body is showing.
//   2. `evaluateTurnActivityReconcile` — WHETHER a load should be running for it.
//
// ---------------------------------------------------------------------------
// Why both rules live here, and why the earlier "dead refresh" fixes bounced.
// ---------------------------------------------------------------------------
//
// The activity body renders the *displayed* turn, which is NOT always the
// routed/selected id. The render resolves it with a fallback:
//
//     selected = turns.find(t => t.turnId === selectedTurnId) ?? turns.at(-1)
//
// i.e. when the selected id is not present in the loaded directory window, the
// body falls back to the latest turn. The selected id legitimately diverges
// from the directory during route-number / anchor resolution: a deep-link or a
// refresh on `/sessions/{id}/turns/{n}` keeps the unresolved id in
// `effectiveSelectedTurnId` (so the URL stays stable) while that turn is not
// yet in the windowed directory — so the body shows the latest turn instead.
//
// Every prior load trigger keyed off `effectiveSelectedTurnId`, NOT the
// displayed turn. So in the divergence case the loader fetched the off-screen
// id while the body showed the latest turn with *no load ever started for it*:
// "Loading activity…" forever, recovered only by a remount or an explicit
// click. Production telemetry confirmed the class — the stranded signal
// (`turn-activity-selected-loading-stranded`, reason=absent) fires keyed on the
// DISPLAYED turn while the slow-load signal stays ~0 and the load lifecycle
// shows started≈succeeded with no failed/timed-out tail. It was never a slow
// load; it was a load that never started, for the turn actually on screen.
//
// The four prior fixes each added another *edge* trigger (tab reactivation,
// approval-follow) or re-keyed the same `effectiveSelectedTurnId` load, so they
// never closed the divergence. The fix is to make BOTH the render and the load
// resolve the displayed turn through the SAME function, then keep a
// level-triggered gate so a visible Turns pane with a displayed, non-terminal,
// not-loading turn ALWAYS has a load running. With one displayed-turn source of
// truth, no selection path — present or future — can show a turn the loader
// isn't reconciling.
//
// See docs/features/transcript-navigation/contract.md → "Live Behavior" and
// "Failure And Recovery".

import type { TurnActivityLoadStatus } from "./turnActivityState";

// resolveDisplayedActivityTurn is the single source of truth for which turn the
// activity body shows. It MUST stay identical to what the body renders, because
// the load reconciler keys off it: if the body shows turn X, the loader must
// load turn X. The fallback to the latest turn mirrors the render — when the
// selected id is not in the (windowed) directory, the body shows the latest
// turn, so that is the turn that must load.
export function resolveDisplayedActivityTurn<T extends { turnId: string }>(
  turns: readonly T[],
  selectedTurnId: string | null,
): T | null {
  if (turns.length === 0) return null;
  const exact = selectedTurnId
    ? turns.find((turn) => turn.turnId === selectedTurnId)
    : undefined;
  return exact ?? turns[turns.length - 1] ?? null;
}

export type TurnActivityReconcileInput = {
  // The pane is on-screen AND the Turns surface is its active tab. A hidden
  // pane (the tabs view keeps non-routed session panes mounted) or a pane
  // parked on another tab does not eagerly load — it reconciles when it next
  // becomes the visible Turns surface. This is also why the stranded/mismatch
  // telemetry is gated on visibility: a hidden pane reading the live URL is not
  // a user-visible strand.
  active: boolean;
  // A turn is displayed. False for an empty session (no turns to fall back to).
  // Callers pass `resolveDisplayedActivityTurn(...) != null` here so the gate
  // and the render agree on whether anything is on screen.
  hasDisplayedTurn: boolean;
  // The displayed turn's load status, or `undefined` when the turn-id-keyed
  // load-state map has no entry for it yet. `undefined` is the `absent` strand
  // class: the selection paths that never started a load for the displayed turn
  // leave exactly this (the map has no key), distinct from an explicit
  // `unloaded` reset.
  status: TurnActivityLoadStatus | undefined;
};

export type TurnActivityReconcileDecision =
  | { action: "idle" }
  | { action: "load" };

// evaluateTurnActivityReconcile is the level-triggered desired-vs-actual rule.
// Desired state: a visible Turns pane with a displayed turn has that turn's
// activity loaded (or genuinely in flight, or terminally errored). Pure, so the
// reconcile effect can call it on every relevant render and the truth table is
// directly testable.
//
//   active=false              → idle (hidden / non-Turns pane does not load)
//   no displayed turn         → idle (nothing to load)
//   status=loaded             → idle (desired state reached)
//   status=error              → idle (terminal; Retry / re-select / a selection
//                                      change re-drives — never an auto-retry
//                                      hot loop against a failing endpoint)
//   status=loading            → idle (a load is genuinely running; a hung one
//                                      self-heals to `error` via the per-load
//                                      AbortController timeout, then Retry)
//   status=absent | unloaded  → LOAD (the strand class: a turn is displayed but
//                                      no load was ever started for it)
export function evaluateTurnActivityReconcile(
  input: TurnActivityReconcileInput,
): TurnActivityReconcileDecision {
  const { active, hasDisplayedTurn, status } = input;
  if (!active) return { action: "idle" };
  if (!hasDisplayedTurn) return { action: "idle" };
  if (status === "loaded" || status === "error" || status === "loading") {
    return { action: "idle" };
  }
  // `undefined` (absent from the map) | "unloaded": a displayed turn whose load
  // was never started. This is the dead-refresh strand — start the load.
  return { action: "load" };
}
