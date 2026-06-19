// Pure geometry + ordering helpers for the sidebar's zone-based session drag.
//
// A drop onto a session row means one of two things depending on WHERE in the
// row's height the pointer is: the top/bottom edge bands reorder the dragged
// row to just before/after the target (an insertion line), and the middle band
// nests the dragged row under the target (the row glows). This matches the
// Notion / OmniOutliner / file-tree convention the session-bar nesting adopts.
// See docs/features/session-bar/capabilities.md → manual-session-nesting.
//
// Kept DOM-free so the zone math and the relative placement are unit-tested
// without a renderer (dragNest.test.ts); App.tsx supplies the live pointer Y and
// the row's getBoundingClientRect().

export type DragIntentKind = "nest" | "reorder-before" | "reorder-after";

// Fraction of the row height at the top and at the bottom that reorders rather
// than nests. 0.25 leaves the middle half as the nest target — a comfortable,
// forgiving hit area while still making "drop above/below to reorder" reachable.
export const NEST_EDGE_FRACTION = 0.25;

// dropIntentForRow maps a pointer Y over a target row to a drag intent. The top
// NEST_EDGE_FRACTION reorders before the target, the bottom NEST_EDGE_FRACTION
// reorders after it, and the middle band nests under it. A degenerate (zero or
// negative) height falls back to nest.
export function dropIntentForRow(
  clientY: number,
  rowTop: number,
  rowHeight: number,
): DragIntentKind {
  if (rowHeight <= 0) return "nest";
  const rel = (clientY - rowTop) / rowHeight;
  if (rel <= NEST_EDGE_FRACTION) return "reorder-before";
  if (rel >= 1 - NEST_EDGE_FRACTION) return "reorder-after";
  return "nest";
}

// placeSessionRelative returns a new id order with movedId removed from its
// current slot and re-inserted immediately before (before=true) or after
// (before=false) targetId. It returns the input array unchanged when the move is
// a no-op (moved===target, either id absent, or the order already matches), so
// the caller can skip a redundant persist.
export function placeSessionRelative(
  order: readonly string[],
  movedId: string,
  targetId: string,
  before: boolean,
): string[] {
  if (movedId === targetId) return order as string[];
  if (!order.includes(movedId) || !order.includes(targetId)) {
    return order as string[];
  }
  const without = order.filter((id) => id !== movedId);
  const targetIndex = without.indexOf(targetId);
  const insertAt = before ? targetIndex : targetIndex + 1;
  const next = [...without];
  next.splice(insertAt, 0, movedId);
  // Preserve referential identity on a true no-op so callers can `=== order`.
  let changed = next.length !== order.length;
  if (!changed) {
    for (let i = 0; i < next.length; i += 1) {
      if (next[i] !== order[i]) {
        changed = true;
        break;
      }
    }
  }
  return changed ? next : (order as string[]);
}
