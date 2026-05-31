import assert from "node:assert/strict";
import test from "node:test";

import {
  MAX_REPOS_PER_SESSION,
  REPO_SLUG_PATTERN,
  REPO_SUPPORTED_MODES,
  RECENT_REPO_PREVIEW_LIMIT,
  addRepoSlug,
  isValidRepoSlug,
  modeSupportsRepos,
  normalizeRepoSlugs,
  recentRepoPreviewSlugs,
  recentRepoShortcutSlugs,
  removeRepoSlug,
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
  for (const mode of ["claude_gui", "codex_gui", "codex_app_server"]) {
    assert.ok(REPO_SUPPORTED_MODES.has(mode), `${mode} should support repos`);
  }
  for (const mode of [
    "claude_cli",
    "codex_cli",
    "config",
    "codex_config",
    "api_key",
    "hermes_gui",
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
