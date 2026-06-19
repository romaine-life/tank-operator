// Sidebar session nesting (single tier).
//
// When an agent spawns a session (spawn_run_session), the backend records the
// parent→child link on the *origin* session's durable `sessions.spawned_sessions`
// row (see docs/features/session-bar/capabilities.md → "spawned-sessions-chip").
// This module turns that durable lineage into a sidebar render order: each
// spawned child is grouped directly under its origin and marked as one indented
// tier so a running sub-session is impossible to lose track of.
//
// Design decision — exactly ONE indent level. A session is either a root
// (depth 0) or a nested child (depth 1). If a sub-session itself spawns
// sessions ("grandchildren"), they are clamped to the same single tier and
// grouped under their top-level ancestor; the sidebar never indents twice.
//
// This is pure presentation over data that already ships. It introduces no new
// durable state and reads only the parent row's `spawned_sessions`, so it
// converges over the session-list SSE exactly like the spawned-sessions chip:
// a freshly created child renders as a root until the parent row's
// `spawned_sessions` includes it, then snaps under the parent. Cross-scope
// test-slot children are excluded for free — they live in a different
// `session_scope`, never appear in this (email, scope)-scoped list, and so are
// never present to nest.

import type { SpawnedSessionRef } from "./spawnedSessions";

// The minimal shape this module needs from a session row. Kept structural (not
// the full App `Session`) so lightweight test fixtures satisfy it too.
export interface SessionTreeInput {
  id: string;
  spawned_sessions?: SpawnedSessionRef[];
}

export interface ArrangedSession<T extends SessionTreeInput> {
  session: T;
  // 0 = root / top-level, 1 = nested child. Capped at 1 by design.
  depth: 0 | 1;
  // The id of the *direct* spawning session when that session is present in
  // the same list, else null. Diagnostic only — the visual tier is `depth`,
  // which is clamped to 1 even for deeper lineage. Roots are null.
  parentId: string | null;
  // True only for the last nested row of a top-level ancestor's group, so the
  // connector renders an elbow (└) and the vertical guide terminates there;
  // earlier nested rows render a tee (├). Always false for roots.
  isLastChild: boolean;
}

// arrangeSessionTree re-orders an already-sorted sidebar session list so that
// each spawned child is grouped under its origin, annotated with the single
// nesting tier. Root order and within-group order are preserved from the input
// (the durable sidebar_position sort), so this layers nesting on top of the
// user's drag order without owning it. Every input session appears exactly once
// in the output, even under malformed (cyclic / self-referential) lineage.
export function arrangeSessionTree<T extends SessionTreeInput>(
  sessions: readonly T[],
): ArrangedSession<T>[] {
  if (sessions.length === 0) return [];

  const byId = new Map<string, T>();
  for (const s of sessions) {
    // First occurrence wins; ids are unique in practice.
    if (!byId.has(s.id)) byId.set(s.id, s);
  }

  // parentOf[child] = the spawning session's id, but only when that parent is
  // present in this list. First parent to claim a child wins (a child has one
  // origin; this is just defensive against duplicate refs). Self-references are
  // ignored so a row can't be its own parent.
  const parentOf = new Map<string, string>();
  for (const parent of sessions) {
    const refs = parent.spawned_sessions;
    if (!refs || refs.length === 0) continue;
    for (const ref of refs) {
      const childId = ref?.id;
      if (!childId || childId === parent.id) continue;
      if (!byId.has(childId)) continue; // cross-scope / not in this list
      if (parentOf.has(childId)) continue; // already claimed
      parentOf.set(childId, parent.id);
    }
  }

  // childrenOf[parent] = direct children in input order.
  const childrenOf = new Map<string, string[]>();
  for (const s of sessions) {
    const parentId = parentOf.get(s.id);
    if (parentId === undefined) continue;
    const bucket = childrenOf.get(parentId);
    if (bucket) bucket.push(s.id);
    else childrenOf.set(parentId, [s.id]);
  }

  const result: ArrangedSession<T>[] = [];
  const emitted = new Set<string>();

  // Pre-order DFS collecting every descendant of `id`. `emitted` is the single
  // guard shared with emitRoot, so a malformed cycle can never double-emit or
  // recurse forever.
  const collectDescendants = (id: string, out: string[]): void => {
    const kids = childrenOf.get(id);
    if (!kids) return;
    for (const kid of kids) {
      if (emitted.has(kid)) continue;
      emitted.add(kid);
      out.push(kid);
      collectDescendants(kid, out);
    }
  };

  const emitRoot = (rootId: string): void => {
    if (emitted.has(rootId)) return;
    emitted.add(rootId);
    result.push({
      session: byId.get(rootId) as T,
      depth: 0,
      parentId: null,
      isLastChild: false,
    });
    const descendants: string[] = [];
    collectDescendants(rootId, descendants);
    const lastIndex = descendants.length - 1;
    descendants.forEach((id, index) => {
      result.push({
        session: byId.get(id) as T,
        depth: 1,
        parentId: parentOf.get(id) ?? null,
        isLastChild: index === lastIndex,
      });
    });
  };

  // Real roots first (preserves their order), then a safety pass so any session
  // trapped in a cycle (no reachable root) is still emitted exactly once.
  for (const s of sessions) {
    if (!parentOf.has(s.id)) emitRoot(s.id);
  }
  for (const s of sessions) {
    if (!emitted.has(s.id)) emitRoot(s.id);
  }

  return result;
}
