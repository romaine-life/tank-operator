#!/usr/bin/env node
// Migration guard: the tank-operator-specific v1 live-frontend-preview path was
// deleted end-to-end (the Stage 5 cutover). v1 was an in-app static-override
// receiver + a per-session SSE control stream + an in-pod live-preview daemon
// sidecar + a per-session sender script + an owner toggle + durable
// test_state.live_preview state + the supporting chart wiring and metric. It is
// superseded by the generic, repo-agnostic live-preview lane owned by Glimmung
// (the preview edge + k8s/session-config/live-preview-push.sh, the v2 sender
// that STAYS). Per docs/migration-policy.md the retired surface must not return.
//
// This guard fails if any deleted v1 symbol, route, filename, env var, or metric
// reappears in live code or docs. It is deliberately v1-specific so it never
// matches the v2 generic sender (live-preview-push.sh / live-preview-watch.sh),
// which legitimately carries the word "live-preview" and the edge route
// __live-preview/push.
//
// Modeled on glimmung/scripts/check-deleted-test-slot-hot-swap.mjs (forbidden
// list → walk → fail). Run directly as a CLI, or import
// collectLivePreviewV1Failures() — it is merged into the broader
// scripts/check-removed-chat-runtime.mjs guard so it rides that guard's CI
// wiring (backend-go/**, frontend/**, k8s/**, docs/**, scripts/**) without a
// separate workflow step (the governed session push cannot edit workflow files).

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

const ignoredDirs = new Set([
  ".git",
  "node_modules",
  "dist",
  "__pycache__",
  ".venv",
  "venv",
]);
// This guard names the retired patterns in its own forbidden list (assembled
// from fragments so the whole literal never appears here); exclude it from the
// scan as well, mirroring the sibling check-* guards.
const ignoredRelativePaths = new Set([
  "scripts/check-removed-live-preview-v1.mjs",
]);

// Identifier word-boundary matcher. Fragments are concatenated so the whole
// retired identifier never appears literally in this file.
const word = (...parts) => new RegExp(`\\b${parts.join("")}\\b`);

// Each rule is either { re } (regex, for word-char identifiers/env/metrics) or
// { needle } (substring, for routes/filenames that contain "/" "-" ".").
const FORBIDDEN = [
  // ── Deleted whole files (v1 receiver, control stream, daemon, sender) ──
  { needle: "handlers_static_" + "override.go", label: "deleted v1 static-override receiver file" },
  { needle: "handlers_live_preview_" + "stream.go", label: "deleted v1 live-preview control-stream file" },
  { needle: "live-preview-" + "daemon.sh", label: "deleted v1 in-pod live-preview daemon script" },
  { needle: "push-" + "frontend.sh", label: "deleted v1 per-session sender script" },

  // ── v1 internal/owner routes (the v2 edge route __live-preview/push is NOT matched) ──
  { needle: "/api/internal/" + "static-override", label: "deleted v1 static-override receiver route" },
  { needle: "/test-slot/" + "live-preview", label: "deleted v1 owner live-preview toggle route" },
  { needle: "live-preview/" + "stream", label: "deleted v1 live-preview control-stream route" },

  // ── v1 Go handlers / helpers ──
  { re: word("handleInternalPut", "StaticOverride"), label: "deleted v1 receiver PUT handler" },
  { re: word("handleInternalDelete", "StaticOverride"), label: "deleted v1 receiver DELETE handler" },
  { re: word("handleInternalLive", "PreviewStream"), label: "deleted v1 control-stream handler" },
  { re: word("handleInternalReportLive", "PreviewPush"), label: "deleted v1 push-receipt handler" },
  { re: word("handleSetLive", "PreviewEnabled"), label: "deleted v1 owner-toggle handler" },
  { re: word("extractStatic", "OverrideTar"), label: "deleted v1 archive extractor" },
  { re: word("flipStatic", "OverrideCurrent"), label: "deleted v1 atomic-flip helper" },
  { re: word("pruneStatic", "OverrideReleases"), label: "deleted v1 release pruner" },
  { re: word("static", "OverrideRoot"), label: "deleted v1 receiver root resolver" },
  { re: word("livePreview", "ControlFromState"), label: "deleted v1 control-state projector" },
  { re: word("emitLivePreview", "IfChanged"), label: "deleted v1 control-stream emitter" },

  // ── v1 durable state (manager) + manifest gate + sidecar sizer ──
  { re: word("UpdateLive", "PreviewState"), label: "deleted v1 durable state writer" },
  { re: word("LivePreview", "Patch"), label: "deleted v1 state patch type" },
  { re: word("applyLive", "PreviewPatch"), label: "deleted v1 state merge helper" },
  { re: word("LivePreview", "Daemon"), label: "deleted v1 sidecar manifest gate" },
  { re: word("livePreview", "Resources"), label: "deleted v1 sidecar resource sizer" },

  // ── v1 metric ──
  { re: word("tank_static_", "override_push_total"), label: "deleted v1 receiver metric" },
  { re: word("static", "OverridePushTotal"), label: "deleted v1 receiver metric var" },
  { re: word("recordStatic", "OverridePush"), label: "deleted v1 metric recorder" },

  // ── v1 env / chart wiring ──
  { re: word("SESSION_LIVE_PREVIEW_", "DAEMON_ENABLED"), label: "deleted v1 daemon-enable env" },
  { re: word("TANK_OPERATOR_STATIC_", "OVERRIDE_ROOT"), label: "deleted v1 receiver root env" },
  { re: word("TANK_OPERATOR_STATIC_", "OVERRIDE_DIR"), label: "deleted v1 receiver dir env" },
  { re: word("PUSH_FRONTEND_", "SCRIPT"), label: "deleted v1 sender-script env" },
  { re: word("LIVE_PREVIEW_DAEMON_", "SCRIPT"), label: "deleted v1 daemon-script env" },

  // ── v1 frontend toggle surface ──
  { re: word("readLive", "Preview"), label: "deleted v1 frontend state reader" },
  { re: word("setLive", "PreviewEnabled"), label: "deleted v1 frontend toggle caller" },
  { re: word("livePreview", "TogglePath"), label: "deleted v1 frontend toggle path" },
  { re: word("LivePreview", "State"), label: "deleted v1 frontend state type" },
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

export async function collectLivePreviewV1Failures() {
  const failures = [];
  for (const root of SCAN_ROOTS) {
    for await (const rel of walk(root)) {
      if (ignoredRelativePaths.has(rel)) continue;
      const content = await fs.readFile(path.join(repoRoot, rel), "utf8");
      for (const rule of FORBIDDEN) {
        const hit = rule.re ? rule.re.test(content) : content.includes(rule.needle);
        if (hit) failures.push(`${rel}: ${rule.label}`);
      }
    }
  }
  return failures;
}

export const LIVE_PREVIEW_V1_FAILURE_HINT =
  "The tank-operator v1 in-app live-preview path (static-override receiver, " +
  "control stream, in-pod daemon, per-session sender, owner toggle, durable " +
  "preview state, metric, chart wiring) was deleted end-to-end. The generic v2 " +
  "lane (k8s/session-config/live-preview-push.sh + Glimmung's preview edge) is " +
  "the replacement; see docs/live-preview-sender.md and docs/migration-policy.md. " +
  "The retired v1 names must not return.";

const isMain =
  process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;

if (isMain) {
  const failures = await collectLivePreviewV1Failures();
  if (failures.length > 0) {
    console.error(
      "Retired v1 live-preview surface reappears in live code or docs. " +
        LIVE_PREVIEW_V1_FAILURE_HINT +
        "\n" +
        failures.map((f) => `  - ${f}`).join("\n"),
    );
    process.exit(1);
  }
  console.log("OK: retired v1 live-preview surface is absent.");
}
