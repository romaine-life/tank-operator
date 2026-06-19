#!/usr/bin/env node
// Migration guard: the retired agent-facing test-slot *provisioning* steps must
// not reappear in the /test skill or the agent-facing testing docs.
//
// Test-slot provisioning is now deterministic and server-side: Tank's Test
// button / `POST /api/sessions/{id}/test-workflow/start` endpoint validates
// readiness and provisions the slot (checkout + deploy + GUI pill) from inside
// the backend, for every project. The three agent-facing MCP provisioning tool
// wrappers were removed from their servers:
//   - checkout_test_slot        (mcp-glimmung)
//   - deploy_image_to_test_slot (mcp-glimmung)
//   - set_test_environment      (mcp-tank-operator)
// and the /test skill was rewritten to stop instructing those manual steps.
//
// The underlying HTTP endpoints (glimmung `/v1/test-slots/*`, tank
// `/api/sessions/{id}/test-state`) and their backend/Go clients intentionally
// stay — they are what the deterministic gate drives server-side, so this guard
// deliberately does NOT scan backend code (where, e.g., a free-form control
// action `source_tool` audit label may legitimately name a tool). It scans only
// the agent-facing skill + testing docs, where a reappearing tool name would tell
// an agent to provision a slot by hand again — the exact regression being blocked.
//
// Run directly as a CLI, or import `collectTestSlotProvisioningFailures()` from
// the broader `check-removed-chat-runtime.mjs` guard (CI runs that one on every
// `k8s/**`, `docs/**`, and `scripts/**` change, so this guard rides along).

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Agent-facing surfaces that drive how a session uses the test workflow.
const SCAN_ROOTS = [
  "k8s/session-config/skills/common/test",
  "docs/testing.md",
];

// Assembled from fragments so the guard file never matches its own scan.
const BLOCKED = [
  "checkout_test_" + "slot",
  "deploy_image_to_test_" + "slot",
  "set_test_" + "environment",
];

async function* walk(rel) {
  const abs = path.join(repoRoot, rel);
  const stat = await fs.stat(abs);
  if (stat.isFile()) {
    yield rel;
    return;
  }
  for (const entry of await fs.readdir(abs, { withFileTypes: true })) {
    yield* walk(path.join(rel, entry.name));
  }
}

export async function collectTestSlotProvisioningFailures() {
  const failures = [];
  for (const root of SCAN_ROOTS) {
    for await (const rel of walk(root)) {
      const content = await fs.readFile(path.join(repoRoot, rel), "utf8");
      for (const needle of BLOCKED) {
        if (content.includes(needle)) {
          failures.push(`${rel}: ${needle}`);
        }
      }
    }
  }
  return failures;
}

export const TEST_SLOT_PROVISIONING_FAILURE_HINT =
  "Provisioning is deterministic and server-side via Tank's Test " +
  "button/endpoint — the /test skill and testing docs must not instruct " +
  "manual slot checkout/deploy/pill steps.";

const isMain =
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href;

if (isMain) {
  const failures = await collectTestSlotProvisioningFailures();
  if (failures.length > 0) {
    console.error(
      "Retired agent-facing test-slot provisioning tool names reappear in the " +
        "/test skill or testing docs. " +
        TEST_SLOT_PROVISIONING_FAILURE_HINT +
        "\n" +
        failures.map((f) => `  - ${f}`).join("\n"),
    );
    process.exit(1);
  }
  console.log(
    "OK: retired test-slot provisioning tool names absent from the /test skill " +
      "and testing docs.",
  );
}
