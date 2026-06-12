import { test, expect } from "vitest";

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

  expect(linkWorkspacePathsInMarkdown(markdown)).toBe([
          "look at this",
          "",
          "Attachments:",
          "- [/workspace/screenshots/1.png](</workspace/screenshots/1.png>)",
        ].join("\n"));
});

test("keeps sentence punctuation outside workspace path links", () => {
  expect(linkWorkspacePathsInMarkdown("Open /workspace/screenshots/1.png.")).toBe("Open [/workspace/screenshots/1.png](</workspace/screenshots/1.png>).");
});

test("links bare urls in markdown prose", () => {
  expect(linkTextTargetsInMarkdown("Try https://google.com")).toBe("Try [https://google.com](<https://google.com>)");
});

test("keeps sentence punctuation outside url links", () => {
  expect(linkTextTargetsInMarkdown("Open https://example.test/docs?x=1.")).toBe("Open [https://example.test/docs?x=1](<https://example.test/docs?x=1>).");
});

test("links urls after opening punctuation but not existing markdown hrefs", () => {
  expect(linkTextTargetsInMarkdown("See (https://example.test/a).")).toBe("See ([https://example.test/a](<https://example.test/a>)).");
  expect(linkTextTargetsInMarkdown("[existing](https://example.test/a)")).toBe("[existing](https://example.test/a)");
});

test("keeps line numbers inside workspace path links", () => {
  expect(linkWorkspacePathsInMarkdown("Open /workspace/src/App.tsx:42.")).toBe("Open [/workspace/src/App.tsx:42](</workspace/src/App.tsx:42>).");
});

test("splits plain text urls and workspace paths without dropping newlines", () => {
  expect(splitLinksInText("a)\nOpen /workspace/src/App.tsx:42 and https://example.test.\nb)")).toEqual([
          { kind: "text", text: "a)\nOpen " },
          { kind: "workspace_path", text: "/workspace/src/App.tsx:42", href: "/workspace/src/App.tsx:42" },
          { kind: "text", text: " and " },
          { kind: "url", text: "https://example.test", href: "https://example.test" },
          { kind: "text", text: ".\nb)" },
        ]);
});

test("splits plain text workspace paths without dropping newlines", () => {
  expect(splitWorkspacePathsInText("a)\nOpen /workspace/src/App.tsx:42.\nb)")).toEqual([
          { kind: "text", text: "a)\nOpen " },
          { kind: "workspace_path", text: "/workspace/src/App.tsx:42", href: "/workspace/src/App.tsx:42" },
          { kind: "text", text: ".\nb)" },
        ]);
});

test("parses workspace markdown hrefs with line numbers", () => {
  expect(workspacePathFromHref("/workspace/src/App.tsx:42")).toEqual({ path: "src/App.tsx", line: 42 });
  expect(workspacePathFromHref("workspace/src/App.tsx:42")).toEqual({ path: "src/App.tsx", line: 42 });
  expect(workspacePathFromHref("/workspace/src/App.tsx")).toEqual({ path: "src/App.tsx", line: null });
  expect(workspacePathFromHref("https://example.test/workspace/src/App.tsx:42")).toBe(null);
});

test("parses same-origin absolute workspace hrefs from browser-normalized anchors", () => {
  const origin = "https://tank-operator-slot-3.tank.dev.romaine.life";

  expect(workspacePathFromHref(`${origin}/workspace/src/App.tsx:42`, origin)).toEqual({ path: "src/App.tsx", line: 42 });
  expect(workspacePathFromHref(`${origin}/workspace/screenshots/one%20two.png`, origin)).toEqual({ path: "screenshots/one two.png", line: null });
  expect(workspacePathFromHref(`${origin}/api/sessions/479`, origin)).toBe(null);
  expect(workspacePathFromHref("https://example.test/workspace/src/App.tsx:42", origin)).toBe(null);
});

test("does not treat arbitrary absolute paths as workspace hrefs", () => {
  expect(workspacePathFromHref("/home/node/.codex/skills/test/SKILL.md")).toBe(null);
  expect(workspacePathFromHref("/src/App.tsx")).toBe(null);
  expect(workspacePathFromHref("file:///home/node/.codex/skills/test/SKILL.md")).toBe(null);
});

test("does not rewrite inline or fenced code while preprocessing markdown", () => {
  const markdown = [
    "Use `/workspace/screenshots/1.png https://example.test` literally.",
    "",
    "```",
    "- /workspace/screenshots/2.png https://example.test",
    "```",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe(markdown);
});

test("does not rewrite existing markdown links", () => {
  const markdown = [
    "[existing](/workspace/screenshots/1.png)",
    "[url](https://example.test/workspace/screenshots/1.png)",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe(markdown);
});

test("rewrites agy workspace file markdown links before hardening", () => {
  const markdown = [
    "See [visual_verification_report.md](file:///workspace/chess-tactics/visual_verification_report.md).",
    "Open [app.js](<file:///workspace/chess-tactics/frontend/app.js:42> \"source\").",
    "Keep [host file](file:///home/node/secret.txt) blocked.",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe([
    "See [visual_verification_report.md](</workspace/chess-tactics/visual_verification_report.md>).",
    "Open [app.js](</workspace/chess-tactics/frontend/app.js:42> \"source\").",
    "Keep [host file](file:///home/node/secret.txt) blocked.",
  ].join("\n"));
});

test("does not rewrite agy file links inside inline or fenced code", () => {
  const markdown = [
    "Use `[app.js](file:///workspace/chess-tactics/frontend/app.js)` literally.",
    "",
    "```",
    "[style.css](file:///workspace/chess-tactics/frontend/style.css)",
    "```",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe(markdown);
});
