#!/usr/bin/env node
// Migration guard: the separate "PR lane" mechanism is retired in favor of the
// unified break-glass branch-lane grant (docs/branch-lane-grants.md). Before the
// unification a restricted session had TWO parallel governed-write mechanisms:
// break-glass git (request_git_break_glass → push) and PR lanes
// (request_pr_lane → create_pr_lane → branch + draft PR). They shared a scope
// model but were disjoint event families, tools, routes, and handlers, and a
// branch-scoped break-glass grant could push a branch but not open its PR — the
// PR half lived only in the other mechanism. The unification makes ONE grant
// cover push + PR-open/own for the scoped branches, so the entire pr_lane
// surface (tools, handlers, routes, the github.pr_lane.* event family, the
// proxy tool wrappers) is deleted, not wrapped.
//
// Per docs/migration-policy.md the retired path must not return. This guard
// fails if any of the retired pr_lane symbols reappear in LIVE source.
//
// NOTE: this guard WILL fail until the backend/proxy/frontend retirement of the
// pr_lane surface lands (that change ships separately). That red state is the
// intended pre-cutover signal — the guard is authored ahead of the deletion so
// the deletion PR has an enforcement target the moment it cuts the symbols, and
// CI cannot go green until every live pr_lane path is gone.
//
// SCOPE EXCLUSIONS:
//   - docs/** — the migration is explained in prose (branch-lane-grants.md and
//     the feature ledgers) by naming the retired symbols. Documentation of the
//     deletion is not a resurrection.
//   - "retired path stays out" guard tests — negative-assertion tests that
//     assert a retired symbol is ABSENT (e.g. frontend/src/migrationPolicy.test.ts)
//     legitimately name the symbol in an expectation; excluding them keeps this
//     guard from firing on its sibling guards. (The CURRENT pr_lane *behavior*
//     tests that assert the old path WORKS are NOT excluded — they are part of
//     what the cutover deletes, and this guard must flag them so they cannot
//     silently persist.)
//   - this guard file and the main guard that imports it.
//
// Run directly as a CLI, or import `collectRemovedPRLaneFailures()` — it is
// merged into the broader scripts/check-removed-chat-runtime.mjs guard so it
// rides that guard's CI wiring (backend-go/**, claude-container/**, frontend/**,
// docs/**, k8s/**, scripts/**, CLAUDE.md, README.md) without a separate workflow
// step (the governed session push cannot edit workflow files).

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const SCAN_ROOTS = [
  "backend-go",
  "claude-container",
  "codex-runner",
  "claude-runner",
  "frontend",
  "infra",
  "k8s",
  "runner-shared",
  "schemas",
  "scripts",
  "CLAUDE.md",
  "README.md",
];

const ignoredDirs = new Set([
  ".git",
  ".terraform",
  "__pycache__",
  ".venv",
  "venv",
  "node_modules",
  "dist",
  "build",
  "coverage",
  "target",
]);

const ignoredFiles = new Set([
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "go.sum",
]);

// "retired path stays out" guards + this guard's own files. These legitimately
// name the retired symbols (as ABSENCE assertions or as the deletion target
// list) and must not trip the scan. docs/** is excluded wholesale by SCAN_ROOTS
// not including it.
const ignoredRelativePaths = new Set([
  "scripts/check-removed-pr-lane.mjs",
  "scripts/check-removed-chat-runtime.mjs",
  // migrationPolicy.test.ts asserts the retired above-composer PR-lane approval
  // popup is ABSENT while the menu-integrated approval still exists. It names
  // the retired symbols in negative expectations — a retired-path-stays-out
  // guard, not a resurrection.
  "frontend/src/migrationPolicy.test.ts",
  // Negative reintroduction guards: these tests assert the retired pr_lane
  // surface STAYS gone (the control-action write path rejects every
  // github.pr_lane.* action; the UI label map drops them). They name the
  // retired symbols only inside ABSENCE/rejection assertions — exactly the
  // "retired path stays out" tests docs/migration-policy.md requires.
  "backend-go/cmd/tank-operator/control_actions_test.go",
  "frontend/src/controlActions.test.ts",
]);

const blocked = [
  { name: "retired request_pr_lane tool", pattern: /\brequest_pr_lane\b/ },
  { name: "retired create_pr_lane tool", pattern: /\bcreate_pr_lane\b/ },
  { name: "retired github.pr_lane.* event family", pattern: /\bgithub\.pr_lane\./ },
  { name: "retired _TANK_PR_LANE_TOOL marker", pattern: /\b_TANK_PR_LANE_TOOL\b/ },
  { name: "retired _TANK_CREATE_PR_LANE_TOOL marker", pattern: /\b_TANK_CREATE_PR_LANE_TOOL\b/ },
  { name: "retired PR-lane handler", pattern: /handle.*PRLane/ },
];

async function* walk(rel) {
  const abs = path.join(repoRoot, rel);
  let stat;
  try {
    stat = await fs.stat(abs);
  } catch {
    return;
  }
  if (stat.isFile()) {
    yield rel;
    return;
  }
  for (const entry of await fs.readdir(abs, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (ignoredDirs.has(entry.name)) continue;
      yield* walk(path.join(rel, entry.name));
      continue;
    }
    if (!entry.isFile()) continue;
    if (ignoredFiles.has(entry.name)) continue;
    yield path.join(rel, entry.name);
  }
}

export async function collectRemovedPRLaneFailures() {
  const failures = [];
  for (const root of SCAN_ROOTS) {
    for await (const rel of walk(root)) {
      const relativePath = rel.split(path.sep).join("/");
      if (ignoredRelativePaths.has(relativePath)) continue;
      const bytes = await fs.readFile(path.join(repoRoot, rel));
      if (bytes.includes(0)) continue;
      const text = bytes.toString("utf8");
      for (const rule of blocked) {
        const match = rule.pattern.exec(text);
        if (!match) continue;
        const { line, column } = lineAndColumn(text, match.index);
        failures.push(
          `${relativePath}:${line}:${column} ${rule.name}: ${JSON.stringify(match[0])}`,
        );
      }
    }
  }
  return failures;
}

export const REMOVED_PR_LANE_FAILURE_HINT =
  "The separate PR-lane mechanism is retired by the unified break-glass " +
  "branch-lane grant (docs/branch-lane-grants.md): one request_git_break_glass " +
  "grant covers push + PR open/own for the scoped branches. The retired " +
  "request_pr_lane / create_pr_lane tools, github.pr_lane.* events, " +
  "_TANK_PR_LANE_TOOL / _TANK_CREATE_PR_LANE_TOOL markers, and handle*PRLane " +
  "handlers must not return; see docs/migration-policy.md.";

function lineAndColumn(text, index) {
  const before = text.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return {
    line: lines.length,
    column: lines[lines.length - 1].length + 1,
  };
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;

if (isMain) {
  const failures = await collectRemovedPRLaneFailures();
  if (failures.length > 0) {
    console.error(
      "Retired PR-lane surface reappears in live code. " +
        REMOVED_PR_LANE_FAILURE_HINT +
        "\n" +
        failures.map((f) => `  - ${f}`).join("\n"),
    );
    process.exit(1);
  }
  console.log("No retired PR-lane surfaces found.");
}
