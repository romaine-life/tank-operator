#!/usr/bin/env node
// Migration guard: the governed-merge gate was renamed off the retired
// "hot-swap" framing (Slice 6). The internal verification endpoint
// POST /api/internal/sessions/{id}/hot-swap/verify, its handler, request/response
// types, and helper functions are now spelled "governed-merge". "hot-swap" was
// stale terminology — the test-slot deploy is provisioned server-side by the
// deterministic gate, not by this endpoint, and docs mislabeled the endpoint a
// "compatibility facade." The endpoint itself is load-bearing and unchanged: it
// composes the shared PR-readiness primitive + governed-publish-proof into an
// allow/deny decision, and its sole caller is the merge MCP tool.
//
// Per docs/migration-policy.md the retired names must not return. This guard
// fails if the OLD gate route/handler/type/function names reappear in live code
// or docs.
//
// NOTE: This guard is deliberately scoped to the governed-MERGE gate's retired
// names. The distinct, still-live test-slot agent-runner code-swap surface
// (HotSwapAgentRunner / SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED / hotSwapBackend,
// guarded separately by scripts/check-session-pod-hot-swap-migration.mjs) shares
// the word "hot-swap" but is a different concept and must NOT be matched here.
//
// Run directly as a CLI, or import `collectHotSwapGateFailures()` — it is merged
// into the broader scripts/check-removed-chat-runtime.mjs guard so it rides that
// guard's CI wiring (backend-go/**, claude-container/**, docs/**, scripts/**)
// without a separate workflow step (the governed session push cannot edit
// workflow files).

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const SCAN_ROOTS = [
  "backend-go",
  "claude-container",
  "docs",
  "frontend",
  "k8s",
  "scripts",
  "README.md",
];

const ignoredDirs = new Set([".git", "node_modules", "dist", "__pycache__", ".venv", "venv"]);
// This guard names the retired patterns in prose; exclude it from its own scan.
const ignoredRelativePaths = new Set(["scripts/check-removed-hot-swap-verify-gate.mjs"]);

// Assembled from fragments so the guard file never matches its own scan.
const BLOCKED = [
  { needle: "hot-swap" + "/verify", label: "retired governed-merge gate route" },
  { needle: "handleInternalVerify" + "HotSwap", label: "retired gate handler name" },
  { needle: "hotSwap" + "VerificationRequest", label: "retired gate request type" },
  { needle: "hotSwap" + "VerificationResponse", label: "retired gate response type" },
  { needle: "evaluateHotSwap" + "Verification", label: "retired gate evaluator" },
  { needle: "applyHotSwap" + "ReadinessResult", label: "retired gate readiness applier" },
  { needle: "_post_tank_" + "hot_swap_verify", label: "retired proxy gate caller" },
  { needle: "_verify_github_" + "hot_swap_head", label: "retired proxy gate dead code" },
  { needle: "_repo_path_for_" + "hot_swap", label: "retired proxy gate dead code" },
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
    if (entry.isDirectory() && ignoredDirs.has(entry.name)) continue;
    yield* walk(path.join(rel, entry.name));
  }
}

export async function collectHotSwapGateFailures() {
  const failures = [];
  for (const root of SCAN_ROOTS) {
    for await (const rel of walk(root)) {
      if (ignoredRelativePaths.has(rel)) continue;
      const content = await fs.readFile(path.join(repoRoot, rel), "utf8");
      for (const { needle, label } of BLOCKED) {
        if (content.includes(needle)) {
          failures.push(`${rel}: ${label} (${needle})`);
        }
      }
    }
  }
  return failures;
}

export const HOT_SWAP_GATE_FAILURE_HINT =
  "The governed-merge gate is named /governed-merge/verify " +
  "(handleInternalVerifyGovernedMerge / governedMergeVerification* / " +
  "evaluateGovernedMerge* / _post_tank_governed_merge_verify). The retired " +
  '"hot-swap" gate names must not return; see docs/migration-policy.md.';

const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;

if (isMain) {
  const failures = await collectHotSwapGateFailures();
  if (failures.length > 0) {
    console.error(
      "Retired hot-swap governed-merge gate names reappear in live code or docs. " +
        HOT_SWAP_GATE_FAILURE_HINT +
        "\n" +
        failures.map((f) => `  - ${f}`).join("\n"),
    );
    process.exit(1);
  }
  console.log("OK: retired hot-swap governed-merge gate surface is absent.");
}
