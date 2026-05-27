import assert from "node:assert/strict";
import { test } from "node:test";

import {
  linkWorkspacePathsInMarkdown,
  workspacePathFromHref,
} from "./workspaceLinks.ts";

test("links screenshot attachment workspace paths", () => {
  const markdown = [
    "look at this",
    "",
    "Attachments:",
    "- /workspace/screenshots/1.png",
  ].join("\n");

  assert.equal(
    linkWorkspacePathsInMarkdown(markdown),
    [
      "look at this",
      "",
      "Attachments:",
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

test("keeps line numbers inside workspace path links", () => {
  assert.equal(
    linkWorkspacePathsInMarkdown("Open /workspace/src/App.tsx:42."),
    "Open [/workspace/src/App.tsx:42](</workspace/src/App.tsx:42>).",
  );
});

test("parses workspace markdown hrefs with line numbers", () => {
  assert.deepEqual(
    workspacePathFromHref("/workspace/src/App.tsx:42"),
    { path: "src/App.tsx", line: 42 },
  );
  assert.deepEqual(
    workspacePathFromHref("workspace/src/App.tsx:42"),
    { path: "src/App.tsx", line: 42 },
  );
  assert.deepEqual(
    workspacePathFromHref("/workspace/src/App.tsx"),
    { path: "src/App.tsx", line: null },
  );
  assert.equal(workspacePathFromHref("https://example.test/workspace/src/App.tsx:42"), null);
});

test("does not treat arbitrary absolute paths as workspace hrefs", () => {
  assert.equal(workspacePathFromHref("/home/node/.codex/skills/test/SKILL.md"), null);
  assert.equal(workspacePathFromHref("/src/App.tsx"), null);
  assert.equal(workspacePathFromHref("file:///home/node/.codex/skills/test/SKILL.md"), null);
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
