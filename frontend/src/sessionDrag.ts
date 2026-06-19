// Session sidebar drag-and-drop orchestration, extracted from App.tsx so the
// event→decision→persist flow is unit- AND integration-testable in isolation.
//
// Why this exists as its own module: the drag handlers used to live inline in
// the ~30k-line App component with only the pure zone math (dragNest.ts) under
// test. That left the actual wiring — does firing dragstart→dragover→drop on a
// real row produce the right persist call? — unobserved, so a regression could
// (and did) ship silently. `planSessionDrop` is the pure decision; the
// `useSessionDrag` hook owns the drag state and the DOM handlers and is rendered
// against in sessionDrag.test.tsx with real DragEvents.

import { useState } from "react";
import type { DragEvent as ReactDragEvent } from "react";

import { arrangeSessionTree } from "./sessionTree";
import {
  dropIntentForRow,
  placeSessionRelative,
  type DragIntentKind,
} from "./dragNest";

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

export interface SessionRowDragHandlers {
  onDragStart: (event: ReactDragEvent<HTMLElement>) => void;
  onDragOver: (event: ReactDragEvent<HTMLElement>) => void;
  onDrop: (event: ReactDragEvent<HTMLElement>) => void;
  onDragEnd: () => void;
}

export interface UseSessionDragOptions {
  /** Current visible+hidden session list, used to compute the drop plan. */
  sessions: readonly SessionDragInput[];
  /** Whole list is a read-only/cross-scope view: drag is disabled. */
  readOnly: boolean;
  /** Drag is otherwise enabled (e.g. an authenticated user is present). */
  enabled: boolean;
  /** Apply the durable + optimistic consequences of a committed drop. */
  onDrop: (plan: DropDecision) => void;
}

export interface UseSessionDragResult {
  draggingSessionId: string | null;
  dragOverSessionId: string | null;
  dragIntent: DragIntentKind | null;
  rowHandlers: (id: string) => SessionRowDragHandlers;
}

// useSessionDrag owns the transient drag state and returns per-row DOM handlers.
// The persist/optimistic side effects are injected via onDrop so this hook stays
// rendering-pure and testable; App wires onDrop to the order/parent writes.
export function useSessionDrag(
  opts: UseSessionDragOptions,
): UseSessionDragResult {
  const [draggingSessionId, setDraggingSessionId] = useState<string | null>(
    null,
  );
  const [dragOverSessionId, setDragOverSessionId] = useState<string | null>(
    null,
  );
  const [dragIntent, setDragIntent] = useState<DragIntentKind | null>(null);

  const end = () => {
    setDraggingSessionId(null);
    setDragOverSessionId(null);
    setDragIntent(null);
  };

  const rowHandlers = (id: string): SessionRowDragHandlers => ({
    onDragStart: (event) => {
      if (opts.readOnly) return;
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", id);
      setDraggingSessionId(id);
      setDragOverSessionId(id);
    },
    onDragOver: (event) => {
      if (opts.readOnly) return;
      if (!draggingSessionId || draggingSessionId === id) return;
      // Calling preventDefault on dragover is what makes the row a valid drop
      // target; without it the browser never fires drop.
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      setDragOverSessionId(id);
      setDragIntent(dropIntentForRow(event.clientY, rect.top, rect.height));
    },
    onDrop: (event) => {
      event.preventDefault();
      // Recompute the zone from the drop position so the decision never rides on
      // possibly-stale hover state.
      const rect = event.currentTarget.getBoundingClientRect();
      const intent = dropIntentForRow(event.clientY, rect.top, rect.height);
      const movedId =
        event.dataTransfer.getData("text/plain") || draggingSessionId || "";
      end();
      if (opts.readOnly || !opts.enabled || !movedId) return;
      const plan = planSessionDrop(opts.sessions, movedId, id, intent);
      if (plan) opts.onDrop(plan);
    },
    onDragEnd: end,
  });

  return { draggingSessionId, dragOverSessionId, dragIntent, rowHandlers };
}
