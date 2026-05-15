#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const ignoredDirs = new Set([
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

const ignoredFiles = new Set([
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "go.sum",
]);

const ignoredRelativePaths = new Set([
  "scripts/check-removed-chat-runtime.mjs",
  "scripts/check-tank-conversation-contract.mjs",
  "backend-go/cmd/tank-operator/server_static_test.go",
  "frontend/src/migrationPolicy.test.ts",
]);

const blocked = [
  { name: "session run create API", pattern: /\/api\/sessions\/run\b/ },
  { name: "internal session run API", pattern: /\/api\/internal\/sessions\/run\b/ },
  { name: "run active API", pattern: /\/run\/active\b/ },
  { name: "run history API", pattern: /\/run\/history\b/ },
  { name: "latest run events API", pattern: /\/runs\/latest\/events(?:\.json)?\b/ },
  { name: "run events API", pattern: /\/runs\/\{run_id\}\/events\b/ },
  { name: "bare run route registration", pattern: /HandleFunc\(\s*["']\/run["']/ },
  { name: "headless dispatcher", pattern: /\bDispatchHeadless\b/ },
  { name: "headless run script", pattern: /headless-run\.sh\b/ },
  { name: "provider event adapter", pattern: /\bproviderEventAdapters\b/ },
  { name: "removed conversation source", pattern: /\blegacy-run\b/ },
  { name: "removed conversation source constant", pattern: /\bSourceLegacyRun\b/ },
  { name: "active run store", pattern: /\bactive[-_]runs\b/i },
  { name: "run event store", pattern: /\brun[-_]events\b/i },
  { name: "removed runtime discriminator", pattern: /runtime\??:\s*["']sdk["']\s*\|\s*["']legacy["']/ },
  { name: "session runtime branch", pattern: /\bsession\.runtime\b/ },
  { name: "old subscription mode alias", pattern: /\bsubscription_headless\b/ },
  { name: "old codex mode alias", pattern: /\bcodex_headless\b/ },
  { name: "old codex subscription alias", pattern: /\bcodex_subscription\b/ },
  { name: "old pi subscription alias", pattern: /\bpi_subscription\b/ },
  { name: "direct Codex credential mirror", pattern: /\bCodexCredsSecret\b/ },
  { name: "default direct Codex credential mirror", pattern: /\bDefaultCodexCredsSecret\b/ },
  { name: "retired agent runner websocket route", pattern: /\/agent-ws\b/ },
  { name: "retired agent runner websocket port", pattern: /\bAGENT_RUNNER_WS_PORT\b/ },
  { name: "retired websocket fanout", pattern: /\bWSFanout\b/ },
  { name: "retired websocket frame type", pattern: /\bClientFrame\b/ },
  { name: "retired Tank order key storage name", pattern: /\btank_order_key\b/ },
  { name: "retired Tank event sequence storage name", pattern: /\btank_event_seq\b/ },
  { name: "retired frontend activity poll interval", pattern: /\bPOLL_INTERVAL_MS\b/ },
  { name: "retired frontend activity polling loop", pattern: /setInterval\(\s*refreshSessionActivity/ },
  { name: "retired internal session event notify route", pattern: /\/events\/notify\b/ },
  { name: "retired in-memory session event broker", pattern: /\bsessionEventBroker\b/ },
  { name: "retired session event notifier", pattern: /\bSessionEventNotifier\b/ },
  { name: "retired runner session notify helper", pattern: /\bsessionNotify\b/ },
  { name: "retired turn queue name", pattern: /\bturn[-_ ]queue\b/i },
  { name: "retired TurnQueue type", pattern: /\bTurnQueue\b/ },
  { name: "retired turnQueue identifier", pattern: /\bturnQueue\b/ },
  { name: "retired turn queue env var", pattern: /\bCOSMOS_TURN_QUEUE_CONTAINER\b/ },
  { name: "retired turn queue env prefix", pattern: /\bTURN_QUEUE_/ },
  { name: "retired runner Cosmos event module", pattern: /\b(?:agent|codex)-runner\/src\/cosmos\.ts\b/ },
  { name: "retired runner Cosmos tests", pattern: /\bcosmos\.test\.ts\b/ },
  { name: "retired session Azure config secret", pattern: /\bSESSION_AZURE_CONFIG_SECRET\b/ },
  { name: "retired session Azure config option", pattern: /\bSessionAzureConfigSecret\b/ },
  { name: "retired session Azure config default", pattern: /\bDefaultSessionAzureConfigSecret\b/ },
  { name: "retired session workload identity resource", pattern: /\btank_session_identity\b/ },
  // Producer-permissive raw-provider event dispatch (the SDK migration's
  // hidden dual path). Tank events are the only thing on the session bus
  // now; isCanonical / CANONICAL_TYPES / stampEventID were the runner-side
  // filter that let raw provider events through. tank.user_message was a
  // phantom canonical type that never matched the schema. The conditional
  // stampers (stampEventID, stampTankEvent local copies) silently emitted
  // half-envelopes; the unconditional runner-shared stampTankEvent throws
  // instead.
  { name: "removed CANONICAL_TYPES producer-permissive filter", pattern: /\bCANONICAL_TYPES\b/ },
  { name: "removed isCanonical producer-side filter", pattern: /\bisCanonical\b/ },
  { name: "removed local stampEventID stamper", pattern: /\bstampEventID\b/ },
  { name: "removed phantom canonical tank.user_message type", pattern: /\btank\.user_message\b/ },
  { name: "duplicate codex-runner conversation contract", pattern: /codex-runner\/src\/conversation\.ts\b/ },
  { name: "duplicate agent-runner conversation contract", pattern: /agent-runner\/src\/conversation\.ts\b/ },
  { name: "duplicate frontend tankConversation contract", pattern: /frontend\/src\/tankConversation\.ts\b/ },
  // Phantom Tank event types: schema/code surface that no production code
  // ever emitted. They became maintenance debt with no live emitter, so
  // they were deleted per docs/migration-policy.md (no inactive surface
  // without a concrete plan to wire it up).
  { name: "phantom conversation.started event type", pattern: /["']conversation\.started["']|\bEventConversationStarted\b/ },
  { name: "phantom conversation.archived event type", pattern: /["']conversation\.archived["']|\bEventConversationArchived\b/ },
  { name: "phantom session.activity_updated event type", pattern: /["']session\.activity_updated["']|\bEventActivityUpdated\b/ },
  { name: "phantom read_state.updated event type", pattern: /["']read_state\.updated["']|\bEventReadStateUpdated\b/ },
  { name: "phantom audit-only visibility", pattern: /["']audit-only["']|\bVisibilityAudit\b/ },
  // Dead-after-cutover symbols. The producer-side cutover (PR #461)
  // removed every caller of Bus.PublishEvent, EventSubject, and
  // SessionEventSink.create — the cleanup PR deleted the now-unreachable
  // definitions. Block reintroduction so a future refactor doesn't
  // accidentally rebuild the dual-publish path.
  { name: "removed Bus.PublishEvent", pattern: /\bfunc \(b \*Bus\) PublishEvent\b|\bsessionBus\.PublishEvent\b|\bbus\.PublishEvent\b/ },
  { name: "removed EventSubject helper", pattern: /\bfunc EventSubject\b|\bEventSubject\(/ },
  { name: "removed SessionEventSink.create", pattern: /\bsink\.create\(|create\(message: StampedTankEvent|create\(event: StampedTankEvent/ },
];

const failures = [];

for await (const filePath of walk(repoRoot)) {
  const relativePath = toRepoPath(filePath);
  if (ignoredRelativePaths.has(relativePath)) continue;
  const bytes = await fs.readFile(filePath);
  if (bytes.includes(0)) continue;
  const text = bytes.toString("utf8");
  for (const rule of blocked) {
    const match = rule.pattern.exec(text);
    if (!match) continue;
    const { line, column } = lineAndColumn(text, match.index);
    failures.push(`${relativePath}:${line}:${column} ${rule.name}: ${JSON.stringify(match[0])}`);
  }
}

if (failures.length > 0) {
  console.error("Removed chat runtime surface detected:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("No removed chat runtime surfaces found.");

async function* walk(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const absolutePath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!ignoredDirs.has(entry.name)) yield* walk(absolutePath);
      continue;
    }
    if (!entry.isFile()) continue;
    if (ignoredFiles.has(entry.name)) continue;
    yield absolutePath;
  }
}

function toRepoPath(filePath) {
  return path.relative(repoRoot, filePath).split(path.sep).join("/");
}

function lineAndColumn(text, index) {
  const before = text.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return {
    line: lines.length,
    column: lines[lines.length - 1].length + 1,
  };
}
