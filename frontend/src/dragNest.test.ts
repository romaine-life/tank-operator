import { describe, expect, test } from "vitest";
import {
  dropIntentForRow,
  placeSessionRelative,
  NEST_EDGE_FRACTION,
} from "./dragNest";

describe("dropIntentForRow", () => {
  const top = 100;
  const height = 40; // edges: top band [100,110], bottom band [130,140]

  test("top edge band reorders before", () => {
    expect(dropIntentForRow(100, top, height)).toBe("reorder-before");
    expect(dropIntentForRow(108, top, height)).toBe("reorder-before");
  });

  test("bottom edge band reorders after", () => {
    expect(dropIntentForRow(140, top, height)).toBe("reorder-after");
    expect(dropIntentForRow(132, top, height)).toBe("reorder-after");
  });

  test("middle band nests", () => {
    expect(dropIntentForRow(120, top, height)).toBe("nest");
    expect(dropIntentForRow(115, top, height)).toBe("nest");
    expect(dropIntentForRow(125, top, height)).toBe("nest");
  });

  test("boundaries are inclusive on the edges", () => {
    // exactly NEST_EDGE_FRACTION down → still reorder-before.
    expect(dropIntentForRow(top + height * NEST_EDGE_FRACTION, top, height)).toBe(
      "reorder-before",
    );
    // exactly (1 - NEST_EDGE_FRACTION) down → reorder-after.
    expect(
      dropIntentForRow(top + height * (1 - NEST_EDGE_FRACTION), top, height),
    ).toBe("reorder-after");
  });

  test("degenerate height falls back to nest", () => {
    expect(dropIntentForRow(100, 100, 0)).toBe("nest");
    expect(dropIntentForRow(100, 100, -5)).toBe("nest");
  });
});

describe("placeSessionRelative", () => {
  test("moves a row after a target", () => {
    expect(placeSessionRelative(["a", "b", "c", "d"], "d", "a", false)).toEqual([
      "a",
      "d",
      "b",
      "c",
    ]);
  });

  test("moves a row before a target", () => {
    expect(placeSessionRelative(["a", "b", "c", "d"], "a", "c", true)).toEqual([
      "b",
      "a",
      "c",
      "d",
    ]);
  });

  test("moves a later row before an earlier target", () => {
    expect(placeSessionRelative(["a", "b", "c"], "c", "a", true)).toEqual([
      "c",
      "a",
      "b",
    ]);
  });

  test("returns the same reference for a no-op move", () => {
    const order = ["a", "b", "c"];
    // moving b to after a is where it already is.
    expect(placeSessionRelative(order, "b", "a", false)).toBe(order);
    // moved === target.
    expect(placeSessionRelative(order, "a", "a", true)).toBe(order);
    // absent ids.
    expect(placeSessionRelative(order, "z", "a", true)).toBe(order);
    expect(placeSessionRelative(order, "a", "z", true)).toBe(order);
  });
});
