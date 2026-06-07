import { test, expect } from "vitest";
import {
  addBugLabelName,
  filterBugLabelSuggestions,
  normalizeBugLabelDisplayName,
} from "./bugLabels";

test("normalizeBugLabelDisplayName removes the redundant bug prefix", () => {
  expect(normalizeBugLabelDisplayName("  bug:   Slow checkout  ")).toBe("Slow checkout");
  expect(normalizeBugLabelDisplayName("Transcript")).toBe("Transcript");
});

test("addBugLabelName stores display names without the bug prefix", () => {
  expect(addBugLabelName([], "bug: Slow checkout")).toEqual(["Slow checkout"]);
  expect(addBugLabelName(["Slow checkout"], "slow checkout")).toEqual(["Slow checkout"]);
});

test("filterBugLabelSuggestions searches visible names and slugs", () => {
  const labels = [
    { name: "Slow checkout", slug: "slow-checkout", display_name: "Slow checkout" },
    { name: "Transcript", slug: "transcript-loss", display_name: "Transcript" },
    { name: "Auth redirect", slug: "auth-redirect", display_name: "Auth redirect" },
  ];

  expect(filterBugLabelSuggestions(labels, "script").map((label) => label.name)).toEqual(["Transcript"]);
  expect(filterBugLabelSuggestions(labels, "slow-check").map((label) => label.name)).toEqual(["Slow checkout"]);
  expect(filterBugLabelSuggestions(labels, "bug: auth").map((label) => label.name)).toEqual(["Auth redirect"]);
});
