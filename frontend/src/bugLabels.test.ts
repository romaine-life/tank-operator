import assert from "node:assert/strict";
import test from "node:test";
import {
  addBugLabelName,
  filterBugLabelSuggestions,
  normalizeBugLabelDisplayName,
} from "./bugLabels";

test("normalizeBugLabelDisplayName removes the redundant bug prefix", () => {
  assert.equal(normalizeBugLabelDisplayName("  bug:   Slow checkout  "), "Slow checkout");
  assert.equal(normalizeBugLabelDisplayName("Transcript"), "Transcript");
});

test("addBugLabelName stores display names without the bug prefix", () => {
  assert.deepEqual(addBugLabelName([], "bug: Slow checkout"), ["Slow checkout"]);
  assert.deepEqual(addBugLabelName(["Slow checkout"], "slow checkout"), ["Slow checkout"]);
});

test("filterBugLabelSuggestions searches visible names and slugs", () => {
  const labels = [
    { name: "Slow checkout", slug: "slow-checkout", display_name: "Slow checkout" },
    { name: "Transcript", slug: "transcript-loss", display_name: "Transcript" },
    { name: "Auth redirect", slug: "auth-redirect", display_name: "Auth redirect" },
  ];

  assert.deepEqual(
    filterBugLabelSuggestions(labels, "script").map((label) => label.name),
    ["Transcript"],
  );
  assert.deepEqual(
    filterBugLabelSuggestions(labels, "slow-check").map((label) => label.name),
    ["Slow checkout"],
  );
  assert.deepEqual(
    filterBugLabelSuggestions(labels, "bug: auth").map((label) => label.name),
    ["Auth redirect"],
  );
});
