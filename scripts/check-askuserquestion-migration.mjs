#!/usr/bin/env node

// Migration guard for the AskUserQuestion "the answer is a new durable turn"
// cutover.
//
// OLD model (deleted): AskUserQuestion was resolved IN-TURN. The runner parked
// the Claude SDK `canUseTool` decision (or the codex app-server
// `requestUserInput` request), the browser POSTed `/input-reply`, the backend
// published a durable `input_reply` command, and the runner resolved the tool
// with `{behavior:"allow", updatedInput:{answers}}`. The exchange produced
// `tool.approval_requested` / `tool.approval_resolved` events and a
// `needs_input_announcement` transcript row.
//
// NEW model: invoking AskUserQuestion ENDS the asking turn with a durable
// `turn.awaiting_input` terminal carrying the Tank-canonical questions. The
// user's answer is a BRAND-NEW turn (POST `/turns/{turn_id}/answer`). There is
// no `input_reply` command, no in-turn tool result, and no `tool.approval_*`
// events; the transcript promotes an interactive `awaiting_input` card.
//
// This guard forbids the deleted in-turn surfaces and requires the new
// turn-boundary surfaces so neither model can drift back. Fail-on-match is the
// contract — there are no warnings. Run from CI alongside the other guards.

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

const ignoredFiles = new Set(["package-lock.json", "pnpm-lock.yaml", "yarn.lock", "go.sum"]);

// Files allowed to reference the forbidden names: this guard (it must list
// them), sibling guards, and the migration-policy test. Docs that narrate the
// retired model by name are also allowed — they explain WHY it was removed, not
// that it is supported. Keep this list minimal; every entry is a hole.
const ignoredRelativePaths = new Set([
  "scripts/check-askuserquestion-migration.mjs",
  "scripts/check-removed-chat-runtime.mjs",
  "scripts/check-stop-request-migration.mjs",
  "frontend/src/migrationPolicy.test.ts",
  "docs/tank-conversation-protocol.md",
  "docs/session-list-redesign.md",
]);

// Forbidden: must NOT appear in any non-excluded file. Each retired surface
// carries a one-line reason — the deletion target, not just THAT it is one.
const forbidden = [
  // --- input_reply command path (the answer is now a normal new turn) --------
  { name: "removed input_reply command constant CommandInputReply", pattern: /\bCommandInputReply\b/ },
  { name: "removed input_reply command type string", pattern: /["']input_reply["']/ },
  { name: "removed /input-reply HTTP route", pattern: /\/input-reply\b/ },
  { name: "removed handleInputReplySessionTurn backend handler", pattern: /\bhandleInputReplySessionTurn\b/ },
  { name: "removed acceptInputReply runner handler", pattern: /\bacceptInputReply\b/ },
  { name: "removed frontend sendInputReply", pattern: /\bsendInputReply\b/ },
  { name: "removed pendingInputReplies resolver map", pattern: /\bpendingInputReplies\b/ },
  { name: "removed resolvedInputReplies map", pattern: /\bresolvedInputReplies\b/ },
  { name: "removed markInputReplyCompleted", pattern: /\bmarkInputReplyCompleted\b/ },
  { name: "removed joinAnswersForSDK in-turn answer injection", pattern: /\bjoinAnswersForSDK\b/ },
  { name: "removed answersForCodexAppServer in-turn answer injection", pattern: /\banswersForCodexAppServer\b/ },
  { name: "removed buildInputReplyMessage synthetic tool_result helper", pattern: /\bbuildInputReplyMessage\b/ },

  // --- tool.approval_* events (were EXCLUSIVELY AskUserQuestion; deleted) -----
  { name: "removed tool.approval_requested event type", pattern: /tool\.approval_requested/ },
  { name: "removed tool.approval_resolved event type", pattern: /tool\.approval_resolved/ },
  { name: "removed EventApprovalRequested/Resolved Go consts", pattern: /\bEventApproval(Requested|Resolved)\b/ },

  // --- in-turn permission gating for AskUserQuestion -------------------------
  { name: "removed updatedInput.answers in-turn answer injection", pattern: /updatedInput[^;\n]*\banswers\b/ },
  { name: "removed permissionMode bypassPermissions", pattern: /permissionMode\s*:\s*["']bypassPermissions["']/ },

  // --- the needs_input_announcement promotion (replaced by awaiting_input) ----
  { name: "removed needs_input_announcement metaKind/row", pattern: /needs_input_announcement/ },
  { name: "removed RunNeedsInputAnnouncement component", pattern: /\bRunNeedsInputAnnouncement\b/ },
  { name: "removed projectNeedsInputAnnouncement projection", pattern: /\bprojectNeedsInputAnnouncement\b/ },
  { name: "removed isProjectionNeedsInputAnnouncement predicate", pattern: /\bisProjectionNeedsInputAnnouncement\b/ },
  { name: "removed needsInputAnnouncement module", pattern: /needsInputAnnouncementState|needsInputAnnouncement"|\.\/needsInputAnnouncement/ },
  { name: "removed frontend optimisticSubmit (in-turn answer optimism)", pattern: /\boptimisticSubmit\b/ },

  // --- retired in-turn AskUserQuestion metric --------------------------------
  {
    name: "removed askUserQuestion pending gauge / wait histogram",
    pattern: /askUserQuestionPendingGauge|tank_runner_askuser_question_(pending|wait_seconds)/,
  },
];

// Required: each entry names an anchor file and a pattern that MUST appear in
// it. A missing anchor means the cutover regressed.
const required = [
  // --- contract -------------------------------------------------------------
  {
    file: "schemas/tank-conversation-event.schema.json",
    name: "turn.awaiting_input event type",
    pattern: /"turn\.awaiting_input"/,
  },
  {
    file: "schemas/tank-conversation-event.schema.json",
    name: "ask_user_answer user-message display kind",
    pattern: /ask_user_answer/,
  },
  {
    file: "runner-shared/conversation.js",
    name: "turn.awaiting_input in TANK_EVENT_TYPES",
    pattern: /"turn\.awaiting_input"/,
  },
  {
    file: "backend-go/internal/conversation/types.go",
    name: "EventTurnAwaitingInput const",
    pattern: /EventTurnAwaitingInput\s+EventType\s*=\s*"turn\.awaiting_input"/,
  },
  {
    file: "backend-go/internal/conversation/types.go",
    name: "turn.awaiting_input registered as a turn terminal",
    pattern: /func IsTurnTerminalEvent[\s\S]{0,400}EventTurnAwaitingInput/,
  },

  // --- backend answer endpoint ----------------------------------------------
  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "handleAnswerSessionTurn handler",
    pattern: /func \(s \*appServer\) handleAnswerSessionTurn\(/,
  },
  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "answer endpoint requires the asking turn's turn.awaiting_input terminal",
    pattern: /EventTurnAwaitingInput/,
  },
  {
    file: "backend-go/cmd/tank-operator/server.go",
    name: "POST /turns/{turn_id}/answer route",
    pattern: /\/turns\/\{turn_id\}\/answer/,
  },

  // --- Claude runner --------------------------------------------------------
  {
    file: "agent-runner/src/runner.ts",
    name: "endTurnAwaitingInput handoff",
    pattern: /\bendTurnAwaitingInput\b/,
  },
  {
    file: "agent-runner/src/runner.ts",
    name: "AskUserQuestion canUseTool ends the turn via deny+interrupt",
    pattern: /AskUserQuestion[\s\S]{0,700}interrupt:\s*true/,
  },
  {
    file: "agent-runner/src/adapters/claude.ts",
    name: "Claude adapter normalizes questions via claudeQuestionsToTankShape",
    pattern: /\bclaudeQuestionsToTankShape\b/,
  },

  // --- Codex runner ---------------------------------------------------------
  {
    file: "codex-runner/src/runner.ts",
    name: "codex requestAppServerUserInput ends the turn awaiting input",
    pattern: /requestAppServerUserInput[\s\S]{0,1200}endTurnAwaitingInput/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "codex publishes turn.awaiting_input",
    pattern: /turn\.awaiting_input/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "codex normalizes questions via codexQuestionsToTankShape",
    pattern: /\bcodexQuestionsToTankShape\b/,
  },

  // --- backend read path: the promoted question card ------------------------
  {
    file: "backend-go/cmd/tank-operator/transcript_projection.go",
    name: "projection emits the awaiting_input card",
    pattern: /projectAwaitingInputCard|"awaiting_input"/,
  },

  // --- frontend interactive card --------------------------------------------
  {
    file: "frontend/src/App.tsx",
    name: "RunAwaitingInputCard renders the question card",
    pattern: /\bRunAwaitingInputCard\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "submitAnswer posts the answer to /answer",
    pattern: /\/answer`/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "card surfaces the allowFreeForm-driven free-form textarea",
    pattern: /\bshowFreeForm\b/,
  },

  // --- protocol doc ---------------------------------------------------------
  {
    file: "docs/tank-conversation-protocol.md",
    name: "protocol documents the turn.awaiting_input handoff",
    pattern: /turn\.awaiting_input/,
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
      failures.push(`REQUIRED   ${rule.file}: anchor file missing (cannot verify "${rule.name}")`);
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
    "Each FORBIDDEN entry is a retired in-turn surface that came back; each REQUIRED",
  );
  console.error(
    "entry is a piece of the turn.awaiting_input cutover that's missing. See",
  );
  console.error(
    "scripts/check-askuserquestion-migration.mjs for the rationale, and",
  );
  console.error("docs/migration-policy.md for the policy.");
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
  return { line: lines.length, column: lines[lines.length - 1].length + 1 };
}
