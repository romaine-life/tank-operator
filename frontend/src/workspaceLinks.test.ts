import assert from "node:assert/strict";
import { test } from "node:test";

import { linkWorkspacePathsInMarkdown } from "./workspaceLinks.ts";

test("links screenshot attachment workspace paths", () => {
  const markdown = [
    "look at this",
    "",
    "Attachments (use the Read tool to load):",
    "- /workspace/screenshots/1.png",
  ].join("\n");

  assert.equal(
    linkWorkspacePathsInMarkdown(markdown),
    [
      "look at this",
      "",
      "Attachments (use the Read tool to load):",
      "- [/workspace/screenshots/1.png](</workspace/screenshots/1.png>)",
    ].join("\n"),
  );
});

test("keeps sentence punctuation outside workspace path links", () => {
  assert.equal(
    linkWorkspacePathsInMarkdown("Open /workspace/screenshots/1.png."),
    "Open [/workspace/screenshots/1.png](</workspace/screenshots/1.png>).",
  );
});

test("does not rewrite inline or fenced code paths", () => {
  const markdown = [
    "Use `/workspace/screenshots/1.png` literally.",
    "",
    "```",
    "- /workspace/screenshots/2.png",
    "```",
  ].join("\n");

  assert.equal(linkWorkspacePathsInMarkdown(markdown), markdown);
});

test("does not rewrite existing links or urls containing workspace paths", () => {
  const markdown = [
    "[existing](/workspace/screenshots/1.png)",
    "https://example.test/workspace/screenshots/1.png",
  ].join("\n");

  assert.equal(linkWorkspacePathsInMarkdown(markdown), markdown);
});
