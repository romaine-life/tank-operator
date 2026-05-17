#!/usr/bin/env node

// Migration guard for the AskUserQuestion durable-resolution cutover.
//
// Background: AskUserQuestion was previously broken end-to-end because the
// agent-runner ran the Claude Agent SDK with `permissionMode: "bypassPermissions"`
// and no `canUseTool` callback. The SDK's built-in tool definition calls
// `checkPermissions` returning `{behavior:"ask", message:"Answer questions?"}`,
// which — with no host UI to answer — surfaces back as an is_error tool_result
// containing the literal string "Answer questions?". The frontend then rendered
// that as the "answered" state and the user never saw the option buttons.
//
// The cutover replaces that path entirely:
//
//   1. Runner switches off `bypassPermissions`, registers a `canUseTool`
//      callback that allow-passthroughs all non-AskUserQuestion tools and
//      gates AskUserQuestion on a durable input_reply by storing a resolver
//      in `pendingInputReplies` keyed by toolUseID.
//   2. `acceptInputReply` resolves the stored canUseTool promise with
//      `{behavior:"allow", updatedInput:{answers, annotations}}`. The SDK
//      then calls the tool's own `call()` and produces a canonical
//      tool_result. The hand-rolled `buildInputReplyMessage` synthetic
//      tool_result user message is deleted — it was the wrong shape.
//   3. The Tank `input_reply` command grows `Answers` (and optional
//      `Annotations`) fields, replacing the singular `InputReply` string.
//   4. The `tool.approval_resolved` event payload grows `answers` /
//      `annotations`, sourced from the canUseTool updatedInput we sent.
//      The frontend renders the answered state from the durable event,
//      not from local React state alone.
//   5. The frontend renders `q.question`, `q.header`, `q.options[]` with
//      `label` / `description` / `preview`, plus `q.multiSelect` semantics.
//
// This script enforces the cutover is complete and prevents regression:
//
//   * Forbidden patterns must NOT appear anywhere in the repo (modulo
//     well-known excluded paths — this script, tests that assert the
//     migration, and the SDK's own d.ts).
//   * Required patterns MUST appear in their named anchor files. Missing
//     anchors means the cutover regressed (e.g., someone removed the
//     `canUseTool` registration).
//
// Run from CI alongside the other migration guards. Fail-on-match is the
// guard contract — there are no warnings.

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

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

const ignoredFiles = new Set([
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "go.sum",
]);

// Files that legitimately reference the forbidden patterns: this script
// itself (it must list them to enforce them), and tests that assert the
// migration's guards stay in place. Keep this list minimal — every entry
// is a hole in the guard.
const ignoredRelativePaths = new Set([
  "scripts/check-askuserquestion-migration.mjs",
  "frontend/src/migrationPolicy.test.ts",
]);

// Forbidden: must NOT appear in any non-excluded file. Each entry is the
// retired/regressive surface plus a one-line explanation of why it's a
// deletion target. Adding an entry without a comment is a review smell —
// future maintainers need to understand WHY the pattern is forbidden, not
// just THAT it is.
const forbidden = [
  // --- Deleted synthetic tool_result path -----------------------------------
  //
  // Before the cutover, the runner manufactured a `{type:"tool_result", ...}`
  // user message via `buildInputReplyMessage` and pushed it onto the SDK's
  // user prompt queue when an input_reply arrived. The SDK had already
  // auto-failed AskUserQuestion via `checkPermissions:"ask"` by then, so the
  // synthetic message arrived after the tool was closed and either had no
  // effect or duplicated the result. The new path uses canUseTool's
  // `updatedInput.answers`, which is the SDK-blessed answer-injection
  // surface. The synthetic helper must not come back.
  {
    name: "removed buildInputReplyMessage synthetic tool_result helper",
    pattern: /\bbuildInputReplyMessage\b/,
  },
  {
    name: "removed inputReplyText singular-field extractor",
    pattern: /\binputReplyText\b/,
  },
  {
    name: "removed synthetic tool_result user message push (userQueue.push of an input_reply payload)",
    pattern: /userQueue\.push\([^)]*[Ii]nputReply/,
  },

  // --- Old singular `input_reply` payload field -----------------------------
  //
  // The Tank `input_reply` command type is unchanged (still keyed
  // `CommandInputReply = "input_reply"`). What changed is the payload shape:
  // the singular `input_reply: string` field is replaced by
  // `answers: map[string]string[]` plus optional `annotations`. The Go
  // struct field, the JSON wire field, and the runner's reader all need to
  // be gone. Match narrowly so the command type constant survives.
  {
    name: "removed singular InputReply struct field on SessionCommand",
    // Matches `InputReply string` and friends, but not method names that
    // contain InputReply (acceptInputReply, inputReplyTargetProviderItemID,
    // pendingInputReplies) — those stay.
    pattern: /\bInputReply\s+(?:string|json:|\*|\[\])/,
  },
  {
    name: "removed singular input_reply JSON payload field",
    // Matches `"input_reply": "<scalar>"` (the singular payload field on
    // the JSON wire). Does not match `Command = "input_reply"` (the command
    // type constant, which is a different positional shape).
    pattern: /["']input_reply["']\s*:\s*["'][^"']*["']/,
  },
  {
    name: "removed record.input_reply runner reader",
    pattern: /\brecord\.input_reply\b|\brecord\[\s*["']input_reply["']\s*\]/,
  },

  // --- Removed bypassPermissions for AskUserQuestion's gating path ----------
  //
  // The runner's SDK options used to set `permissionMode: "bypassPermissions"`
  // with no canUseTool. That mode skips canUseTool entirely, so there was no
  // way to suspend AskUserQuestion. The cutover switches to a mode that
  // routes through canUseTool. Narrow the match to the agent-runner so other
  // surfaces (tests pointing at the SDK's own type definitions, docs that
  // mention the mode by name) aren't broken.
  //
  // The companion `allowDangerouslySkipPermissions` flag is only meaningful
  // under bypassPermissions — without it, the flag is dead config.
  {
    name: "removed permissionMode: 'bypassPermissions' in agent-runner",
    pattern: /permissionMode\s*:\s*["']bypassPermissions["']/,
  },
  {
    name: "removed allowDangerouslySkipPermissions flag in agent-runner",
    pattern: /\ballowDangerouslySkipPermissions\b/,
  },

  // --- "Answer questions?" placeholder string -------------------------------
  //
  // This literal is the Claude Agent SDK's own `checkPermissions` message
  // for AskUserQuestion. It lives in the SDK's precompiled `claude` binary
  // (under node_modules, which we don't walk). If it shows up in OUR source
  // tree, it means either (a) someone copy-pasted the SDK's behavior into
  // our adapter as a fallback (regressing the cutover) or (b) someone
  // hard-coded the placeholder into a test fixture / renderer instead of
  // reading the real answer from the durable event. Both are deletion
  // targets per migration-policy.md.
  {
    name: "placeholder 'Answer questions?' string (SDK fallback leaked into our code)",
    pattern: /Answer questions\?/,
  },
];

// Required: each entry names an anchor file (relative repo path) and a
// pattern that MUST appear in it. Missing an anchor is a regression —
// e.g., someone deleted the canUseTool registration. The anchor file
// itself can move (rename the path here when it does), but the surface
// it represents has to live somewhere reachable.
//
// Anchor files exist; an anchor pattern missing from an existing file is
// the regression signal. If the anchor file itself disappears (e.g., the
// agent-runner is rewritten in Go), update this script in the same PR.
const required = [
  // --- Runner: canUseTool is registered and gates AskUserQuestion -----------
  {
    file: "agent-runner/src/runner.ts",
    name: "canUseTool option is passed into the SDK query()",
    pattern: /\bcanUseTool\s*:/,
  },
  {
    file: "agent-runner/src/runner.ts",
    name: "pendingInputReplies resolver map exists",
    pattern: /\bpendingInputReplies\b/,
  },
  {
    file: "agent-runner/src/runner.ts",
    name: "AskUserQuestion is named in the canUseTool gating logic",
    pattern: /["']AskUserQuestion["']/,
  },
  {
    file: "agent-runner/src/runner.ts",
    name: "updatedInput.answers shape is constructed for the SDK permission allow path",
    pattern: /updatedInput[^;]*answers/,
  },

  // --- Adapter: emits answers on tool.approval_resolved ---------------------
  {
    file: "agent-runner/src/adapters/claude.ts",
    name: "claude adapter emits answers on tool.approval_resolved",
    pattern: /tool\.approval_resolved[\s\S]{0,400}\banswers\b/,
  },

  // --- Backend command: Answers field on the input_reply command ------------
  {
    file: "backend-go/internal/sessionbus/commands.go",
    name: "input_reply command carries an Answers field",
    pattern: /\bAnswers\s+map\[string\]\[\]string\b/,
  },

  // --- Frontend: renders the question / options / new fields ----------------
  {
    file: "frontend/src/App.tsx",
    name: "ToolAskUserBody renders q.question",
    pattern: /q\.question\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "ToolAskUserBody renders q.header chip",
    pattern: /q\.header\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "ToolAskUserBody handles q.multiSelect",
    pattern: /q\.multiSelect\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "ToolAskUserBody renders option.preview content",
    pattern: /opt\.preview\b|option\.preview\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "ToolAskUserBody reads answers from the durable event payload (not only local React state)",
    // Either `entry.toolOutput`-as-answers-projection or a typed
    // `payload.answers` read counts. The point is the answered render
    // must consult durable state, not only `selectedAnswer`.
    pattern: /entry\.(?:payload|projectedAnswers|answers)\b|payload\.answers\b/,
  },

  // --- Protocol docs and fixtures -------------------------------------------
  {
    file: "docs/tank-conversation-protocol.md",
    name: "protocol doc describes canUseTool-gated AskUserQuestion resolution",
    pattern: /canUseTool/,
  },
  {
    file: "docs/tank-conversation-protocol.md",
    name: "protocol doc describes the answers payload on tool.approval_resolved",
    pattern: /tool\.approval_resolved[\s\S]{0,800}\banswers\b/,
  },
  {
    file: "schemas/tank-conversation-event.fixtures.json",
    name: "approval_resolved fixture carries an answers field",
    pattern: /tool\.approval_resolved[\s\S]{0,800}"answers"/,
  },

  // --- Codex parity is intentional, document it -----------------------------
  //
  // Codex (OpenAI Responses API) has no native AskUserQuestion equivalent.
  // The protocol doc must say Codex explicitly fails input_reply, not
  // because it's a TODO but because the migration policy's "unknown
  // callers are unsupported" applies — Codex is a known caller for which
  // this feature does not exist. If the line is removed from the doc, the
  // guard fires.
  {
    file: "docs/tank-conversation-protocol.md",
    name: "protocol doc states Codex explicitly does not support input_reply / AskUserQuestion",
    // Two orderings (Codex...input_reply or input_reply...Codex) within a
    // 600-char window, followed by a stance word that rules out fallback.
    // The window has to be generous because the surrounding paragraph
    // legitimately documents the input_reply command shape in detail
    // before the Codex-unsupported sentence. Stance vocabulary covers
    // "fails", "rejects", "does not support", "unsupported", "not
    // implemented" — any of these communicates the explicit gap.
    pattern:
      /(?:Codex[\s\S]{0,600}input_reply|input_reply[\s\S]{0,600}Codex)[\s\S]{0,400}(?:fail|reject|does not support|unsupported|not implement)/i,
  },
];

const failures = [];

for await (const filePath of walk(repoRoot)) {
  const relativePath = toRepoPath(filePath);
  if (ignoredRelativePaths.has(relativePath)) continue;
  const bytes = await fs.readFile(filePath);
  if (bytes.includes(0)) continue;
  const text = bytes.toString("utf8");
  for (const rule of forbidden) {
    const match = rule.pattern.exec(text);
    if (!match) continue;
    const { line, column } = lineAndColumn(text, match.index);
    failures.push(
      `FORBIDDEN  ${relativePath}:${line}:${column} ${rule.name}: ${JSON.stringify(match[0])}`,
    );
  }
}

for (const rule of required) {
  const absolutePath = path.join(repoRoot, rule.file);
  let text;
  try {
    text = await fs.readFile(absolutePath, "utf8");
  } catch (err) {
    if (err && err.code === "ENOENT") {
      failures.push(
        `REQUIRED   ${rule.file}: anchor file missing (cannot verify "${rule.name}")`,
      );
      continue;
    }
    throw err;
  }
  if (!rule.pattern.test(text)) {
    failures.push(`REQUIRED   ${rule.file}: missing "${rule.name}" (pattern ${rule.pattern})`);
  }
}

if (failures.length > 0) {
  console.error("AskUserQuestion migration guard failed:");
  for (const failure of failures) console.error(`- ${failure}`);
  console.error("");
  console.error(
    "Each FORBIDDEN entry above is a retired surface that came back; each REQUIRED entry",
  );
  console.error(
    "is a piece of the cutover that's missing. See scripts/check-askuserquestion-migration.mjs",
  );
  console.error("for the rationale per rule, and docs/migration-policy.md for the policy.");
  process.exit(1);
}

console.log("AskUserQuestion migration guard passed.");

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
