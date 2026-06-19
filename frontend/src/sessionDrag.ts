// Pure session-sidebar drag decision: turn a committed drop (the moved row, the
// target row, and the zone under the pointer) into the durable consequence to
// persist. Kept out of the ~30k-line App component so the decision is unit-
// testable in isolation. App owns the DOM drag handlers — the minimal,
// known-good gesture — and calls planSessionDrop at drop time to choose
// nest-vs-reorder. The zone math itself lives in dragNest.ts.

import { arrangeSessionTree } from "./sessionTree";
import { placeSessionRelative, type DragIntentKind } from "./dragNest";

// The minimal session shape the drag decision needs. Structural so App's full
// Session and lightweight test fixtures both satisfy it.
export interface SessionDragInput {
  id: string;
  parent_session_id?: string | null;
  read_only_hidden?: boolean;
}

// The durable consequence of one drop: the moved row's new parent (null = root /
// un-nest) and the full id permutation to persist as sidebar order.
export interface DropDecision {
  movedId: string;
  /** Full visible+hidden id permutation for PUT /api/sessions/order. */
  nextOrder: string[];
  /** New parent_session_id for the moved row; null clears it (root). */
  newParentId: string | null;
  /** True when the parent edge changed (issue the parent write). */
  parentChanged: boolean;
  /** True when the order changed (issue the order write). */
  orderChanged: boolean;
}

// planSessionDrop turns (current rows, moved, target, zone) into the durable
// drop decision, or null for a no-op / invalid drop. It mirrors the on-screen
// arrangement (children grouped under origins) so a move lands where it was
// aimed, and leans on arrangeSessionTree as the single source of grouping truth:
// the stored parent is the literal target (nest) or the target's own parent
// (reorder), and the renderer clamps depth to one tier. Pure — no React, no DOM.
export function planSessionDrop(
  sessions: readonly SessionDragInput[],
  movedId: string,
  targetId: string,
  intent: DragIntentKind,
): DropDecision | null {
  if (!movedId || movedId === targetId) return null;

  const arranged = arrangeSessionTree(
    sessions.filter((session) => session.read_only_hidden !== true),
  );
  const target = arranged.find((a) => a.session.id === targetId);
  const moved = arranged.find((a) => a.session.id === movedId);
  if (!target || !moved) return null;

  // nest: under the target's group (literal target id; the renderer clamps
  // deeper lineage to one tier). reorder: join the target's level — its own
  // parent, null at the top level, so reordering a child beside a root un-nests.
  const newParentId: string | null =
    intent === "nest" ? targetId : target.parentId;
  const parentChanged = newParentId !== moved.parentId;

  const visibleOrder = arranged.map((a) => a.session.id);
  const hiddenOrder = sessions
    .filter((session) => session.read_only_hidden === true)
    .map((session) => session.id);
  const currentOrder = [...visibleOrder, ...hiddenOrder];
  const nextOrder = placeSessionRelative(
    currentOrder,
    movedId,
    targetId,
    intent === "reorder-before",
  );
  const orderChanged = nextOrder !== currentOrder;

  if (!parentChanged && !orderChanged) return null;
  return { movedId, nextOrder, newParentId, parentChanged, orderChanged };
}
