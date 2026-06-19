import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, test, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import {
  planSessionDrop,
  useSessionDrag,
  type DropDecision,
  type SessionDragInput,
} from "./sessionDrag";

afterEach(cleanup);

// Regression guard for the real-browser drag bug the #1358 telemetry caught:
// `dragstart` fired but `dragover` never did, because the onDragOver guard read
// `draggingSessionId` (React state) which isn't flushed before the next native
// dragover under concurrent rendering, so it bailed before preventDefault. jsdom
// flushes state between fireEvent calls, so a behavioral test can't reproduce
// the staleness — this pins the fix at the source: the guard must read the
// synchronous ref, never the state.
test("onDragOver guards on the synchronous draggingIdRef, not React state", () => {
  const src = readFileSync(join(import.meta.dirname, "sessionDrag.ts"), "utf8");
  // Slice the actual handler (its `(event) => {` body), not the interface decl.
  const onDragOver = src.slice(
    src.indexOf("onDragOver: (event) => {"),
    src.indexOf("onDrop: (event) => {"),
  );
  expect(
    onDragOver,
    "onDragOver must gate on draggingIdRef.current (survives an unflushed dragstart state update)",
  ).toMatch(/draggingIdRef\.current/);
  expect(
    onDragOver.includes("draggingSessionId"),
    "onDragOver must not gate on the draggingSessionId state (the stale-closure bug)",
  ).toBe(false);
});

// ---- pure decision ---------------------------------------------------------

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

// ---- DOM integration: real drag events drive the handlers ------------------

function makeDataTransfer() {
  const store = new Map<string, string>();
  return {
    effectAllowed: "",
    dropEffect: "",
    setData: (k: string, v: string) => store.set(k, String(v)),
    getData: (k: string) => store.get(k) ?? "",
  };
}

function Harness({
  sessions,
  onDrop,
  readOnly = false,
  enabled = true,
}: {
  sessions: SessionDragInput[];
  onDrop: (plan: DropDecision) => void;
  readOnly?: boolean;
  enabled?: boolean;
}) {
  const drag = useSessionDrag({ sessions, readOnly, enabled, onDrop });
  return (
    <ul>
      {sessions.map((s) => (
        <li
          key={s.id}
          data-testid={`row-${s.id}`}
          data-dragging={drag.draggingSessionId === s.id}
          data-over={drag.dragOverSessionId === s.id}
          draggable
          {...drag.rowHandlers(s.id)}
        />
      ))}
    </ul>
  );
}

describe("useSessionDrag DOM wiring", () => {
  test("dragging A and dropping on B fires onDrop with the plan", () => {
    const onDrop = vi.fn();
    render(
      <Harness sessions={[{ id: "a" }, { id: "b" }, { id: "c" }]} onDrop={onDrop} />,
    );
    const a = screen.getByTestId("row-a");
    const b = screen.getByTestId("row-b");
    const dt = makeDataTransfer();

    fireEvent.dragStart(a, { dataTransfer: dt });
    // After dragstart the dragged row is marked, so dragover on B is a valid drop.
    expect(a.getAttribute("data-dragging")).toBe("true");
    fireEvent.dragOver(b, { dataTransfer: dt, clientY: 10 });
    fireEvent.drop(b, { dataTransfer: dt, clientY: 10 });

    expect(onDrop).toHaveBeenCalledTimes(1);
    const plan = onDrop.mock.calls[0][0] as DropDecision;
    expect(plan.movedId).toBe("a");
    // jsdom getBoundingClientRect is all-zeros → height<=0 → "nest" zone.
    expect(plan.newParentId).toBe("b");
  });

  test("dragover preventDefault is what enables the drop (the drop-target contract)", () => {
    const onDrop = vi.fn();
    render(<Harness sessions={[{ id: "a" }, { id: "b" }]} onDrop={onDrop} />);
    const a = screen.getByTestId("row-a");
    const b = screen.getByTestId("row-b");
    const dt = makeDataTransfer();

    fireEvent.dragStart(a, { dataTransfer: dt });
    const over = fireEvent.dragOver(b, { dataTransfer: dt, clientY: 10 });
    // fireEvent returns false when a handler called preventDefault.
    expect(over).toBe(false);
  });

  test("read-only and disabled views never fire onDrop", () => {
    const onDrop = vi.fn();
    const { rerender } = render(
      <Harness sessions={[{ id: "a" }, { id: "b" }]} onDrop={onDrop} readOnly />,
    );
    const dt = makeDataTransfer();
    fireEvent.dragStart(screen.getByTestId("row-a"), { dataTransfer: dt });
    fireEvent.dragOver(screen.getByTestId("row-b"), { dataTransfer: dt, clientY: 10 });
    fireEvent.drop(screen.getByTestId("row-b"), { dataTransfer: dt, clientY: 10 });
    expect(onDrop).not.toHaveBeenCalled();

    rerender(
      <Harness
        sessions={[{ id: "a" }, { id: "b" }]}
        onDrop={onDrop}
        enabled={false}
      />,
    );
    fireEvent.dragStart(screen.getByTestId("row-a"), { dataTransfer: dt });
    fireEvent.dragOver(screen.getByTestId("row-b"), { dataTransfer: dt, clientY: 10 });
    fireEvent.drop(screen.getByTestId("row-b"), { dataTransfer: dt, clientY: 10 });
    expect(onDrop).not.toHaveBeenCalled();
  });
});
