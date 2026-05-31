#!/usr/bin/env node

// Completion manifest for the session-pod SDK runner hot-swap migration.
//
// This script is the spec. "Done" = exit 0. Same workflow as
// scripts/check-stop-request-migration.mjs: committed as commit 1 of the
// branch, before any feature code, so the contract is auditable
// independently of whatever the agent later writes.
//
// THE CONTRACT — three user-named checkboxes:
//
//   1. We can hot-swap whatever we couldn't hot-swap before — specifically
//      the SDK runner code inside session pods (today only the orchestrator
//      pod is hot-swappable; this PR closes that gap for the runner code
//      that drives Claude, Codex, and Gemini SDK integration).
//
//   2. Prod doesn't suffer because of a dev practice — the hot-swap path
//      is gated on renderMode=hot, exactly like the orchestrator's
//      hot-swap is today. Production session pods see zero behavioral
//      change. The image gains a small baked binary (tank-supervisor) and
//      a tiny shim script, both dormant when the env vars aren't set.
//
//   3. Nothing else is touched that already works — orchestrator hot-swap
//      paths, tank-supervisor source, the orchestrator Dockerfile, the
//      orchestrator backend hot-swap wiring, k8s/values.yaml's orchestrator
//      hot-swap defaults, and the existing static + backend sub-blocks of
//      README's test_slot_hot_swap entry retain their behavior.
//
// After merge, this script stays in scripts/ as a regression guard.
//
// Skip slow exec gates during structural iteration with:
//   SKIP_EXEC=1 node scripts/check-session-pod-hot-swap-migration.mjs

import { spawnSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const skipExec = process.env.SKIP_EXEC === "1";

const CHECKS = [
  // ───────────── Checkbox 1: hot-swap of agent-runner code now works ─────────────

  {
    id: "session-image-builds-supervisor",
    from: "Checkbox 1: hot-swap works",
    file: "claude-container/Dockerfile",
    description: "session image Dockerfile builds tank-supervisor (or copies it from a multi-stage build)",
    kind: "grep-present",
    pattern: /tank-supervisor/,
  },
  {
    id: "session-image-installs-supervisor",
    from: "Checkbox 1: hot-swap works",
    file: "claude-container/Dockerfile",
    description: "session image installs tank-supervisor at /app/tank-supervisor",
    kind: "grep-present",
    pattern: /\/app\/tank-supervisor/,
  },
  {
    id: "session-image-baked-shim",
    from: "Checkbox 1: hot-swap works",
    file: "claude-container/Dockerfile",
    description: "session image bakes a launch shim at /app/agent-runner-launch-binary.sh",
    kind: "grep-present",
    pattern: /\/app\/agent-runner-launch-binary\.sh/,
  },
  {
    id: "session-image-baked-gemini-shim",
    from: "Checkbox 1: hot-swap works",
    file: "claude-container/Dockerfile",
    description: "session image bakes a launch shim at /app/gemini-runner-launch-binary.sh",
    kind: "grep-present",
    pattern: /\/app\/gemini-runner-launch-binary\.sh/,
  },
  {
    id: "session-image-shim-execs-node",
    from: "Checkbox 1: hot-swap works",
    file: "claude-container/Dockerfile",
    description: "baked shim's contents include `exec node /opt/agent-runner/dist/index.js` (the baked-path entrypoint the supervisor falls back to when no hot artifact is present)",
    kind: "grep-present",
    pattern: /exec\s+node\s+\/opt\/agent-runner\/dist\/index\.js/,
  },
  {
    id: "sessionmodel-volume-name",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel.go",
    description: "PodManifest declares an `agent-runner-hot` emptyDir volume",
    kind: "grep-present",
    pattern: /agent-runner-hot/,
  },
  {
    id: "sessionmodel-mount-path",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel.go",
    description: "PodManifest mounts /var/run/agent-runner-hot on the agent-runner container",
    kind: "grep-present",
    pattern: /\/var\/run\/agent-runner-hot/,
  },
  {
    id: "sessionmodel-supervisor-child-env",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel.go",
    description: "PodManifest sets GLIMMUNG_SUPERVISOR_CHILD on the agent-runner container (baked shim path)",
    kind: "grep-present",
    pattern: /GLIMMUNG_SUPERVISOR_CHILD/,
  },
  {
    id: "sessionmodel-supervisor-hot-artifact-env",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel.go",
    description: "PodManifest sets GLIMMUNG_SUPERVISOR_HOT_ARTIFACT on the agent-runner container (writable shim path)",
    kind: "grep-present",
    pattern: /GLIMMUNG_SUPERVISOR_HOT_ARTIFACT/,
  },
  {
    id: "launch-script-supervisor-branch",
    from: "Checkbox 1: hot-swap works",
    file: "k8s/session-config/agent-runner-launch.sh",
    description: "launch script has a branch that exec's /app/tank-supervisor when supervisor env vars are present",
    kind: "grep-present",
    pattern: /exec\s+\/app\/tank-supervisor/,
  },
  {
    id: "gemini-launch-script-supervisor-branch",
    from: "Checkbox 1: hot-swap works",
    file: "k8s/session-config/gemini-runner-launch.sh",
    description: "Gemini launch script has a branch that exec's /app/tank-supervisor when supervisor env vars are present",
    kind: "grep-present",
    pattern: /exec\s+\/app\/tank-supervisor/,
  },
  {
    id: "readme-agent-runner-entry",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "README test_slot_hot_swap block has an agent_runner entry alongside static and backend",
    kind: "grep-present",
    pattern: /"agent_runner"\s*:\s*\{/,
  },
  {
    id: "readme-fidelity-classifier-entry",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "README test_slot_hot_swap block declares the Tank-specific test fidelity classifier command",
    kind: "grep-present",
    pattern: /"fidelity_classifier"[\s\S]{0,300}?"command"\s*:\s*"node scripts\/classify-tank-test-fidelity\.mjs"/,
  },
  {
    id: "tank-fidelity-classifier-self-test",
    from: "Checkbox 1: hot-swap works",
    description: "Tank-specific test fidelity classifier self-test exits 0",
    kind: "exec",
    command: ["node", "scripts/classify-tank-test-fidelity.mjs", "--self-test"],
  },
  {
    id: "readme-agent-runner-source",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "agent_runner entry declares source: agent-runner/hot so dist and runner-shared are swapped together",
    kind: "grep-present",
    pattern: /"agent_runner"[\s\S]{0,900}?"source"\s*:\s*"agent-runner\/hot"/,
  },
  {
    id: "readme-agent-runner-target",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "agent_runner entry declares target: /var/run/agent-runner-hot so the hot artifact can include runner-shared",
    kind: "grep-present",
    pattern: /"agent_runner"[\s\S]{0,900}?"target"\s*:\s*"\/var\/run\/agent-runner-hot"/,
  },
  {
    id: "readme-agent-runner-bundles-shared",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "agent_runner build command copies runner-shared into the hot artifact and rewrites imports to that hot copy",
    kind: "grep-present",
    pattern: /"agent_runner"[\s\S]{0,500}?"build_command"[\s\S]{0,500}?cp -R \.\.\/runner-shared hot\/runner-shared[\s\S]{0,500}?\/var\/run\/agent-runner-hot\/runner-shared/,
  },
  {
    id: "readme-agent-runner-selector-mode",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "agent_runner pod selector is narrowed to claude_gui session pods",
    kind: "grep-present",
    pattern: /"agent_runner"[\s\S]{0,1200}?"pod_selector"\s*:\s*"tank-operator\/session-id,tank-operator\/mode=claude_gui"/,
  },
  {
    id: "readme-agent-runner-restart",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "agent_runner entry declares restart: SIGHUP (matches the supervisor's re-exec signal)",
    kind: "grep-present",
    pattern: /"agent_runner"[\s\S]{0,900}?"restart"\s*:\s*"SIGHUP"/,
  },
  {
    id: "readme-codex-runner-source",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "codex_runner entry declares source: codex-runner/hot so dist and runner-shared are swapped together",
    kind: "grep-present",
    pattern: /"codex_runner"[\s\S]{0,900}?"source"\s*:\s*"codex-runner\/hot"/,
  },
  {
    id: "readme-codex-runner-target",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "codex_runner entry declares target: /var/run/codex-runner-hot so the hot artifact can include runner-shared",
    kind: "grep-present",
    pattern: /"codex_runner"[\s\S]{0,900}?"target"\s*:\s*"\/var\/run\/codex-runner-hot"/,
  },
  {
    id: "readme-codex-runner-bundles-shared",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "codex_runner build command copies runner-shared into the hot artifact and rewrites imports to that hot copy",
    kind: "grep-present",
    pattern: /"codex_runner"[\s\S]{0,500}?"build_command"[\s\S]{0,500}?cp -R \.\.\/runner-shared hot\/runner-shared[\s\S]{0,500}?\/var\/run\/codex-runner-hot\/runner-shared/,
  },
  {
    id: "readme-codex-runner-selector-mode",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "codex_runner pod selector is narrowed to Codex GUI session pods",
    kind: "grep-present",
    pattern: /"codex_runner"[\s\S]{0,1200}?"pod_selector"\s*:\s*"tank-operator\/session-id,tank-operator\/mode in \(codex_gui,codex_exec_gui,codex_app_server\)"/,
  },
  {
    id: "readme-gemini-runner-source",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "gemini_runner entry declares source: gemini-runner/hot so dist and runner-shared are swapped together",
    kind: "grep-present",
    pattern: /"gemini_runner"[\s\S]{0,900}?"source"\s*:\s*"gemini-runner\/hot"/,
  },
  {
    id: "readme-gemini-runner-target",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "gemini_runner entry declares target: /var/run/gemini-runner-hot so the hot artifact can include runner-shared",
    kind: "grep-present",
    pattern: /"gemini_runner"[\s\S]{0,900}?"target"\s*:\s*"\/var\/run\/gemini-runner-hot"/,
  },
  {
    id: "readme-gemini-runner-bundles-shared",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "gemini_runner build command copies runner-shared into the hot artifact and rewrites imports to that hot copy",
    kind: "grep-present",
    pattern: /"gemini_runner"[\s\S]{0,500}?"build_command"[\s\S]{0,500}?cp -R \.\.\/runner-shared hot\/runner-shared[\s\S]{0,500}?\/var\/run\/gemini-runner-hot\/runner-shared/,
  },
  {
    id: "readme-gemini-runner-selector-mode",
    from: "Checkbox 1: hot-swap works",
    file: "README.md",
    description: "gemini_runner pod selector is narrowed to Gemini GUI/test session pods",
    kind: "grep-present",
    pattern: /"gemini_runner"[\s\S]{0,1200}?"pod_selector"\s*:\s*"tank-operator\/session-id,tank-operator\/mode in \(gemini_gui,gemini_test\)"/,
  },
  {
    id: "sessionmodel-test-slot-mode-attaches-volume",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel_test.go",
    description: "sessionmodel test asserts: with testEnv enabled, agent-runner container gets agent-runner-hot volumeMount + supervisor env vars",
    kind: "grep-present",
    pattern: /TestPodManifestSlotModeAttachesAgentRunnerHotSwap|testEnv[^\n]{0,200}agent-runner-hot/,
  },
  {
    id: "sessionmodel-test-slot-mode-attaches-gemini-volume",
    from: "Checkbox 1: hot-swap works",
    file: "backend-go/internal/sessionmodel/sessionmodel_test.go",
    description: "sessionmodel test asserts: with testEnv enabled, gemini-runner container gets gemini-runner-hot volumeMount + supervisor env vars",
    kind: "grep-present",
    pattern: /TestPodManifestSlotModeAttachesGeminiRunnerHotSwap|gemini-runner-hot/,
  },

  // ───────────── Checkbox 2: prod doesn't suffer ─────────────

  {
    id: "sessionmodel-prod-no-volume",
    from: "Checkbox 2: prod unchanged",
    file: "backend-go/internal/sessionmodel/sessionmodel_test.go",
    description: "sessionmodel test asserts: with testEnv disabled, agent-runner container has NO agent-runner-hot volume / mount / env vars",
    kind: "grep-present",
    pattern: /TestPodManifestProdLeavesAgentRunnerUnchanged|prod[^\n]{0,200}agent-runner-hot[^\n]{0,200}absent/,
  },
  {
    id: "launch-script-prod-fallback",
    from: "Checkbox 2: prod unchanged",
    file: "k8s/session-config/agent-runner-launch.sh",
    description: "launch script still has `exec node /opt/agent-runner/dist/index.js` as the fallback path when supervisor env vars are not set",
    kind: "grep-present",
    pattern: /exec\s+node\s+\/opt\/agent-runner\/dist\/index\.js/,
  },
  {
    id: "gemini-launch-script-prod-fallback",
    from: "Checkbox 2: prod unchanged",
    file: "k8s/session-config/gemini-runner-launch.sh",
    description: "Gemini launch script still has `exec node /opt/gemini-runner/dist/index.js` as the fallback path when supervisor env vars are not set",
    kind: "grep-present",
    pattern: /exec\s+node\s+\/opt\/gemini-runner\/dist\/index\.js/,
  },
  {
    id: "launch-script-conditional-on-env",
    from: "Checkbox 2: prod unchanged",
    file: "k8s/session-config/agent-runner-launch.sh",
    description: "launch script conditionally branches on GLIMMUNG_SUPERVISOR_CHILD env (prod doesn't set it → fallthrough to direct exec)",
    kind: "grep-present",
    pattern: /GLIMMUNG_SUPERVISOR_CHILD/,
  },
  {
    id: "sessionmodel-gated-on-testEnv",
    from: "Checkbox 2: prod unchanged",
    file: "backend-go/internal/sessionmodel/sessionmodel.go",
    description: "PodManifest's hot-swap additions are gated on a testEnv-style boolean (mirrors orchestrator's pattern); the literal `agent-runner-hot` appears inside a conditional block",
    kind: "grep-present",
    pattern: /(TestEnv|testEnv|HotSwap|hotSwap)[\s\S]{0,800}?agent-runner-hot/,
  },

  // ───────────── Checkbox 3: nothing else touched ─────────────

  {
    id: "supervisor-source-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "backend-go/cmd/tank-supervisor/main.go is byte-identical to origin/main (the supervisor binary is reused as-is, with no edits)",
    kind: "git-diff-empty",
    paths: ["backend-go/cmd/tank-supervisor/main.go"],
    base: "origin/main",
  },
  {
    id: "supervisor-process-unix-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "backend-go/cmd/tank-supervisor/process_unix.go is byte-identical to origin/main",
    kind: "git-diff-empty",
    paths: ["backend-go/cmd/tank-supervisor/process_unix.go"],
    base: "origin/main",
  },
  {
    id: "orchestrator-dockerfile-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "the orchestrator Dockerfile (root Dockerfile) is byte-identical to origin/main",
    kind: "git-diff-empty",
    paths: ["Dockerfile"],
    base: "origin/main",
  },
  {
    id: "orchestrator-deployment-supervisor-command-intact",
    from: "Checkbox 3: nothing else touched",
    description: "orchestrator deployment.yaml still wires /app/tank-supervisor as the command in test-env mode (existing hot-swap behavior preserved)",
    file: "k8s/templates/deployment.yaml",
    kind: "grep-present",
    pattern: /\/app\/tank-supervisor/,
  },
  {
    id: "orchestrator-deployment-hot-backend-volume-intact",
    from: "Checkbox 3: nothing else touched",
    description: "orchestrator deployment.yaml still declares the hot-backend volume (orchestrator hot-swap volume preserved)",
    file: "k8s/templates/deployment.yaml",
    kind: "grep-present",
    pattern: /hot-backend/,
  },
  {
    id: "orchestrator-deployment-supervisor-env-intact",
    from: "Checkbox 3: nothing else touched",
    description: "orchestrator deployment.yaml still sets GLIMMUNG_SUPERVISOR_CHILD + GLIMMUNG_SUPERVISOR_HOT_ARTIFACT on the orchestrator container (env wiring preserved)",
    file: "k8s/templates/deployment.yaml",
    kind: "grep-present",
    pattern: /GLIMMUNG_SUPERVISOR_CHILD[\s\S]{0,800}?GLIMMUNG_SUPERVISOR_HOT_ARTIFACT/,
  },
  {
    id: "orchestrator-hotswap-values-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "k8s/values.yaml's existing hotSwapBackend block is unchanged (the block is byte-identical to origin/main; this PR may add a SIBLING block for sessions, but the orchestrator block is untouched)",
    kind: "yaml-block-unchanged",
    file: "k8s/values.yaml",
    blockKey: "hotSwapBackend",
    base: "origin/main",
  },
  {
    id: "readme-static-block-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "the static sub-block of README's test_slot_hot_swap is unchanged (only SDK runner sub-blocks are added)",
    kind: "json-block-unchanged",
    file: "README.md",
    parentJSONPath: ["test_slot_hot_swap"],
    subKey: "static",
  },
  {
    id: "readme-backend-block-unchanged",
    from: "Checkbox 3: nothing else touched",
    description: "the backend sub-block of README's test_slot_hot_swap is unchanged",
    kind: "json-block-unchanged",
    file: "README.md",
    parentJSONPath: ["test_slot_hot_swap"],
    subKey: "backend",
  },

  // ───────────── Executable gates ─────────────

  {
    id: "exec-sessionmodel-tests",
    from: "Executable gates",
    description: "go test ./internal/sessionmodel/... passes (covers the new slot-mode pod spec + the prod-mode negative-space test)",
    kind: "exec",
    command: ["go", "test", "./internal/sessionmodel/..."],
    cwd: "backend-go",
  },
  {
    id: "exec-removed-chat-runtime-guard",
    from: "Executable gates",
    description: "scripts/check-removed-chat-runtime.mjs exits 0",
    kind: "exec",
    command: ["node", "scripts/check-removed-chat-runtime.mjs"],
  },
  {
    id: "exec-helm-template",
    from: "Executable gates",
    description: "helm template k8s renders cleanly (chart still valid)",
    kind: "exec",
    command: ["helm", "template", "tank-operator", "k8s"],
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
    case "grep-present":          return await grepPresent(check);
    case "grep-absent":           return await grepAbsent(check);
    case "git-diff-empty":        return gitDiffEmpty(check);
    case "yaml-block-unchanged":  return yamlBlockUnchanged(check);
    case "json-block-unchanged":  return await jsonBlockUnchanged(check);
    case "exec":                  return execCheck(check);
    default: return { pass: false, evidence: `unknown kind: ${check.kind}` };
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Check implementations
// ─────────────────────────────────────────────────────────────────────────────

async function grepPresent({ file, pattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const match = pattern.exec(content);
  if (!match) return { pass: false, evidence: `pattern not found in ${file}: ${pattern}` };
  const { line } = locate(content, match.index);
  return { pass: true, evidence: `${file}:${line}` };
}

async function grepAbsent({ file, pattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const match = pattern.exec(content);
  if (match) {
    const { line, column } = locate(content, match.index);
    const preview = match[0].replace(/\s+/g, " ").slice(0, 80);
    return { pass: false, evidence: `${file}:${line}:${column} present but should be absent: ${JSON.stringify(preview)}` };
  }
  return { pass: true, evidence: `${file}: pattern absent` };
}

function gitDiffEmpty({ paths, base }) {
  const result = spawnSync("git", ["diff", "--quiet", base, "--", ...paths], {
    cwd: repoRoot,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.error) return { pass: false, evidence: `spawn error: ${result.error.message}` };
  // git diff --quiet exits 0 if no diff, 1 if diff, anything else on error.
  if (result.status === 0) return { pass: true, evidence: `unchanged vs ${base}: ${paths.join(", ")}` };
  if (result.status === 1) {
    return { pass: false, evidence: `MODIFIED vs ${base}: ${paths.join(", ")}` };
  }
  return { pass: false, evidence: `git diff failed (status=${result.status}): ${result.stderr.trim().slice(0, 200)}` };
}

function yamlBlockUnchanged({ file, blockKey, base }) {
  // Crude but effective: extract the contiguous block under `blockKey:` from
  // both HEAD and base, normalize whitespace, compare. The block is the lines
  // following `<blockKey>:` up to the next top-level key (no leading space).
  const headResult = spawnSync("git", ["show", `HEAD:${file}`], { cwd: repoRoot, encoding: "utf8" });
  const baseResult = spawnSync("git", ["show", `${base}:${file}`], { cwd: repoRoot, encoding: "utf8" });
  if (headResult.status !== 0) return { pass: false, evidence: `HEAD:${file} read failed: ${headResult.stderr.trim().slice(0, 200)}` };
  if (baseResult.status !== 0) return { pass: false, evidence: `${base}:${file} read failed: ${baseResult.stderr.trim().slice(0, 200)}` };
  const headBlock = extractYAMLBlock(headResult.stdout, blockKey);
  const baseBlock = extractYAMLBlock(baseResult.stdout, blockKey);
  if (headBlock === null) return { pass: false, evidence: `${file}: block ${blockKey} not found in HEAD` };
  if (baseBlock === null) return { pass: false, evidence: `${file}: block ${blockKey} not found in ${base}` };
  if (headBlock !== baseBlock) {
    return { pass: false, evidence: `${file}: block ${blockKey} differs from ${base}` };
  }
  return { pass: true, evidence: `${file}: block ${blockKey} unchanged vs ${base}` };
}

function extractYAMLBlock(yamlContent, blockKey) {
  const lines = yamlContent.split(/\r?\n/);
  const startRe = new RegExp(`^${blockKey}\\s*:\\s*$`);
  let startIdx = -1;
  for (let i = 0; i < lines.length; i++) {
    if (startRe.test(lines[i])) { startIdx = i; break; }
  }
  if (startIdx < 0) return null;
  // Block runs until the next top-level key (a line that starts with a
  // non-whitespace char and contains ':') or EOF.
  const out = [lines[startIdx]];
  for (let i = startIdx + 1; i < lines.length; i++) {
    const line = lines[i];
    if (/^\S/.test(line) && line.includes(":")) break;
    out.push(line);
  }
  return out.join("\n");
}

async function jsonBlockUnchanged({ file, parentJSONPath, subKey }) {
  // Extract the JSON block embedded in README (or any markdown) by finding
  // the first ```json fence whose body parses to an object containing the
  // path. Then compare the subKey's sub-object stringified between HEAD and
  // origin/main.
  const headResult = spawnSync("git", ["show", `HEAD:${file}`], { cwd: repoRoot, encoding: "utf8" });
  const baseResult = spawnSync("git", ["show", `origin/main:${file}`], { cwd: repoRoot, encoding: "utf8" });
  if (headResult.status !== 0) return { pass: false, evidence: `HEAD:${file} read failed: ${headResult.stderr.trim().slice(0, 200)}` };
  if (baseResult.status !== 0) return { pass: false, evidence: `origin/main:${file} read failed: ${baseResult.stderr.trim().slice(0, 200)}` };
  const headSub = findJSONSubBlock(headResult.stdout, parentJSONPath, subKey);
  const baseSub = findJSONSubBlock(baseResult.stdout, parentJSONPath, subKey);
  if (headSub === undefined) return { pass: false, evidence: `${file}: sub-block ${[...parentJSONPath, subKey].join(".")} not found in HEAD` };
  if (baseSub === undefined) return { pass: false, evidence: `${file}: sub-block ${[...parentJSONPath, subKey].join(".")} not found in origin/main` };
  const a = JSON.stringify(headSub, Object.keys(headSub).sort());
  const b = JSON.stringify(baseSub, Object.keys(baseSub).sort());
  if (a !== b) {
    return { pass: false, evidence: `${file}: ${[...parentJSONPath, subKey].join(".")} differs from origin/main` };
  }
  return { pass: true, evidence: `${file}: ${[...parentJSONPath, subKey].join(".")} unchanged vs origin/main` };
}

function findJSONSubBlock(markdown, parentPath, subKey) {
  const fenceRe = /```json\s*\n([\s\S]*?)\n```/g;
  let m;
  while ((m = fenceRe.exec(markdown)) !== null) {
    try {
      const parsed = JSON.parse(m[1]);
      let node = parsed;
      for (const key of parentPath) {
        if (node && typeof node === "object" && key in node) node = node[key];
        else { node = undefined; break; }
      }
      if (node && typeof node === "object" && subKey in node) {
        return node[subKey];
      }
    } catch {
      // Skip non-JSON fences.
    }
  }
  return undefined;
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
  console.log(`Session-pod hot-swap manifest: ${CHECKS.length} checks across ${byCategory.size} categories`);
  for (const [cat, n] of byCategory) console.log(`  ${String(n).padStart(2)} ${cat}`);
  if (skipExec) console.log("  (SKIP_EXEC=1 — exec gates will be marked PASS without running)");
  console.log("");
}

function printResult(r) {
  const sym = r.skipped ? "SKIP" : r.pass ? "PASS" : "FAIL";
  console.log(`${sym}  ${r.check.id.padEnd(50)}  ${r.check.description}`);
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

async function fileExists(rel) {
  try {
    await fs.access(path.join(repoRoot, rel));
    return true;
  } catch {
    return false;
  }
}

async function readRel(rel) {
  return await fs.readFile(path.join(repoRoot, rel), "utf8");
}

function locate(content, index) {
  const before = content.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return { line: lines.length, column: lines[lines.length - 1].length + 1 };
}
