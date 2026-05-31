import assert from "node:assert/strict";
import { test } from "node:test";

import {
  sessionMatchesFilterFields,
  sessionRepoSlugs,
} from "./sessionRepos";

test("sessionRepoSlugs unions repos and discovered_repos", () => {
  assert.deepEqual(
    sessionRepoSlugs({
      repos: ["nelsong6/tank-operator"],
      discovered_repos: ["nelsong6/glimmung"],
    }),
    ["nelsong6/glimmung", "nelsong6/tank-operator"],
  );
});

test("sessionRepoSlugs dedupes case-insensitively, keeping first-seen casing", () => {
  // repos comes first, so its casing wins over the discovered duplicate.
  assert.deepEqual(
    sessionRepoSlugs({
      repos: ["NelsonG6/Tank-Operator"],
      discovered_repos: ["nelsong6/tank-operator", "nelsong6/auth"],
    }),
    ["nelsong6/auth", "NelsonG6/Tank-Operator"],
  );
});

test("sessionRepoSlugs sorts case-insensitively and trims/drops blanks", () => {
  assert.deepEqual(
    sessionRepoSlugs({
      repos: ["  owner/Zed  ", "", "owner/alpha"],
      discovered_repos: ["owner/Beta", "   "],
    }),
    ["owner/alpha", "owner/Beta", "owner/Zed"],
  );
});

test("sessionRepoSlugs tolerates missing/null fields (degraded snapshots)", () => {
  assert.deepEqual(sessionRepoSlugs({}), []);
  assert.deepEqual(
    sessionRepoSlugs({ repos: null, discovered_repos: undefined }),
    [],
  );
  assert.deepEqual(
    sessionRepoSlugs({ discovered_repos: ["owner/only-discovered"] }),
    ["owner/only-discovered"],
  );
});

test("sessionMatchesFilterFields: empty query matches everything", () => {
  assert.equal(
    sessionMatchesFilterFields(
      { slugs: [], name: "anything", id: "7", mode: "claude_gui" },
      "",
    ),
    true,
  );
});

test("sessionMatchesFilterFields matches on a repo slug substring", () => {
  const fields = {
    slugs: ["nelsong6/tank-operator"],
    name: "design work",
    id: "42",
    mode: "claude_gui",
  };
  // Query is pre-lowercased by the caller; matches owner or name fragments.
  assert.equal(sessionMatchesFilterFields(fields, "tank"), true);
  assert.equal(sessionMatchesFilterFields(fields, "nelsong6"), true);
  assert.equal(sessionMatchesFilterFields(fields, "glimmung"), false);
});

test("sessionMatchesFilterFields matches on name, id, and mode", () => {
  const fields = {
    slugs: [],
    name: "migration plan",
    id: "353",
    mode: "codex_gui",
  };
  assert.equal(sessionMatchesFilterFields(fields, "migration"), true);
  assert.equal(sessionMatchesFilterFields(fields, "353"), true);
  assert.equal(sessionMatchesFilterFields(fields, "codex"), true);
  assert.equal(sessionMatchesFilterFields(fields, "nope"), false);
});
