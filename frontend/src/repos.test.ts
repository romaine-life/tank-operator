import assert from "node:assert/strict";
import test from "node:test";

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
  assert.ok(REPO_SLUG_PATTERN.test("nelsong6/tank-operator"));
  assert.ok(REPO_SLUG_PATTERN.test("a/b"));
  assert.ok(REPO_SLUG_PATTERN.test("nelsong6/mcp.azure-personal"));
});

test("REPO_SLUG_PATTERN rejects shell/scheme/path injection", () => {
  assert.ok(!REPO_SLUG_PATTERN.test("https://github.com/nelsong6/tank-operator"));
  assert.ok(!REPO_SLUG_PATTERN.test("../etc/passwd"));
  assert.ok(!REPO_SLUG_PATTERN.test("nelsong6/tank-operator;rm -rf /"));
  assert.ok(!REPO_SLUG_PATTERN.test("nelsong6"));
  assert.ok(!REPO_SLUG_PATTERN.test("-org/repo"));
  assert.ok(!REPO_SLUG_PATTERN.test(""));
});

test("isValidRepoSlug trims whitespace before validating", () => {
  assert.ok(isValidRepoSlug("  nelsong6/tank-operator  "));
  assert.ok(!isValidRepoSlug("   "));
});

test("REPO_SUPPORTED_MODES matches the SDK-runner modes only", () => {
  for (const mode of [
    "claude_gui",
    "codex_gui",
    "codex_exec_gui",
    "codex_app_server",
  ]) {
    assert.ok(REPO_SUPPORTED_MODES.has(mode), `${mode} should support repos`);
  }
  for (const mode of [
    "claude_cli",
    "codex_cli",
    "config",
    "codex_config",
    "gemini_gui",
    "gemini_test",
    "gemini_config",
    "api_key",
  ]) {
    assert.ok(!REPO_SUPPORTED_MODES.has(mode), `${mode} should NOT support repos`);
  }
});

test("modeSupportsRepos round-trips the supported-modes set", () => {
  assert.equal(modeSupportsRepos("claude_gui"), true);
  assert.equal(modeSupportsRepos("claude_cli"), false);
});

test("MAX_REPOS_PER_SESSION matches backend cap", () => {
  // Mirrors maxReposPerSession in cmd/tank-operator/handlers_repos.go.
  // Update both sides together — this test exists to surface a
  // one-sided change.
  assert.equal(MAX_REPOS_PER_SESSION, 5);
});

test("normalizeRepoSlugs trims, dedupes, and caps staged repo defaults", () => {
  assert.deepEqual(
    normalizeRepoSlugs([
      "  nelsong6/tank-operator  ",
      "NelsonG6/Tank-Operator",
      "bad slug",
      "nelsong6/infra-bootstrap",
      "nelsong6/mcp-tank-operator",
      "openai/codex",
      "example/fifth",
      "example/sixth",
    ]),
    [
      "nelsong6/tank-operator",
      "nelsong6/infra-bootstrap",
      "nelsong6/mcp-tank-operator",
      "openai/codex",
      "example/fifth",
    ],
  );
});

test("RECENT_REPO_PREVIEW_LIMIT keeps the splash recent list short", () => {
  assert.equal(RECENT_REPO_PREVIEW_LIMIT, 4);
});

test("addRepoSlug rejects empty input", () => {
  const result = addRepoSlug([], "   ");
  assert.equal(result.ok, false);
});

test("addRepoSlug rejects malformed slugs", () => {
  const result = addRepoSlug([], "https://github.com/foo/bar");
  assert.equal(result.ok, false);
  assert.ok(result.ok === false && /doesn't look like/.test(result.error));
});

test("addRepoSlug rejects case-insensitive duplicates", () => {
  const result = addRepoSlug(["NelsonG6/Tank-Operator"], "nelsong6/tank-operator");
  assert.equal(result.ok, false);
  assert.ok(result.ok === false && /already added/.test(result.error));
});

test("addRepoSlug enforces the 5-repo cap", () => {
  const five = ["a/1", "b/2", "c/3", "d/4", "e/5"];
  const result = addRepoSlug(five, "f/6");
  assert.equal(result.ok, false);
  assert.ok(result.ok === false && /At most 5/.test(result.error));
});

test("addRepoSlug preserves insertion order on success", () => {
  let staged: string[] = [];
  for (const slug of ["a/1", "b/2", "c/3"]) {
    const result = addRepoSlug(staged, slug);
    assert.equal(result.ok, true);
    if (result.ok) staged = result.next;
  }
  assert.deepEqual(staged, ["a/1", "b/2", "c/3"]);
});

test("addRepoSlug trims before storing", () => {
  const result = addRepoSlug([], "  nelsong6/tank-operator  ");
  assert.equal(result.ok, true);
  if (result.ok) {
    assert.deepEqual(result.next, ["nelsong6/tank-operator"]);
  }
});

test("removeRepoSlug removes exact matches", () => {
  const next = removeRepoSlug(["a/1", "b/2", "c/3"], "b/2");
  assert.deepEqual(next, ["a/1", "c/3"]);
});

test("removeRepoSlug is case-sensitive (mirrors UI)", () => {
  // The UI's chips render the original-case slug, so the X button's
  // onRemove is keyed to that exact string. Case-insensitive removal
  // would be a bug — typing "Foo/Bar" then clicking X on "Foo/Bar"
  // should remove "Foo/Bar", not a hypothetical "foo/bar".
  const next = removeRepoSlug(["Foo/Bar"], "foo/bar");
  assert.deepEqual(next, ["Foo/Bar"]);
});

test("recentRepoPreviewSlugs caps and filters recent repos", () => {
  const recent = [
    "nelsong6/tank-operator",
    "  bad slug  ",
    "NelsonG6/Tank-Operator",
    "nelsong6/infra-bootstrap",
    "nelsong6/mcp-tank-operator",
    "openai/codex",
    "example/fifth",
  ];
  const selected = ["nelsong6/infra-bootstrap"];

  assert.deepEqual(recentRepoPreviewSlugs(recent, selected), [
    "nelsong6/tank-operator",
    "nelsong6/mcp-tank-operator",
    "openai/codex",
    "example/fifth",
  ]);
});

test("recentRepoPreviewSlugs hides suggestions once the session repo cap is reached", () => {
  const selected = ["a/1", "b/2", "c/3", "d/4", "e/5"];

  assert.deepEqual(recentRepoPreviewSlugs(["f/6"], selected), []);
});

test("recentRepoShortcutSlugs keeps stable numbered recent choices", () => {
  assert.deepEqual(
    recentRepoShortcutSlugs([
      "nelsong6/tank-operator",
      "  bad slug  ",
      "NelsonG6/Tank-Operator",
      "nelsong6/infra-bootstrap",
      "nelsong6/mcp-tank-operator",
      "openai/codex",
      "example/fifth",
    ]),
    [
      "nelsong6/tank-operator",
      "nelsong6/infra-bootstrap",
      "nelsong6/mcp-tank-operator",
      "openai/codex",
    ],
  );
});

test("pinnedRepoSlugs normalizes pins without the session repo cap", () => {
  assert.deepEqual(
    pinnedRepoSlugs([
      "nelsong6/tank-operator",
      "bad slug",
      "NelsonG6/Tank-Operator",
      "a/1",
      "b/2",
      "c/3",
      "d/4",
      "e/5",
    ]),
    ["nelsong6/tank-operator", "a/1", "b/2", "c/3", "d/4", "e/5"],
  );
});

test("pinnedRepoSlugs caps profile metadata", () => {
  const raw = Array.from({ length: MAX_PINNED_REPOS + 3 }, (_, i) => `owner/repo${i}`);
  const pinned = pinnedRepoSlugs(raw);
  assert.equal(pinned.length, MAX_PINNED_REPOS);
  assert.equal(pinned[0], "owner/repo0");
  assert.equal(pinned[MAX_PINNED_REPOS - 1], `owner/repo${MAX_PINNED_REPOS - 1}`);
});

test("repoShortcutSlugs orders pinned repos before recent repos", () => {
  assert.deepEqual(
    repoShortcutSlugs(
      ["nelsong6/glimmung", "NelsonG6/Tank-Operator"],
      ["nelsong6/tank-operator", "nelsong6/infra-bootstrap", "openai/codex"],
    ),
    [
      "nelsong6/glimmung",
      "NelsonG6/Tank-Operator",
      "nelsong6/infra-bootstrap",
      "openai/codex",
    ],
  );
});

test("pin helpers toggle case-insensitive pins", () => {
  const pinned = pinRepoSlug(["nelsong6/tank-operator"], "NelsonG6/Glimmung");
  assert.deepEqual(pinned, ["nelsong6/tank-operator", "NelsonG6/Glimmung"]);
  assert.equal(isRepoPinned(pinned, "nelsong6/glimmung"), true);
  assert.deepEqual(unpinRepoSlug(pinned, "NELSONG6/GLIMMUNG"), [
    "nelsong6/tank-operator",
  ]);
});

// reorderPinnedRepoSlugs is the pure core of the splash picker's drag-and-drop
// (and keyboard) pin reordering. The durable pinned_repos text[] order is the
// pin order, so these cases double as the SPA-side contract that the order a
// user drags into is exactly the order PUT to the server.
test("reorderPinnedRepoSlugs moves a pin downward to land after the target", () => {
  // Dragging the first pin onto the third lands it just after the third.
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3", "d/4"], "a/1", "c/3"),
    ["b/2", "c/3", "a/1", "d/4"],
  );
});

test("reorderPinnedRepoSlugs moves a pin upward to land before the target", () => {
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3", "d/4"], "d/4", "b/2"),
    ["a/1", "d/4", "b/2", "c/3"],
  );
});

test("reorderPinnedRepoSlugs reaches both ends via the drop target", () => {
  // Drop on the last item while dragging down -> moves to the very end.
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "a/1", "c/3"),
    ["b/2", "c/3", "a/1"],
  );
  // Drop on the first item while dragging up -> moves to the very front.
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "c/3", "a/1"),
    ["c/3", "a/1", "b/2"],
  );
});

test("reorderPinnedRepoSlugs supports adjacent (keyboard) single-step moves", () => {
  // ArrowDown on b/2: target is its next neighbour c/3.
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "b/2", "c/3"),
    ["a/1", "c/3", "b/2"],
  );
  // ArrowUp on b/2: target is its previous neighbour a/1.
  assert.deepEqual(
    reorderPinnedRepoSlugs(["a/1", "b/2", "c/3"], "b/2", "a/1"),
    ["b/2", "a/1", "c/3"],
  );
});

test("reorderPinnedRepoSlugs matches slugs case-insensitively", () => {
  assert.deepEqual(
    reorderPinnedRepoSlugs(
      ["NelsonG6/Tank-Operator", "nelsong6/glimmung", "openai/codex"],
      "OPENAI/CODEX",
      "nelsong6/tank-operator",
    ),
    ["openai/codex", "NelsonG6/Tank-Operator", "nelsong6/glimmung"],
  );
});

test("reorderPinnedRepoSlugs is a normalized no-op for same/unknown/empty slugs", () => {
  const list = ["a/1", "b/2", "c/3"];
  assert.deepEqual(reorderPinnedRepoSlugs(list, "a/1", "a/1"), list);
  assert.deepEqual(reorderPinnedRepoSlugs(list, "a/1", "z/9"), list);
  assert.deepEqual(reorderPinnedRepoSlugs(list, "z/9", "a/1"), list);
  assert.deepEqual(reorderPinnedRepoSlugs(list, "", "a/1"), list);
  assert.deepEqual(reorderPinnedRepoSlugs(list, "a/1", ""), list);
});

test("reorderPinnedRepoSlugs normalizes the list (dedup, drop invalid, cap)", () => {
  // A malformed entry and a case-dup are stripped before the move, so the
  // output always satisfies the same contract as pinnedRepoSlugs.
  assert.deepEqual(
    reorderPinnedRepoSlugs(
      ["a/1", "bad slug", "A/1", "b/2", "c/3"],
      "c/3",
      "a/1",
    ),
    ["c/3", "a/1", "b/2"],
  );
});
