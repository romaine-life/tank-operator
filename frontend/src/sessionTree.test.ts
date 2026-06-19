import { test, expect } from "vitest";

import { arrangeSessionTree, type SessionTreeInput } from "./sessionTree";

// Minimal fixture: a session is just an id plus the durable child→parent
// pointer the backend stamps on the child row at create (parent_session_id).
function s(id: string, parentId?: string): SessionTreeInput {
  return { id, parent_session_id: parentId };
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

test("sessions with no parent pointer are all roots in input order", () => {
  expect(shape([s("3"), s("2"), s("1")])).toEqual([
    { id: "3", depth: 0, parentId: null, isLastChild: false },
    { id: "2", depth: 0, parentId: null, isLastChild: false },
    { id: "1", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("spawned children nest under their parent, last child marked for the elbow", () => {
  // "11" and "12" were both spawned by "10".
  expect(shape([s("10"), s("11", "10"), s("12", "10")])).toEqual([
    { id: "10", depth: 0, parentId: null, isLastChild: false },
    { id: "11", depth: 1, parentId: "10", isLastChild: false },
    { id: "12", depth: 1, parentId: "10", isLastChild: true },
  ]);
});

test("children are pulled under their parent regardless of their own position; root order is preserved", () => {
  // Input order scatters the children away from the parent and places a second
  // root ("20") between them. Children must regroup under "10"; roots "10" and
  // "20" keep their relative input order.
  expect(shape([s("12", "10"), s("10"), s("20"), s("11", "10")])).toEqual([
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
  const out = arrangeSessionTree([s("1"), s("2", "1"), s("3", "2")]);
  expect(out.map((r) => [r.session.id, r.depth, r.parentId])).toEqual([
    ["1", 0, null],
    ["2", 1, "1"],
    ["3", 1, "2"],
  ]);
  // Only the final descendant of the group terminates the connector.
  expect(out.map((r) => r.isLastChild)).toEqual([false, false, true]);
});

test("a child whose parent is not in the list is treated as a root", () => {
  // "11" points at "10", but "10" is absent (deleted / different scope). "11"
  // has no present parent, so it renders as a root.
  expect(shape([s("11", "10"), s("20")])).toEqual([
    { id: "11", depth: 0, parentId: null, isLastChild: false },
    { id: "20", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a cross-scope parent pointer (test slot spawned from prod) does not nest", () => {
  // The child carries a parent_session_id from another scope ("999") that is
  // not in this scoped list, so it renders as a root — never a phantom nest.
  expect(shape([s("10", "999")])).toEqual([
    { id: "10", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a self-referential parent pointer does not make a row its own parent", () => {
  expect(shape([s("1", "1")])).toEqual([
    { id: "1", depth: 0, parentId: null, isLastChild: false },
  ]);
});

test("a malformed two-node cycle still emits every session exactly once", () => {
  // 1's parent is 2 and 2's parent is 1. There is no reachable root; the safety
  // pass must still emit both, once each, without recursing forever.
  const out = arrangeSessionTree([s("1", "2"), s("2", "1")]);
  expect(out).toHaveLength(2);
  expect(new Set(out.map((r) => r.session.id))).toEqual(new Set(["1", "2"]));
});

test("every input session appears exactly once across nesting shapes", () => {
  const input = [
    s("1"),
    s("2", "1"),
    s("3", "1"),
    s("4", "2"),
    s("5"),
  ];
  const out = arrangeSessionTree(input);
  expect(out).toHaveLength(input.length);
  expect(new Set(out.map((r) => r.session.id))).toEqual(
    new Set(["1", "2", "3", "4", "5"]),
  );
});

test("preserves the original session object identity for each row", () => {
  const parent = s("10");
  const child = s("11", "10");
  const out = arrangeSessionTree([parent, child]);
  expect(out[0].session).toBe(parent);
  expect(out[1].session).toBe(child);
});
