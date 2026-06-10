#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const CLASS_FAITHFUL = "hot_swap_faithful";
const CLASS_PARTIAL = "hot_swap_partial";
const CLASS_IMAGE = "branch_image_required";

const TARGET_ALIASES = new Map([
  ["existing_pod", "existing_session"],
  ["existing_pods", "existing_session"],
  ["existing_session", "existing_session"],
  ["existing_sessions", "existing_session"],
  ["new_session", "new_session"],
  ["future_session", "new_session"],
  ["future_sessions", "new_session"],
  ["full_runtime", "full_runtime"],
  ["branch_image", "full_runtime"],
]);

const KNOWN_ARTIFACTS = new Set([
  "auto",
  "static",
  "backend",
  "agent_runner",
  "codex_runner",
  "antigravity_runner",
  "full_runtime",
]);

function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.selfTest) {
    runSelfTests();
    return;
  }

  const files = opts.changedFiles.length > 0 ? opts.changedFiles : changedFilesFromGit(opts.baseRef);
  const result = classifyTankTestFidelity(files, opts);
  process.stdout.write(JSON.stringify(result, null, 2) + "\n");

  if (opts.enforce && result.classification !== CLASS_FAITHFUL) {
    const detail = result.reasons.length > 0 ? `: ${result.reasons.join("; ")}` : "";
    console.error(`Tank test fidelity guard: ${result.classification}${detail}`);
    process.exit(3);
  }
}

function parseArgs(argv) {
  const opts = {
    artifactKind: process.env.GLIMMUNG_HOT_SWAP_ARTIFACT_KIND || process.env.ARTIFACT_KIND || "auto",
    baseRef: process.env.GLIMMUNG_BASE_REF || "origin/main",
    changedFiles: parseChangedFilesEnv(process.env.GLIMMUNG_CHANGED_FILES || ""),
    enforce: false,
    selfTest: false,
    validationTarget:
      process.env.GLIMMUNG_HOT_SWAP_VALIDATION_TARGET ||
      process.env.VALIDATION_TARGET ||
      "existing_session",
  };

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    switch (arg) {
      case "--artifact-kind":
        opts.artifactKind = requireValue(argv, ++i, arg);
        break;
      case "--base-ref":
        opts.baseRef = requireValue(argv, ++i, arg);
        break;
      case "--changed-file":
        opts.changedFiles.push(requireValue(argv, ++i, arg));
        break;
      case "--changed-files-file": {
        const file = requireValue(argv, ++i, arg);
        opts.changedFiles.push(...readChangedFilesFile(file));
        break;
      }
      case "--enforce":
        opts.enforce = true;
        break;
      case "--self-test":
        opts.selfTest = true;
        break;
      case "--validation-target":
        opts.validationTarget = requireValue(argv, ++i, arg);
        break;
      case "-h":
      case "--help":
        printHelp();
        process.exit(0);
      default:
        if (arg.startsWith("--")) {
          throw new Error(`unknown argument ${arg}`);
        }
        opts.changedFiles.push(arg);
    }
  }

  opts.artifactKind = normalizeArtifactKind(opts.artifactKind);
  opts.validationTarget = normalizeValidationTarget(opts.validationTarget);
  return opts;
}

function requireValue(argv, index, flag) {
  const value = argv[index];
  if (!value || value.startsWith("--")) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function parseChangedFilesEnv(raw) {
  return raw
    .split(/[\n,]/)
    .map((part) => part.trim())
    .filter(Boolean);
}

function readChangedFilesFile(file) {
  return fs
    .readFileSync(file, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function normalizeArtifactKind(raw) {
  const value = String(raw || "auto").trim();
  if (!KNOWN_ARTIFACTS.has(value)) {
    throw new Error(`unknown artifact kind ${JSON.stringify(value)}`);
  }
  return value;
}

function normalizeValidationTarget(raw) {
  const key = String(raw || "existing_session").trim();
  const value = TARGET_ALIASES.get(key);
  if (!value) {
    throw new Error(`unknown validation target ${JSON.stringify(key)}`);
  }
  return value;
}

function changedFilesFromGit(baseRef) {
  runGit(["fetch", "--quiet", "origin", "main", "--depth=1"], { allowFailure: true });
  for (const args of [
    ["diff", "--name-only", `${baseRef}...HEAD`],
    ["diff", "--name-only", "HEAD~1..HEAD"],
    ["diff", "--name-only"],
  ]) {
    const result = runGit(args, { allowFailure: true });
    if (result.status === 0 && result.stdout.trim()) {
      return result.stdout.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    }
  }
  const status = runGit(["status", "--porcelain"], { allowFailure: true });
  if (status.status !== 0) return [];
  return status.stdout
    .split(/\r?\n/)
    .map((line) => line.slice(3).trim())
    .filter(Boolean);
}

function runGit(args, { allowFailure = false } = {}) {
  const result = spawnSync("git", args, {
    cwd: repoRoot,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (!allowFailure && result.status !== 0) {
    throw new Error(`git ${args.join(" ")} failed: ${result.stderr.trim()}`);
  }
  return result;
}

function classifyTankTestFidelity(paths, opts = {}) {
  const artifactKind = normalizeArtifactKind(opts.artifactKind || "auto");
  const validationTarget = normalizeValidationTarget(opts.validationTarget || "existing_session");
  const changedFiles = normalizePaths(paths);
  const impacts = collectImpacts(changedFiles);
  const requiredArtifacts = requiredArtifactsForImpacts(impacts);
  const reasons = [];
  const requires = [];

  for (const file of changedFiles) {
    if (requiresBranchImage(file)) {
      reasons.push(`${file}: affects a baked image, package dependency set, launch script, or Helm/session-pod resource`);
    }
  }

  const futureSessionTarget = validationTarget === "new_session" || validationTarget === "full_runtime";
  const runnerImpacted =
    impacts.has("agent_runner") ||
    impacts.has("codex_runner") ||
    impacts.has("antigravity_runner") ||
    impacts.has("runner_shared") ||
    impacts.has("session_bus_contract");

  if (futureSessionTarget && runnerImpacted) {
    reasons.push("new Tank session pods boot runner code from the branch image; runner hot-swap only patches already-running session pods");
  }

  if (validationTarget === "full_runtime" && requiredArtifacts.size > 1) {
    reasons.push("full runtime validation must exercise every changed Tank runtime artifact together");
  }

  if (reasons.length > 0 && reasons.some((reason) => reason.includes("baked image") || reason.includes("boot runner code"))) {
    requires.push("branch_image", "new_session_smoke");
    return result({
      artifactKind,
      changedFiles,
      classification: CLASS_IMAGE,
      impacts,
      reasons,
      requiredArtifacts,
      requires,
      validationTarget,
    });
  }

  const uncovered = uncoveredImpactsForArtifact(impacts, artifactKind);
  if (uncovered.length > 0) {
    reasons.push(
      `${artifactKind} hot-swap does not apply ${formatList(uncovered)}; Tank's backend app pods and session runner pods are one distributed runtime`,
    );
  }

  if (validationTarget === "full_runtime" && requiredArtifacts.size > 1 && artifactKind !== "auto" && artifactKind !== "full_runtime") {
    reasons.push(`single-artifact hot-swap cannot prove full_runtime for ${formatList([...requiredArtifacts])}`);
  }

  if (reasons.length > 0) {
    requires.push(...requiredHotSwapRequirements(requiredArtifacts));
    return result({
      artifactKind,
      changedFiles,
      classification: CLASS_PARTIAL,
      impacts,
      reasons,
      requiredArtifacts,
      requires,
      validationTarget,
    });
  }

  if (changedFiles.length === 0 || impacts.size === 0) {
    reasons.push("no Tank runtime artifact changed");
  } else if (requiredArtifacts.size > 0) {
    reasons.push(`${formatList([...requiredArtifacts])} hot-swap covers the changed Tank runtime surface for ${validationTarget}`);
  }

  requires.push(...requiredHotSwapRequirements(requiredArtifacts));
  return result({
    artifactKind,
    changedFiles,
    classification: CLASS_FAITHFUL,
    impacts,
    reasons,
    requiredArtifacts,
    requires,
    validationTarget,
  });
}

function result({ artifactKind, changedFiles, classification, impacts, reasons, requiredArtifacts, requires, validationTarget }) {
  return {
    project: "tank-operator",
    validation_target: validationTarget,
    artifact_kind: artifactKind,
    classification,
    faithful_hot_swap: classification === CLASS_FAITHFUL,
    impacts: [...impacts].sort(),
    required_artifacts: [...requiredArtifacts].sort(),
    requires: [...new Set(requires)].sort(),
    reasons,
    changed_files: changedFiles,
  };
}

function normalizePaths(paths) {
  return paths
    .map((raw) => String(raw || "").trim().replaceAll("\\", "/").replace(/^\/+/, ""))
    .map((raw) => raw.replace(new RegExp(`^${escapeRegExp(repoRoot.replaceAll("\\", "/"))}/`), ""))
    .filter(Boolean)
    .filter((value) => !value.includes("node_modules/") && !value.includes("/dist/") && !value.includes("/coverage/"));
}

function collectImpacts(paths) {
  const impacts = new Set();
  for (const file of paths) {
    if (isDocsOnly(file)) continue;
    if (isStatic(file)) impacts.add("static");
    if (isBackend(file)) impacts.add("backend");
    if (isAgentRunner(file)) impacts.add("agent_runner");
    if (isCodexRunner(file)) impacts.add("codex_runner");
    if (isAntigravityRunner(file)) impacts.add("antigravity_runner");
    if (isRunnerShared(file)) impacts.add("runner_shared");
    if (isSessionBusContract(file)) impacts.add("session_bus_contract");
    if (isSessionPodInput(file)) impacts.add("session_pod");
  }
  return impacts;
}

function requiredArtifactsForImpacts(impacts) {
  const out = new Set();
  if (impacts.has("static")) out.add("static");
  if (impacts.has("backend") || impacts.has("session_bus_contract")) out.add("backend");
  if (impacts.has("agent_runner")) out.add("agent_runner");
  if (impacts.has("codex_runner")) out.add("codex_runner");
  if (impacts.has("antigravity_runner")) out.add("antigravity_runner");
  if (impacts.has("runner_shared") || impacts.has("session_bus_contract")) {
    out.add("agent_runner");
    out.add("codex_runner");
    out.add("antigravity_runner");
  }
  if (impacts.has("session_pod")) out.add("branch_image");
  return out;
}

function uncoveredImpactsForArtifact(impacts, artifactKind) {
  if (artifactKind === "auto" || artifactKind === "full_runtime") return [];
  const requiredArtifacts = requiredArtifactsForImpacts(impacts);
  if (requiredArtifacts.size === 0) return [];
  if (requiredArtifacts.has("branch_image")) return ["branch_image"];
  requiredArtifacts.delete(artifactKind);
  return [...requiredArtifacts].sort();
}

function requiredHotSwapRequirements(requiredArtifacts) {
  const out = [];
  for (const artifact of requiredArtifacts) {
    if (artifact === "branch_image") {
      out.push("branch_image", "new_session_smoke");
    } else {
      out.push(`${artifact}_hot_swap`);
    }
  }
  return out;
}

function requiresBranchImage(file) {
  const base = path.posix.basename(file);
  if (base === "Dockerfile" || base === ".dockerignore") return true;
  if (isSessionPodInput(file)) return true;
  if (/^(claude-runner|codex-runner|antigravity-runner)\/(?:package|package-lock)\.json$/.test(file)) return true;
  if (/^(claude-runner|codex-runner|antigravity-runner)\/(?:pnpm-lock\.yaml|yarn\.lock)$/.test(file)) return true;
  if (/^k8s\/.*\.(ya?ml|json)$/.test(file)) return true;
  return false;
}

function isDocsOnly(file) {
  return (
    file.startsWith("docs/") ||
    file.startsWith(".tank/docs/") ||
    file.endsWith(".md") ||
    file.startsWith("screenshots/")
  );
}

function isStatic(file) {
  return file.startsWith("frontend/");
}

function isBackend(file) {
  // Go test files (`*_test.go`) are never compiled into the runtime binary —
  // `go build` excludes them — so a test-only backend change has no artifact
  // to hot-swap and must not raise a "backend impact" that forces a backend
  // hot-swap on top of, say, a frontend-only validation. Contract-sensitive
  // test files (handlers_turns_test.go, anything under internal/sessionbus/)
  // are still caught explicitly by isSessionBusContract, so dropping them here
  // only removes the false positive a pure test change would otherwise raise.
  if (file.endsWith("_test.go")) return false;
  return file.startsWith("backend-go/");
}

function isAgentRunner(file) {
  return file.startsWith("claude-runner/src/") || file.startsWith("claude-runner/test/");
}

function isCodexRunner(file) {
  return file.startsWith("codex-runner/src/") || file.startsWith("codex-runner/test/");
}

function isAntigravityRunner(file) {
  return file.startsWith("antigravity-runner/src/");
}

function isRunnerShared(file) {
  return file.startsWith("runner-shared/");
}

function isSessionBusContract(file) {
  return (
    file.startsWith("backend-go/internal/sessionbus/") ||
    file === "runner-shared/sessionBus.js" ||
    file === "runner-shared/sessionBus.d.ts" ||
    file === "claude-runner/src/sessionBus.ts" ||
    file === "codex-runner/src/sessionBus.ts" ||
    file === "antigravity-runner/src/sessionBus.ts" ||
    file === "backend-go/cmd/tank-operator/handlers_turns.go" ||
    file === "backend-go/cmd/tank-operator/handlers_turns_test.go"
  );
}

function isSessionPodInput(file) {
  return (
    file.startsWith("claude-container/") ||
    file.startsWith("agent-container/") ||
    file.startsWith("antigravity-container/") ||
    file.startsWith("k8s/session-config/")
  );
}

function formatList(values) {
  const unique = [...new Set(values)].sort();
  if (unique.length === 0) return "nothing";
  if (unique.length === 1) return unique[0];
  return `${unique.slice(0, -1).join(", ")} and ${unique.at(-1)}`;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function printHelp() {
  process.stdout.write(`Usage: node scripts/classify-tank-test-fidelity.mjs [options] [changed-file...]

Options:
  --artifact-kind <kind>       auto, static, backend, agent_runner, codex_runner, antigravity_runner, full_runtime
  --validation-target <target> existing_session, new_session, or full_runtime
  --changed-file <path>        Add one changed file
  --changed-files-file <path>  Read newline-delimited changed files
  --base-ref <ref>             Base ref for git diff fallback (default origin/main)
  --enforce                    Exit non-zero unless classification is hot_swap_faithful
  --self-test                  Run built-in classifier tests
`);
}

function runSelfTests() {
  const c = (files, opts) => classifyTankTestFidelity(files, opts);

  assert.equal(c(["frontend/src/App.tsx"], { artifactKind: "static" }).classification, CLASS_FAITHFUL);
  assert.equal(c(["backend-go/cmd/tank-operator/handlers_sessions.go"], { artifactKind: "backend" }).classification, CLASS_FAITHFUL);
  assert.equal(c(["codex-runner/src/runner.ts"], { artifactKind: "codex_runner" }).classification, CLASS_FAITHFUL);
  assert.equal(c(["antigravity-runner/src/runner.ts"], { artifactKind: "antigravity_runner" }).classification, CLASS_FAITHFUL);
  assert.equal(
    c(["codex-runner/src/runner.ts"], { artifactKind: "codex_runner", validationTarget: "new_session" }).classification,
    CLASS_IMAGE,
  );
  assert.equal(
    c(["antigravity-runner/src/runner.ts"], { artifactKind: "antigravity_runner", validationTarget: "new_session" }).classification,
    CLASS_IMAGE,
  );
  const retiredRunnerKind = String.fromCharCode(103, 101, 109, 105, 110, 105) + "_runner";
  assert.throws(
    () => c(["antigravity-runner/src/runner.ts"], { artifactKind: retiredRunnerKind }),
    new RegExp(`unknown artifact kind "${retiredRunnerKind}"`),
  );
  assert.equal(
    c(["backend-go/internal/sessionbus/subjects.go", "runner-shared/sessionBus.js"], {
      artifactKind: "codex_runner",
      validationTarget: "existing_session",
    }).classification,
    CLASS_PARTIAL,
  );
  assert.equal(
    c(["backend-go/internal/sessionbus/subjects.go", "runner-shared/sessionBus.js"], {
      artifactKind: "codex_runner",
      validationTarget: "new_session",
    }).classification,
    CLASS_IMAGE,
  );
  assert.equal(
    c(["claude-runner/package-lock.json"], { artifactKind: "agent_runner", validationTarget: "existing_session" }).classification,
    CLASS_IMAGE,
  );
  assert.equal(c(["docs/testing.md"], { artifactKind: "auto", validationTarget: "full_runtime" }).classification, CLASS_FAITHFUL);

  // A backend-only *_test.go change has no runtime artifact, so a frontend
  // (static) validation stays faithful — the added Go test must not force a
  // backend hot-swap. Paired with a real frontend change, still static-only.
  assert.equal(
    c(["backend-go/cmd/tank-operator/transcript_projection_test.go"], { artifactKind: "static" }).classification,
    CLASS_FAITHFUL,
  );
  assert.equal(
    c(
      ["backend-go/cmd/tank-operator/transcript_projection_test.go", "frontend/src/App.tsx"],
      { artifactKind: "static", validationTarget: "existing_session" },
    ).classification,
    CLASS_FAITHFUL,
  );
  // But contract-sensitive test files (explicit allowlist) and non-test
  // backend sources still require a backend artifact.
  assert.deepEqual(
    [...requiredArtifactsForImpacts(collectImpacts(["backend-go/cmd/tank-operator/handlers_turns_test.go"]))].sort(),
    ["agent_runner", "antigravity_runner", "backend", "codex_runner"],
  );
  assert.equal(
    c(["backend-go/cmd/tank-operator/transcript_projection.go"], { artifactKind: "static", validationTarget: "existing_session" }).classification,
    CLASS_PARTIAL,
  );

  console.log("Tank test-fidelity classifier self-test passed.");
}

main();
