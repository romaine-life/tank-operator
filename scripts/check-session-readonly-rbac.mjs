#!/usr/bin/env node

// Enforcement guard for the session-SA read-only migration.
//
// The Tank session ServiceAccount must be READ-ONLY on the cluster — never
// bound to cluster-admin. That grant was the kubectl-cp/exec bypass of the
// CI-gated hot-swap path (an agent could copy un-CI'd code straight into a test
// slot). Removing it is the enforcement half of routing every slot hot-swap
// through `apply_test_slot_hot_swap`. Serious ad-hoc cluster writes go through a
// gated MCP path / break-glass, not the session's own credential.
//
// This renders the chart and asserts: the session SA binds
// `tank-session-readonly`, and NO ClusterRoleBinding maps it to cluster-admin.
// Run in CI; exit 0 = the bypass stays closed.

import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const render = spawnSync("helm", ["template", "tank-operator", "k8s", "--set", "renderWarm=true"], {
  cwd: repoRoot,
  encoding: "utf8",
});
if (render.status !== 0) {
  console.error("helm template failed:", (render.stderr || render.stdout || "").trim().slice(-400));
  process.exit(2);
}

// Split into YAML docs; inspect each ClusterRoleBinding whose subject is the
// session SA (claude-session in the sessions namespace).
const docs = render.stdout.split(/^---\s*$/m);
let sawReadonly = false;
let sawClusterAdmin = false;
for (const doc of docs) {
  if (!/kind:\s*ClusterRoleBinding/.test(doc)) continue;
  // subject must be the session SA
  if (!/subjects:[\s\S]*?name:\s*claude-session\b/.test(doc)) continue;
  const roleRef = /roleRef:[\s\S]*?name:\s*(\S+)/.exec(doc);
  const role = roleRef ? roleRef[1] : "";
  if (role === "cluster-admin") sawClusterAdmin = true;
  if (role === "tank-session-readonly") sawReadonly = true;
}

const problems = [];
if (sawClusterAdmin) {
  problems.push("session SA is bound to cluster-admin — the kubectl-cp/exec bypass. It must be read-only.");
}
if (!sawReadonly) {
  problems.push("session SA is not bound to tank-session-readonly — the expected read-only binding is missing.");
}

if (problems.length) {
  console.error("check-session-readonly-rbac: FAIL");
  for (const p of problems) console.error("  - " + p);
  process.exit(1);
}
console.log("check-session-readonly-rbac: OK — session SA is read-only (tank-session-readonly); no cluster-admin binding.");
