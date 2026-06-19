import { test, expect } from "vitest";

import { arrangeSessionTree, type SessionTreeInput } from "./sessionTree";

// Minimal fixture: a session is just an id plus the spawned-children refs the
// backend records on the *parent* row. Only `id` on each ref is load-bearing
// for nesting, so the fixtures keep refs to `{ id }`.
function s(id: string, spawnedIds: string[] = []): SessionTreeInput {
  return {
    id,
    spawned_sessions: spawnedIds.map((childId) => ({
      id: childId,
      name: `session ${childId}`,
      url: `https://tank.romaine.life/?session=${childId}`,
    })),
  };
}

function shape(input: SessionTreeInput[]) {
  return arrangeSessionTree(input).map((r) => ({
    id: r.session.id,
    depth: r.depth,
    parentId: r.parentId,
    isLastChild: r.isLastChild,
  }));
}

test("empty input returns empty", () => {
  expect(arrangeSessionTree([])).toEqual([]);
});

test("sessions with no lineage are all roots in input order", () => {
  expect(shape([s("3"), s("2"), s("1")])).toEqual([
    { id: "3", depth: 0, parentId: null, isLastChild: false },
    { id: "2", depth: 0, parentId: null, isLastChild: false },
    { id: "1", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("spawned children nest under their parent, last child marked for the elbow", () => {
  // Parent "10" spawned "11" then "12".
  expect(shape([s("10", ["11", "12"]), s("11"), s("12")])).toEqual([
    { id: "10", depth: 0, parentId: null, isLastChild: false },
    { id: "11", depth: 1, parentId: "10", isLastChild: false },
    { id: "12", depth: 1, parentId: "10", isLastChild: true },
  ]);
});

test("children are pulled under their parent regardless of their own position; root order is preserved", () => {
  // Input order scatters the children away from the parent and places a second
  // root ("20") between them. Children must regroup under "10"; roots "10" and
  // "20" keep their relative input order.
  expect(shape([s("12"), s("10", ["11", "12"]), s("20"), s("11")])).toEqual([
    { id: "10", depth: 0, parentId: null, isLastChild: false },
    // Children keep their input order: "12" appears before "11".
    { id: "12", depth: 1, parentId: "10", isLastChild: false },
    { id: "11", depth: 1, parentId: "10", isLastChild: true },
    { id: "20", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("grandchildren clamp to a single tier under the top-level ancestor", () => {
  // A(1) -> B(2) -> C(3). The UI must never indent twice: B and C are both
  // depth 1, grouped contiguously under A, but parentId still records the
  // *direct* spawner for diagnostics.
  const out = arrangeSessionTree([s("1", ["2"]), s("2", ["3"]), s("3")]);
  expect(out.map((r) => [r.session.id, r.depth, r.parentId])).toEqual([
    ["1", 0, null],
    ["2", 1, "1"],
    ["3", 1, "2"],
  ]);
  // Only the final descendant of the group terminates the connector.
  expect(out.map((r) => r.isLastChild)).toEqual([false, false, true]);
});

test("a child whose parent is not in the list is treated as a root", () => {
  // "11" would be a child of "10", but "10" is absent (deleted / different
  // scope). "11" has no present parent, so it renders as a root.
  expect(shape([s("11"), s("20")])).toEqual([
    { id: "11", depth: 0, parentId: null, isLastChild: false },
    { id: "20", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("spawned refs pointing outside the list (cross-scope test slot) create no phantom rows", () => {
  // Parent "10" lists a child "999" that is not in this scoped list. It must be
  // ignored — no phantom row, and "10" has no nested tier.
  expect(shape([s("10", ["999"])])).toEqual([
    { id: "10", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a child claimed by two parents nests under the first parent in input order", () => {
  expect(shape([s("1", ["3"]), s("2", ["3"]), s("3")])).toEqual([
    { id: "1", depth: 0, parentId: null, isLastChild: false },
    { id: "3", depth: 1, parentId: "1", isLastChild: true },
    { id: "2", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a self-referential spawn ref does not make a row its own parent", () => {
  expect(shape([s("1", ["1"])])).toEqual([
    { id: "1", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a malformed two-node cycle still emits every session exactly once", () => {
  // 1 -> 2 and 2 -> 1. There is no reachable root; the safety pass must still
  // emit both, once each, without recursing forever.
  const out = arrangeSessionTree([s("1", ["2"]), s("2", ["1"])]);
  expect(out).toHaveLength(2);
  expect(new Set(out.map((r) => r.session.id))).toEqual(new Set(["1", "2"]));
});

test("every input session appears exactly once across nesting shapes", () => {
  const input = [
    s("1", ["2", "3"]),
    s("2", ["4"]),
    s("3"),
    s("4"),
    s("5"),
  ];
  const out = arrangeSessionTree(input);
  expect(out).toHaveLength(input.length);
  expect(new Set(out.map((r) => r.session.id))).toEqual(
    new Set(["1", "2", "3", "4", "5"]),
  );
});

test("preserves the original session object identity for each row", () => {
  const parent = s("10", ["11"]);
  const child = s("11");
  const out = arrangeSessionTree([parent, child]);
  expect(out[0].session).toBe(parent);
  expect(out[1].session).toBe(child);
});
