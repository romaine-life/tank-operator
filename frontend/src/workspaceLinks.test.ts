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

test("parses workspace markdown hrefs into absolute paths with line numbers", () => {
  expect(workspacePathFromHref("/workspace/src/App.tsx:42")).toEqual({ path: "/workspace/src/App.tsx", line: 42 });
  expect(workspacePathFromHref("workspace/src/App.tsx:42")).toEqual({ path: "/workspace/src/App.tsx", line: 42 });
  expect(workspacePathFromHref("/workspace/src/App.tsx")).toEqual({ path: "/workspace/src/App.tsx", line: null });
  expect(workspacePathFromHref("https://example.test/workspace/src/App.tsx:42")).toBe(null);
});

test("parses same-origin absolute workspace hrefs from browser-normalized anchors", () => {
  const origin = "https://tank-operator-slot-3.tank.dev.romaine.life";

  expect(workspacePathFromHref(`${origin}/workspace/src/App.tsx:42`, origin)).toEqual({ path: "/workspace/src/App.tsx", line: 42 });
  expect(workspacePathFromHref(`${origin}/workspace/screenshots/one%20two.png`, origin)).toEqual({ path: "/workspace/screenshots/one two.png", line: null });
  expect(workspacePathFromHref(`${origin}/api/sessions/479`, origin)).toBe(null);
  expect(workspacePathFromHref("https://example.test/workspace/src/App.tsx:42", origin)).toBe(null);
});

test("linkifies the browsable roots and ~, but not arbitrary paths or secrets", () => {
  // Home, tooling, and tmp are browsable roots now → linkable (absolute path).
  expect(workspacePathFromHref("/home/node/.claude/plan.md")).toEqual({ path: "/home/node/.claude/plan.md", line: null });
  expect(workspacePathFromHref("~/.claude/plan.md")).toEqual({ path: "/home/node/.claude/plan.md", line: null });
  expect(workspacePathFromHref("/opt/tank/session-config/skills__x.md")).toEqual({ path: "/opt/tank/session-config/skills__x.md", line: null });
  expect(workspacePathFromHref("file:///home/node/.codex/skills/test/SKILL.md")).toEqual({ path: "/home/node/.codex/skills/test/SKILL.md", line: null });
  // Outside every readable root → plain text.
  expect(workspacePathFromHref("/src/App.tsx")).toBe(null);
  expect(workspacePathFromHref("/etc/passwd")).toBe(null);
  // Secret deny-prefixes are never linkified.
  expect(workspacePathFromHref("/var/run/secrets/auth.romaine.life/token")).toBe(null);
  expect(workspacePathFromHref("/proc/1/environ")).toBe(null);
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

test("rewrites browsable-root file markdown links, leaves secrets blocked", () => {
  const markdown = [
    "See [visual_verification_report.md](file:///workspace/chess-tactics/visual_verification_report.md).",
    "Open [app.js](<file:///workspace/chess-tactics/frontend/app.js:42> \"source\").",
    "Open [the plan](file:///home/node/.claude/plan.md) too.",
    "Keep [token](file:///var/run/secrets/auth.romaine.life/token) blocked.",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe([
    "See [visual_verification_report.md](</workspace/chess-tactics/visual_verification_report.md>).",
    "Open [app.js](</workspace/chess-tactics/frontend/app.js:42> \"source\").",
    "Open [the plan](</home/node/.claude/plan.md>) too.",
    "Keep [token](file:///var/run/secrets/auth.romaine.life/token) blocked.",
  ].join("\n"));
});

test("does not rewrite file links inside inline or fenced code", () => {
  const markdown = [
    "Use `[app.js](file:///workspace/chess-tactics/frontend/app.js)` literally.",
    "",
    "```",
    "[style.css](file:///workspace/chess-tactics/frontend/style.css)",
    "```",
  ].join("\n");

  expect(linkTextTargetsInMarkdown(markdown)).toBe(markdown);
});
