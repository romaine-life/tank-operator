import { describe, test, expect } from "vitest";

import {
  buildTurnViewItems,
  mergeTurnDirectoryWithLiveShells,
} from "./App.tsx";
import type { TranscriptEntry } from "./App.tsx";

// A durable turn-directory shell row, as /turns/directory returns it (the same
// shape /timeline emits for a collapsed turn_activity row).
function shell(
  turnId: string,
  turnNumber: number,
  activity: Record<string, unknown> = { status: "completed" },
): TranscriptEntry {
  return {
    id: `row-${turnId}`,
    kind: "turn_activity",
    turnId,
    turnNumber,
    activity: { turnId, ...activity },
  } as unknown as TranscriptEntry;
}

describe("durable turn directory feeds the Turns selector", () => {
  test("lists every turn from the directory, including ones the chat window never loaded", () => {
    // The session has 5 turns; the bounded chat window only ever held the tail.
    // Sourced from the directory, the selector still lists Turn 1.
    const directory = [1, 2, 3, 4, 5].map((n) => shell(`turn_${n}`, n));
    const items = buildTurnViewItems(directory, null, {}, "claude", 200_000);
    expect(items.map((t) => t.label)).toEqual([
      "Turn 1",
      "Turn 2",
      "Turn 3",
      "Turn 4",
      "Turn 5",
    ]);
    expect(items.map((t) => t.turnId)).toEqual([
      "turn_1",
      "turn_2",
      "turn_3",
      "turn_4",
      "turn_5",
    ]);
  });

  test("overlays live shells onto the directory set; a non-shell row adds no turn", () => {
    const directory = [shell("turn_1", 1), shell("turn_2", 2), shell("turn_3", 3)];
    const liveActive = shell("turn_3", 3, { status: "active", active: true });
    const live: TranscriptEntry[] = [
      liveActive,
      // A plain message row in the live window must not introduce a turn.
      { id: "msg-x", kind: "message", role: "assistant", turnId: "turn_3" } as unknown as TranscriptEntry,
    ];
    const merged = mergeTurnDirectoryWithLiveShells(directory, live);
    expect(merged.map((e) => e.turnId)).toEqual(["turn_1", "turn_2", "turn_3"]);
    // turn_3 comes from the live overlay (fresh/active); 1-2 from the directory.
    expect(merged[2]).toBe(liveActive);
    expect(merged[0]).toBe(directory[0]);
  });

  test("an empty directory yields no turns — never a silent fall-back to the window", () => {
    const live = [shell("turn_99", 99)];
    expect(mergeTurnDirectoryWithLiveShells([], live)).toEqual([]);
  });

  test("the active in-flight turn is appended before the directory lists it, with a neutral label", () => {
    const directory = [shell("turn_1", 1), shell("turn_2", 2)];
    // A just-submitted turn is active but not yet in the durable directory.
    const items = buildTurnViewItems(directory, "turn_3", {}, "claude", 200_000);
    expect(items.map((t) => t.turnId)).toEqual(["turn_1", "turn_2", "turn_3"]);
    expect(items[2].label).toBe("Current turn");
    expect(items[2].active).toBe(true);
  });
});
