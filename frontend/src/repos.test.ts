import { test, expect } from "vitest";

import {
  MAX_PINNED_REPOS,
  MAX_REPOS_PER_SESSION,
  REPO_SLUG_PATTERN,
  REPO_SUPPORTED_MODES,
  RECENT_REPO_PREVIEW_LIMIT,
  addRepoSlug,
  isRepoPinned,
  isValidRepoSlug,
  modeSupportsRepos,
  normalizeRepoSlugs,
  pinRepoSlug,
  pinnedRepoSlugs,
  recentRepoPreviewSlugs,
  recentRepoShortcutSlugs,
  reorderPinnedRepoSlugs,
  repoShortcutSlugs,
  removeRepoSlug,
  unpinRepoSlug,
} from "./repos";

// These tests pin the SPA half of the repo-selection contract. The
// backend half lives in cmd/tank-operator/handlers_repos_test.go;
// the two suites assert the same regex shape, the same mode set,
// and the same 5-repo cap. A change to one side without the other
// is the regression these tests are designed to surface.

test("REPO_SLUG_PATTERN accepts canonical owner/name slugs", () => {
  expect(REPO_SLUG_PATTERN.test("romaine-life/tank-operator")).toBeTruthy();
  expect(REPO_SLUG_PATTERN.test("a/b")).toBeTruthy();
  expect(REPO_SLUG_PATTERN.test("romaine-life/mcp.azure-personal")).toBeTruthy();
});

test("REPO_SLUG_PATTERN rejects shell/scheme/path injection", () => {
  expect(!REPO_SLUG_PATTERN.test("https://github.com/romaine-life/tank-operator")).toBeTruthy();
  expect(!REPO_SLUG_PATTERN.test("../etc/passwd")).toBeTruthy();
  expect(!REPO_SLUG_PATTERN.test("romaine-life/tank-operator;rm -rf /")).toBeTruthy();
  expect(!REPO_SLUG_PATTERN.test("romaine-life")).toBeTruthy();
  expect(!REPO_SLUG_PATTERN.test("-org/repo")).toBeTruthy();
  expect(!REPO_SLUG_PATTERN.test("")).toBeTruthy();
});

test("isValidRepoSlug trims whitespace before validating", () => {
  expect(isValidRepoSlug("  romaine-life/tank-operator  ")).toBeTruthy();
  expect(!isValidRepoSlug("   ")).toBeTruthy();
});

test("REPO_SUPPORTED_MODES matches the SDK-runner modes only", () => {
  for (const mode of [
    "claude_gui",
    "codex_gui",
    "codex_exec_gui",
    "codex_app_server",
    "antigravity_gui",
  ]) {
    expect(REPO_SUPPORTED_MODES.has(mode), `${mode} should support repos`).toBeTruthy();
  }
  for (const mode of [
    "claude_cli",
    "codex_cli",
    "config",
    "codex_config",
    "antigravity_config",
    "gemini_gui",
    "gemini_test",
    "gemini_config",
    "api_key",
  ]) {
    expect(!REPO_SUPPORTED_MODES.has(mode), `${mode} should NOT support repos`).toBeTruthy();
  }
});

test("modeSupportsRepos round-trips the supported-modes set", () => {
  expect(modeSupportsRepos("claude_gui")).toBe(true);
  expect(modeSupportsRepos("claude_cli")).toBe(false);
});

test("MAX_REPOS_PER_SESSION matches backend cap", () => {
  // Mirrors maxReposPerSession in cmd/tank-operator/handlers_repos.go.
  // Update both sides together — this test exists to surface a
  // one-sided change.
  expect(MAX_REPOS_PER_SESSION).toBe(5);
});

test("normalizeRepoSlugs trims, dedupes, and caps staged repo defaults", () => {
  expect(normalizeRepoSlugs([
          "  romaine-life/tank-operator  ",
          "Romaine-Life/Tank-Operator",
          "bad slug",
          "romaine-life/infra-bootstrap",
          "romaine-life/mcp-tank-operator",
          "openai/codex",
          "example/fifth",
          "example/sixth",
        ])).toEqual([
          "romaine-life/tank-operator",
          "romaine-life/infra-bootstrap",
          "romaine-life/mcp-tank-operator",
          "openai/codex",
          "example/fifth",
        ]);
});

test("RECENT_REPO_PREVIEW_LIMIT keeps the splash recent list short", () => {
  expect(RECENT_REPO_PREVIEW_LIMIT).toBe(4);
});

test("addRepoSlug rejects empty input", () => {
  const result = addRepoSlug([], "   ");
  expect(result.ok).toBe(false);
});

test("addRepoSlug rejects malformed slugs", () => {
  const result = addRepoSlug([], "https://github.com/foo/bar");
  expect(result.ok).toBe(false);
  expect(result.ok === false && /doesn't look like/.test(result.error)).toBeTruthy();
});

test("addRepoSlug rejects case-insensitive duplicates", () => {
  const result = addRepoSlug(["Romaine-Life/Tank-Operator"], "romaine-life/tank-operator");
  expect(result.ok).toBe(false);
  expect(result.ok === false && /already added/.test(result.error)).toBeTruthy();
});

test("addRepoSlug enforces the 5-repo cap", () => {
  const five = ["a/1", "b/2", "c/3", "d/4", "e/5"];
  const result = addRepoSlug(five, "f/6");
  expect(result.ok).toBe(false);
  expect(result.ok === false && /At most 5/.test(result.error)).toBeTruthy();
});

test("addRepoSlug preserves insertion order on success", () => {
  let staged: string[] = [];
  for (const slug of ["a/1", "b/2", "c/3"]) {
    const result = addRepoSlug(staged, slug);
    expect(result.ok).toBe(true);
    if (result.ok) staged = result.next;
  }
  expect(staged).toEqual(["a/1", "b/2", "c/3"]);
});

test("addRepoSlug trims before storing", () => {
  const result = addRepoSlug([], "  romaine-life/tank-operator  ");
  expect(result.ok).toBe(true);
  if (result.ok) {
    expect(result.next).toEqual(["romaine-life/tank-operator"]);
  }
});

test("removeRepoSlug removes exact matches", () => {
  const next = removeRepoSlug(["a/1", "b/2", "c/3"], "b/2");
  expect(next).toEqual(["a/1", "c/3"]);
});

test("removeRepoSlug is case-sensitive (mirrors UI)", () => {
  // The UI's chips render the original-case slug, so the X button's
  // onRemove is keyed to that exact string. Case-insensitive removal
  // would be a bug — typing "Foo/Bar" then clicking X on "Foo/Bar"
  // should remove "Foo/Bar", not a hypothetical "foo/bar".
  const next = removeRepoSlug(["Foo/Bar"], "foo/bar");
  expect(next).toEqual(["Foo/Bar"]);
});

test("recentRepoPreviewSlugs caps and filters recent repos", () => {
  const recent = [
    "romaine-life/tank-operator",
    "  bad slug  ",
    "Romaine-Life/Tank-Operator",
    "romaine-life/infra-bootstrap",
    "romaine-life/mcp-tank-operator",
    "openai/codex",
    "example/fifth",
  ];
  const selected = ["romaine-life/infra-bootstrap"];

  expect(recentRepoPreviewSlugs(recent, selected)).toEqual([
        "romaine-life/tank-operator",
        "romaine-life/mcp-tank-operator",
        "openai/codex",
        "example/fifth",
      ]);
});

test("recentRepoPreviewSlugs hides suggestions once the session repo cap is reached", () => {
  const selected = ["a/1", "b/2", "c/3", "d/4", "e/5"];

  expect(recentRepoPreviewSlugs(["f/6"], selected)).toEqual([]);
});

test("recentRepoShortcutSlugs keeps stable numbered recent choices", () => {
  expect(recentRepoShortcutSlugs([
          "romaine-life/tank-operator",
          "  bad slug  ",
          "Romaine-Life/Tank-Operator",
          "romaine-life/infra-bootstrap",
          "romaine-life/mcp-tank-operator",
          "openai/codex",
          "example/fifth",
        ])).toEqual([
          "romaine-life/tank-operator",
          "romaine-life/infra-bootstrap",
          "romaine-life/mcp-tank-operator",
          "openai/codex",
        ]);
});

test("pinnedRepoSlugs normalizes pins without the session repo cap", () => {
  expect(pinnedRepoSlugs([
          "romaine-life/tank-operator",
          "bad slug",
          "Romaine-Life/Tank-Operator",
          "a/1",
          "b/2",
          "c/3",
          "d/4",
          "e/5",
        ])).toEqual(["romaine-life/tank-operator", "a/1", "b/2", "c/3", "d/4", "e/5"]);
});

test("pinnedRepoSlugs caps profile metadata", () => {
  const raw = Array.from({ length: MAX_PINNED_REPOS + 3 }, (_, i) => `owner/repo${i}`);
  const pinned = pinnedRepoSlugs(raw);
  expect(pinned.length).toBe(MAX_PINNED_REPOS);
  expect(pinned[0]).toBe("owner/repo0");
  expect(pinned[MAX_PINNED_REPOS - 1]).toBe(`owner/repo${MAX_PINNED_REPOS - 1}`);
});

test("repoShortcutSlugs orders pinned repos before recent repos", () => {
  expect(repoShortcutSlugs(
          ["romaine-life/glimmung", "Romaine-Life/Tank-Operator"],
          ["romaine-life/tank-operator", "romaine-life/infra-bootstrap", "openai/codex"],
        )).toEqual([
          "romaine-life/glimmung",
          "Romaine-Life/Tank-Operator",
          "romaine-life/infra-bootstrap",
          "openai/codex",
        ]);
});

test("pin helpers toggle case-insensitive pins", () => {
  const pinned = pinRepoSlug(["romaine-life/tank-operator"], "Romaine-Life/Glimmung");
  expect(pinned).toEqual(["romaine-life/tank-operator", "Romaine-Life/Glimmung"]);
  expect(isRepoPinned(pinned, "romaine-life/glimmung")).toBe(true);
  expect(unpinRepoSlug(pinned, "ROMAINE-LIFE/GLIMMUNG")).toEqual([
        "romaine-life/tank-operator",
      ]);
});

// reorderPinnedRepoSlugs is the pure core of the splash picker's drag-and-drop
// (and keyboard) pin reordering. The durable pinned_repos text[] order is the
// pin order, so these cases double as the SPA-side contract that the order a
// user drags into is exactly the order PUT to the server.
test("reorderPinnedRepoSlugs moves a pin downward to land after the target", () => {
  // Dragging the first pin onto the third lands it just after the third.
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3", "d/4"], "a/1", "c/3")).toEqual(["b/2", "c/3", "a/1", "d/4"]);
});

test("reorderPinnedRepoSlugs moves a pin upward to land before the target", () => {
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3", "d/4"], "d/4", "b/2")).toEqual(["a/1", "d/4", "b/2", "c/3"]);
});

test("reorderPinnedRepoSlugs reaches both ends via the drop target", () => {
  // Drop on the last item while dragging down -> moves to the very end.
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "a/1", "c/3")).toEqual(["b/2", "c/3", "a/1"]);
  // Drop on the first item while dragging up -> moves to the very front.
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "c/3", "a/1")).toEqual(["c/3", "a/1", "b/2"]);
});

test("reorderPinnedRepoSlugs supports adjacent (keyboard) single-step moves", () => {
  // ArrowDown on b/2: target is its next neighbour c/3.
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "b/2", "c/3")).toEqual(["a/1", "c/3", "b/2"]);
  // ArrowUp on b/2: target is its previous neighbour a/1.
  expect(reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "b/2", "a/1")).toEqual(["b/2", "a/1", "c/3"]);
});

test("reorderPinnedRepoSlugs matches slugs case-insensitively", () => {
  expect(reorderPinnedRepoSlugs(
          ["Romaine-Life/Tank-Operator", "romaine-life/glimmung", "openai/codex"],
          "OPENAI/CODEX",
          "romaine-life/tank-operator",
        )).toEqual(["openai/codex", "Romaine-Life/Tank-Operator", "romaine-life/glimmung"]);
});

test("reorderPinnedRepoSlugs is a normalized no-op for same/unknown/empty slugs", () => {
  const list = ["a/1", "b/2", "c/3"];
  expect(reorderPinnedRepoSlugs(list, "a/1", "a/1")).toEqual(list);
  expect(reorderPinnedRepoSlugs(list, "a/1", "z/9")).toEqual(list);
  expect(reorderPinnedRepoSlugs(list, "z/9", "a/1")).toEqual(list);
  expect(reorderPinnedRepoSlugs(list, "", "a/1")).toEqual(list);
  expect(reorderPinnedRepoSlugs(list, "a/1", "")).toEqual(list);
});

test("reorderPinnedRepoSlugs normalizes the list (dedup, drop invalid, cap)", () => {
  // A malformed entry and a case-dup are stripped before the move, so the
  // output always satisfies the same contract as pinnedRepoSlugs.
  expect(reorderPinnedRepoSlugs(
          ["a/1", "bad slug", "A/1", "b/2", "c/3"],
          "c/3",
          "a/1",
        )).toEqual(["c/3", "a/1", "b/2"]);
});
