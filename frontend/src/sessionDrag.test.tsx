import { describe, expect, test } from "vitest";

import { planSessionDrop, type SessionDragInput } from "./sessionDrag";

// planSessionDrop is the pure drop decision App calls at drop time: given the
// current rows, the moved row, the target, and the zone under the pointer, it
// returns the durable consequence (new parent + full order permutation) or null
// for a no-op. The DOM gesture itself (dragstart/dragover/drop) lives inline in
// App and is observed live via tank_session_drag_step_total; here we pin the
// decision logic that turns a drop into what gets persisted.
describe("planSessionDrop", () => {
  const roots: SessionDragInput[] = [{ id: "a" }, { id: "b" }, { id: "c" }];

  test("nest stores the literal target as parent and reorders adjacent", () => {
    const plan = planSessionDrop(roots, "c", "a", "nest");
    expect(plan).not.toBeNull();
    expect(plan?.newParentId).toBe("a");
    expect(plan?.parentChanged).toBe(true);
    expect(plan?.nextOrder).toEqual(["a", "c", "b"]);
  });

  test("reorder keeps the target's level (root → parent stays null)", () => {
    const plan = planSessionDrop(roots, "c", "a", "reorder-before");
    expect(plan?.newParentId).toBeNull();
    expect(plan?.parentChanged).toBe(false);
    expect(plan?.nextOrder).toEqual(["c", "a", "b"]);
  });

  test("reorder-after a root un-nests a child (parent → null)", () => {
    const withChild: SessionDragInput[] = [
      { id: "a" },
      { id: "b", parent_session_id: "a" },
      { id: "c" },
    ];
    const plan = planSessionDrop(withChild, "b", "c", "reorder-after");
    expect(plan?.newParentId).toBeNull();
    expect(plan?.parentChanged).toBe(true);
  });

  test("self-drop and unknown ids are no-ops", () => {
    expect(planSessionDrop(roots, "a", "a", "nest")).toBeNull();
    expect(planSessionDrop(roots, "zz", "a", "nest")).toBeNull();
    expect(planSessionDrop(roots, "a", "zz", "nest")).toBeNull();
  });
});
