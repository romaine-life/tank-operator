#!/usr/bin/env node

// Completion manifest for the context-window-table removal migration.
//
// This script is the spec for the migration. "Done" = exit 0. Same workflow
// and shape as scripts/check-stop-request-migration.mjs: it lands on the
// branch as a committed manifest so the contract is auditable independently
// of whatever an agent later writes, and after merge it stays in scripts/ as
// a regression guard, exactly like scripts/check-removed-chat-runtime.mjs.
//
// THE CONTRACT:
//
//   The composer's context indicator is a `used/window` fraction whose
//   denominator is the provider-observed context window persisted on the
//   session row (`runtime_context_window_tokens`, reported by the runners
//   through PUT /api/internal/sessions/{id}/runtime-config — codex
//   app-server token usage; Claude Agent SDK modelUsage.contextWindow). There
//   is NO frontend model-window table and NO percent ring. The frontend
//   `CONTEXT_WINDOW_BY_MODEL` table and its `getContextWindow` lookup are
//   deleted; nothing under frontend/src may reintroduce them.
//
// NOTE: the frontend deletion happens in parallel with this manifest, so
// running the guard before that deletion lands legitimately FAILs on the
// forbidden-pattern checks while the table still exists. The script is the
// target state; a red row means the migration is incomplete, not that the
// script is wrong.
//
// Skip the slow exec gates during structural iteration with:
//   SKIP_EXEC=1 node scripts/check-context-window-table-migration.mjs

import { spawnSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const skipExec = process.env.SKIP_EXEC === "1";

// Files exempt from the tree-absent walk, mirroring the
// `ignoredRelativePaths` idiom in scripts/check-removed-chat-runtime.mjs:
// a migration test that names the retired symbols in `doesNotMatch` /
// negative assertions is the cutover's own proof that the surface is gone,
// not live code reintroducing it. The guard blocks reintroduction in
// implementation files; it must not fire on the assertion fixtures that
// pin the deletion.
const ignoredRelativePaths = new Set([
  // This manifest names CONTEXT_WINDOW_BY_MODEL / getContextWindow as the
  // forbidden patterns; exclude it so the guard doesn't match its own rules.
  "scripts/check-context-window-table-migration.mjs",
  // The frontend cutover's migration test asserts, via assert.doesNotMatch,
  // that the model-window table + lookup are absent from the app source.
  // The retired identifiers appear only inside those negative assertions.
  "frontend/src/turnCostEstimateUi.test.ts",
]);

// Directories the tree-absent walk skips, mirroring
// scripts/check-removed-chat-runtime.mjs.
const ignoredDirs = new Set([
  ".claude",
  ".git",
  ".terraform",
  ".vite",
  ".next",
  ".venv",
  "__pycache__",
  "build",
  "coverage",
  "dist",
  "node_modules",
  "target",
  "venv",
]);

const CHECKS = [
  // ────────────────────────── Frontend table deletion ──────────────────────────
  {
    id: "no-context-window-by-model-table",
    from: "Frontend table deletion",
    root: "frontend/src",
    description: "CONTEXT_WINDOW_BY_MODEL model-window table does not appear anywhere under frontend/src",
    kind: "tree-absent",
    pattern: /\bCONTEXT_WINDOW_BY_MODEL\b/,
  },
  {
    id: "no-get-context-window-lookup",
    from: "Frontend table deletion",
    root: "frontend/src",
    description: "getContextWindow model-window lookup does not appear anywhere under frontend/src",
    kind: "tree-absent",
    pattern: /\bgetContextWindow\b/,
  },

  // ────────────────────────── Composer reads the durable window ──────────────────────────
  {
    id: "composer-reads-runtime-context-window",
    from: "Composer reads durable window",
    root: "frontend/src",
    description: "the composer context fraction reads the durable session-row field runtime_context_window_tokens",
    kind: "tree-present",
    pattern: /runtime_context_window_tokens/,
  },

  // ────────────────────────── Executable gates ──────────────────────────
  {
    id: "exec-removed-chat-runtime-guard",
    from: "Executable gates",
    description: "scripts/check-removed-chat-runtime.mjs exits 0 (the standing forbidden-surface guard stays green)",
    kind: "exec",
    command: ["node", "scripts/check-removed-chat-runtime.mjs"],
  },
];

// ─────────────────────────────────────────────────────────────────────────────
// Runner
// ─────────────────────────────────────────────────────────────────────────────

printHeader();

const results = [];
for (const check of CHECKS) {
  if (check.kind === "exec" && skipExec) {
    results.push({ check, pass: true, skipped: true, evidence: "SKIP_EXEC=1" });
    printResult(results[results.length - 1]);
    continue;
  }
  const result = await runCheck(check);
  results.push(result);
  printResult(result);
}

printSummary(results);
const failed = results.filter((r) => !r.pass);
process.exit(failed.length === 0 ? 0 : 1);

// ─────────────────────────────────────────────────────────────────────────────
// Dispatch
// ─────────────────────────────────────────────────────────────────────────────

async function runCheck(check) {
  try {
    const result = await dispatch(check);
    return { check, ...result };
  } catch (err) {
    return { check, pass: false, evidence: `error: ${err.message}` };
  }
}

async function dispatch(check) {
  switch (check.kind) {
    case "tree-absent":  return await treeAbsent(check);
    case "tree-present": return await treePresent(check);
    case "exec":         return execCheck(check);
    default: return { pass: false, evidence: `unknown kind: ${check.kind}` };
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Check implementations
// ─────────────────────────────────────────────────────────────────────────────

// treeAbsent fails if `pattern` matches in any non-binary file under `root`.
// It reports the first offending location, like check-removed-chat-runtime.mjs.
async function treeAbsent({ root, pattern }) {
  const rootAbs = path.join(repoRoot, root);
  if (!(await dirExists(rootAbs))) return { pass: false, evidence: `root missing: ${root}` };
  for await (const filePath of walk(rootAbs)) {
    const rel = toRepoPath(filePath);
    if (ignoredRelativePaths.has(rel)) continue;
    const bytes = await fs.readFile(filePath);
    if (bytes.includes(0)) continue;
    const text = bytes.toString("utf8");
    const match = pattern.exec(text);
    if (match) {
      const { line, column } = locate(text, match.index);
      return { pass: false, evidence: `${rel}:${line}:${column} present but should be absent: ${JSON.stringify(match[0])}` };
    }
  }
  return { pass: true, evidence: `${root}: pattern absent` };
}

// treePresent fails if `pattern` matches in no file under `root`. On success
// it reports the first matching location.
async function treePresent({ root, pattern }) {
  const rootAbs = path.join(repoRoot, root);
  if (!(await dirExists(rootAbs))) return { pass: false, evidence: `root missing: ${root}` };
  for await (const filePath of walk(rootAbs)) {
    const rel = toRepoPath(filePath);
    if (ignoredRelativePaths.has(rel)) continue;
    const bytes = await fs.readFile(filePath);
    if (bytes.includes(0)) continue;
    const text = bytes.toString("utf8");
    const match = pattern.exec(text);
    if (match) {
      const { line } = locate(text, match.index);
      return { pass: true, evidence: `${rel}:${line}` };
    }
  }
  return { pass: false, evidence: `${root}: pattern not found: ${pattern}` };
}

function execCheck({ command, cwd }) {
  const cwdAbs = cwd ? path.join(repoRoot, cwd) : repoRoot;
  const result = spawnSync(command[0], command.slice(1), {
    cwd: cwdAbs,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.error) return { pass: false, evidence: `spawn error: ${result.error.message}` };
  if (result.status !== 0) {
    const stream = (result.stderr && result.stderr.trim()) || (result.stdout && result.stdout.trim()) || "";
    const tail = stream.split("\n").slice(-3).join(" ¶ ").slice(0, 240);
    return { pass: false, evidence: `exit ${result.status}: ${tail}` };
  }
  return { pass: true, evidence: `exit 0` };
}

// ─────────────────────────────────────────────────────────────────────────────
// Output
// ─────────────────────────────────────────────────────────────────────────────

function printHeader() {
  const byCategory = new Map();
  for (const check of CHECKS) {
    byCategory.set(check.from, (byCategory.get(check.from) ?? 0) + 1);
  }
  console.log(`Context-window-table migration manifest: ${CHECKS.length} checks across ${byCategory.size} categories`);
  for (const [cat, n] of byCategory) console.log(`  ${String(n).padStart(2)} ${cat}`);
  if (skipExec) console.log("  (SKIP_EXEC=1 — exec gates will be marked PASS without running)");
  console.log("");
}

function printResult(r) {
  const sym = r.skipped ? "SKIP" : r.pass ? "PASS" : "FAIL";
  console.log(`${sym}  ${r.check.id.padEnd(46)}  ${r.check.description}`);
  if (!r.pass || r.skipped) {
    if (r.evidence) console.log(`      ↳ ${r.evidence}`);
  }
}

function printSummary(results) {
  const passed = results.filter((r) => r.pass && !r.skipped).length;
  const skipped = results.filter((r) => r.skipped).length;
  const failed = results.filter((r) => !r.pass);
  console.log("");
  console.log(`${passed}/${results.length} pass${skipped ? `, ${skipped} skipped` : ""}${failed.length ? `, ${failed.length} fail` : ""}`);
  if (failed.length) {
    console.log("");
    console.log("Failing checks:");
    for (const r of failed) {
      console.log(`  ${r.check.id}  [${r.check.from}]`);
      console.log(`      ${r.evidence}`);
    }
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

async function* walk(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const absolutePath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!ignoredDirs.has(entry.name)) yield* walk(absolutePath);
      continue;
    }
    if (!entry.isFile()) continue;
    yield absolutePath;
  }
}

async function dirExists(abs) {
  try {
    const stat = await fs.stat(abs);
    return stat.isDirectory();
  } catch {
    return false;
  }
}

function toRepoPath(filePath) {
  return path.relative(repoRoot, filePath).split(path.sep).join("/");
}

function locate(content, index) {
  const before = content.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return { line: lines.length, column: lines[lines.length - 1].length + 1 };
}
