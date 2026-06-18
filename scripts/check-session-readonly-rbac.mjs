#!/usr/bin/env node

// Enforcement guard for the session-SA read-only migration.
//
// The Tank session ServiceAccount must be READ-ONLY on the cluster — never
// bound to cluster-admin. That grant was the kubectl-cp/exec bypass of the
// governed slot validation path (an agent could copy un-CI'd code straight into
// a test slot). Serious ad-hoc cluster writes go through a gated MCP path /
// break-glass, not the session's own credential.
//
// This renders the chart and asserts: the BASE session SA binds
// `tank-session-readonly`, and NO ClusterRoleBinding maps it to cluster-admin.
// Run in CI; exit 0 = the bypass stays closed.
//
// Exception: the separate `claude-session-trusted` SA IS bound to cluster-admin
// — it is the sanctioned, on-demand kubectl credential for NON-RESTRICTED
// sessions (minted by the orchestrator's cluster-credential endpoint, never the
// pod's own identity). It is matched exactly so it is not confused with the base
// `claude-session` SA, which must stay read-only.

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
let sawBaseClusterAdmin = false;
let sawTrustedClusterAdmin = false;
for (const doc of docs) {
  if (!/kind:\s*ClusterRoleBinding/.test(doc)) continue;
  // Subject SA name (exact) — distinguish the base `claude-session` from the
  // sanctioned `claude-session-trusted`.
  const subjMatch = /subjects:[\s\S]*?name:\s*(\S+)/.exec(doc);
  const subject = subjMatch ? subjMatch[1] : "";
  if (subject !== "claude-session" && subject !== "claude-session-trusted") continue;
  const roleRef = /roleRef:[\s\S]*?name:\s*(\S+)/.exec(doc);
  const role = roleRef ? roleRef[1] : "";
  if (subject === "claude-session") {
    if (role === "cluster-admin") sawBaseClusterAdmin = true;
    if (role === "tank-session-readonly") sawReadonly = true;
  } else if (subject === "claude-session-trusted") {
    if (role === "cluster-admin") sawTrustedClusterAdmin = true;
  }
}

const problems = [];
if (sawBaseClusterAdmin) {
  problems.push("base session SA (claude-session) is bound to cluster-admin — the kubectl-cp/exec bypass. It must be read-only.");
}
if (!sawReadonly) {
  problems.push("base session SA (claude-session) is not bound to tank-session-readonly — the expected read-only binding is missing.");
}

if (problems.length) {
  console.error("check-session-readonly-rbac: FAIL");
  for (const p of problems) console.error("  - " + p);
  process.exit(1);
}
const trustedNote = sawTrustedClusterAdmin
  ? " claude-session-trusted holds the sanctioned cluster-admin (non-restricted kubectl)."
  : "";
console.log("check-session-readonly-rbac: OK — base session SA is read-only (tank-session-readonly); no base cluster-admin binding." + trustedNote);
