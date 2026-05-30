// Durable-order placement for the running "Thinking…" placeholder.
//
// The active-turn placeholder is not a durable transcript row of its own; it
// is synthesized client-side from the server-projected `turn_activity` shell.
// Its *position*, however, must follow the same durable `order_key` ordering
// as every settled row — never a browser-local heuristic. The transcript the
// renderer groups is already sorted by `order_key`
// (`projectedTranscriptEntryOrderKey`), so the only question is where to splice
// the placeholder into that ordered list.
//
// History: PR #732 ("Fix active thinking row placement") replaced an earlier
// "splice at the shell's turn-start order" rule — which let the placeholder
// overtake a later answered AskUserQuestion handoff for the same turn — with a
// structural rule: "splice right after the latest visible row carrying the
// same turnId." That fixed the handoff case but regressed a different one:
// session-lifecycle notices ("Session is loading." / "Session is ready.") are
// durable `session.status` rows with **no turnId**, so on a new session's
// first turn the only turn-tagged row is the user's message and the
// placeholder landed *above* the notices even though the turn's activity
// (and therefore the placeholder) belongs below them in durable order.
//
// This module restores a single durable-order rule that satisfies both cases:
// place the placeholder at the turn's **live-tail order key** — the latest
// durable order key the turn has reached, whether that lives in the shell's
// compacted activity (`endOrderKey`) or in a turn-tagged row that stays in the
// main transcript (an answered AskUserQuestion handoff). The placeholder then
// sorts after the loading/ready notices (their keys precede the turn's
// activity) and after the handoff (its key is later than the shell's), with no
// dependence on which rows happen to carry the turnId.

export interface ThinkingPlacementGroup {
  /**
   * The group's representative durable order key, or "" when the group
   * carries no order key (local-only optimistic rows that the server has not
   * yet confirmed). Groups are already in ascending order-key order.
   */
  orderKey: string;
  /** Whether this group belongs to the placeholder's active turn. */
  includesTurn: boolean;
}

function clampIndex(index: number, length: number): number {
  if (!Number.isFinite(index)) return length;
  return Math.min(Math.max(Math.trunc(index), 0), length);
}

// resolveThinkingInsertIndex returns the index at which to splice the running
// placeholder into `groups`.
//
// `shellTailOrderKey` is the turn-activity shell's live-tail order key
// (`endOrderKey ?? startOrderKey ?? orderKey`); it covers activity rows folded
// into the shell. `fallbackIndex` is the shell's own stream position, used only
// when no durable order key is available anywhere (fully local-only fixtures).
export function resolveThinkingInsertIndex(
  groups: ThinkingPlacementGroup[],
  shellTailOrderKey: string,
  fallbackIndex: number,
): number {
  // The turn's live tail: the furthest durable order key the turn has reached,
  // across both the shell's compacted activity and any turn-tagged row that
  // stays in the main transcript.
  let tail = shellTailOrderKey ?? "";
  for (const group of groups) {
    if (group.includesTurn && group.orderKey && (tail === "" || group.orderKey > tail)) {
      tail = group.orderKey;
    }
  }

  if (tail === "") {
    // No durable order key anywhere — keep placement deterministic for
    // local-only rows by anchoring to the latest turn-tagged group, else the
    // shell's stream position. This is not an old-behavior compatibility path:
    // it only triggers when every candidate row genuinely lacks an order key.
    let latestTurnGroupIndex = -1;
    for (let index = 0; index < groups.length; index += 1) {
      if (groups[index]!.includesTurn) latestTurnGroupIndex = index;
    }
    return latestTurnGroupIndex >= 0
      ? latestTurnGroupIndex + 1
      : clampIndex(fallbackIndex, groups.length);
  }

  // Splice after the last group whose durable order key is at or before the
  // turn's live tail. Groups are ascending by order key, so this lands the
  // placeholder at its chronological position.
  let insertIndex = -1;
  for (let index = 0; index < groups.length; index += 1) {
    const key = groups[index]!.orderKey;
    if (key && key <= tail) insertIndex = index;
  }
  return insertIndex >= 0 ? insertIndex + 1 : clampIndex(fallbackIndex, groups.length);
}
