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
