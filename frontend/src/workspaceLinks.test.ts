import assert from "node:assert/strict";
import { test } from "node:test";

import {
  linkTextTargetsInMarkdown,
  linkWorkspacePathsInMarkdown,
  splitLinksInText,
  splitWorkspacePathsInText,
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

test("links bare urls in markdown prose", () => {
  assert.equal(
    linkTextTargetsInMarkdown("Try https://google.com"),
    "Try [https://google.com](<https://google.com>)",
  );
});

test("keeps sentence punctuation outside url links", () => {
  assert.equal(
    linkTextTargetsInMarkdown("Open https://example.test/docs?x=1."),
    "Open [https://example.test/docs?x=1](<https://example.test/docs?x=1>).",
  );
});

test("links urls after opening punctuation but not existing markdown hrefs", () => {
  assert.equal(
    linkTextTargetsInMarkdown("See (https://example.test/a)."),
    "See ([https://example.test/a](<https://example.test/a>)).",
  );
  assert.equal(
    linkTextTargetsInMarkdown("[existing](https://example.test/a)"),
    "[existing](https://example.test/a)",
  );
});

test("keeps line numbers inside workspace path links", () => {
  assert.equal(
    linkWorkspacePathsInMarkdown("Open /workspace/src/App.tsx:42."),
    "Open [/workspace/src/App.tsx:42](</workspace/src/App.tsx:42>).",
  );
});

test("splits plain text urls and workspace paths without dropping newlines", () => {
  assert.deepEqual(
    splitLinksInText("a)\nOpen /workspace/src/App.tsx:42 and https://example.test.\nb)"),
    [
      { kind: "text", text: "a)\nOpen " },
      { kind: "workspace_path", text: "/workspace/src/App.tsx:42", href: "/workspace/src/App.tsx:42" },
      { kind: "text", text: " and " },
      { kind: "url", text: "https://example.test", href: "https://example.test" },
      { kind: "text", text: ".\nb)" },
    ],
  );
});

test("splits plain text workspace paths without dropping newlines", () => {
  assert.deepEqual(
    splitWorkspacePathsInText("a)\nOpen /workspace/src/App.tsx:42.\nb)"),
    [
      { kind: "text", text: "a)\nOpen " },
      { kind: "workspace_path", text: "/workspace/src/App.tsx:42", href: "/workspace/src/App.tsx:42" },
      { kind: "text", text: ".\nb)" },
    ],
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

test("parses same-origin absolute workspace hrefs from browser-normalized anchors", () => {
  const origin = "https://tank-operator-slot-3.tank.dev.romaine.life";

  assert.deepEqual(
    workspacePathFromHref(`${origin}/workspace/src/App.tsx:42`, origin),
    { path: "src/App.tsx", line: 42 },
  );
  assert.deepEqual(
    workspacePathFromHref(`${origin}/workspace/screenshots/one%20two.png`, origin),
    { path: "screenshots/one two.png", line: null },
  );
  assert.equal(workspacePathFromHref(`${origin}/api/sessions/479`, origin), null);
  assert.equal(
    workspacePathFromHref("https://example.test/workspace/src/App.tsx:42", origin),
    null,
  );
});

test("does not treat arbitrary absolute paths as workspace hrefs", () => {
  assert.equal(workspacePathFromHref("/home/node/.codex/skills/test/SKILL.md"), null);
  assert.equal(workspacePathFromHref("/src/App.tsx"), null);
  assert.equal(workspacePathFromHref("file:///home/node/.codex/skills/test/SKILL.md"), null);
});

test("does not rewrite inline or fenced code while preprocessing markdown", () => {
  const markdown = [
    "Use `/workspace/screenshots/1.png https://example.test` literally.",
    "",
    "```",
    "- /workspace/screenshots/2.png https://example.test",
    "```",
  ].join("\n");

  assert.equal(linkTextTargetsInMarkdown(markdown), markdown);
});

test("does not rewrite existing markdown links", () => {
  const markdown = [
    "[existing](/workspace/screenshots/1.png)",
    "[url](https://example.test/workspace/screenshots/1.png)",
  ].join("\n");

  assert.equal(linkTextTargetsInMarkdown(markdown), markdown);
});
