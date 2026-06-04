#!/usr/bin/env node

// Migration guard for the AskUserQuestion "pause the same turn" cutover.
//
// OLD model (deleted): AskUserQuestion ended the asking turn with a durable
// `turn.awaiting_input` terminal. The user's answer opened a brand-new turn
// through POST `/turns/{turn_id}/answer`, represented the answer as a
// `user_message.created` display kind (`ask_user_answer`), and left no
// in-turn reply command for the paused provider callback.
//
// NEW model: invoking AskUserQuestion publishes durable `turn.awaiting_input`
// as a same-turn pause, keeps that turn active, opens a semantic
// Turn-activity question-set page, records the user's answer as
// `turn.input_answered`, and delivers it to the paused runner over the control
// plane as `input_reply`. The main transcript may announce and navigate to the
// question page, but it must not own the interactive answer form.
//
// This guard forbids the deleted new-turn surfaces and requires the same-turn
// pause/resume surfaces so neither model can drift back. Fail-on-match is the
// contract; there are no warnings.

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

const ignoredRelativePaths = new Set([
  "scripts/check-askuserquestion-migration.mjs",
  "scripts/check-removed-chat-runtime.mjs",
  "scripts/check-stop-request-migration.mjs",
  "frontend/src/migrationPolicy.test.ts",
  "docs/session-list-redesign.md",
]);

const forbidden = [
  { name: "removed /input-reply HTTP route", pattern: /\/input-reply\b/ },
  { name: "removed handleInputReplySessionTurn backend handler", pattern: /\bhandleInputReplySessionTurn\b/ },
  { name: "removed frontend sendInputReply", pattern: /\bsendInputReply\b/ },
  { name: "removed resolvedInputReplies map", pattern: /\bresolvedInputReplies\b/ },
  { name: "removed markInputReplyCompleted", pattern: /\bmarkInputReplyCompleted\b/ },
  { name: "removed buildInputReplyMessage synthetic tool_result helper", pattern: /\bbuildInputReplyMessage\b/ },

  { name: "removed tool.approval_requested event type", pattern: /tool\.approval_requested/ },
  { name: "removed tool.approval_resolved event type", pattern: /tool\.approval_resolved/ },
  { name: "removed EventApprovalRequested/Resolved Go consts", pattern: /\bEventApproval(Requested|Resolved)\b/ },

  { name: "removed needs_input_announcement metaKind/row", pattern: /needs_input_announcement/ },
  { name: "removed RunNeedsInputAnnouncement component", pattern: /\bRunNeedsInputAnnouncement\b/ },
  { name: "removed projectNeedsInputAnnouncement projection", pattern: /\bprojectNeedsInputAnnouncement\b/ },
  { name: "removed isProjectionNeedsInputAnnouncement predicate", pattern: /\bisProjectionNeedsInputAnnouncement\b/ },
  { name: "removed needsInputAnnouncement module", pattern: /needsInputAnnouncementState|needsInputAnnouncement"|\.\/needsInputAnnouncement/ },

  { name: "removed ask_user_answer user-message display kind", pattern: /ask_user_answer/ },
  { name: "removed askUserAnswers transcript decoration", pattern: /\baskUserAnswers\b/ },
  { name: "removed AskUserAnswerDisplay builder", pattern: /\bAskUserAnswerDisplay\b/ },
  { name: "removed endTurnAwaitingInput runner method", pattern: /\bendTurnAwaitingInput\b/ },
  {
    name: "removed turn.awaiting_input terminal registration",
    pattern: /func IsTurnTerminalEvent[\s\S]{0,500}EventTurnAwaitingInput/,
  },
  {
    name: "removed AskUserQuestion deny+interrupt handoff",
    pattern: /AskUserQuestion[\s\S]{0,900}deny[\s\S]{0,300}interrupt:\s*true/,
  },
];

const required = [
  {
    file: "schemas/tank-conversation-event.schema.json",
    name: "turn.awaiting_input event type",
    pattern: /"turn\.awaiting_input"/,
  },
  {
    file: "schemas/tank-conversation-event.schema.json",
    name: "turn.input_answered event type",
    pattern: /"turn\.input_answered"/,
  },
  {
    file: "runner-shared/conversation.js",
    name: "turn.awaiting_input in TANK_EVENT_TYPES",
    pattern: /"turn\.awaiting_input"/,
  },
  {
    file: "runner-shared/conversation.js",
    name: "turn.input_answered in TANK_EVENT_TYPES",
    pattern: /"turn\.input_answered"/,
  },
  {
    file: "backend-go/internal/conversation/types.go",
    name: "EventTurnAwaitingInput const",
    pattern: /EventTurnAwaitingInput\s+EventType\s*=\s*"turn\.awaiting_input"/,
  },
  {
    file: "backend-go/internal/conversation/types.go",
    name: "EventTurnInputAnswered const",
    pattern: /EventTurnInputAnswered\s+EventType\s*=\s*"turn\.input_answered"/,
  },

  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "handleAnswerSessionTurn handler",
    pattern: /func \(s \*appServer\) handleAnswerSessionTurn\(/,
  },
  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "answer endpoint requires the asking turn's turn.awaiting_input pause",
    pattern: /EventTurnAwaitingInput/,
  },
  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "answer endpoint persists turn.input_answered",
    pattern: /TurnInputAnsweredEventMap/,
  },
  {
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    name: "answer endpoint publishes input_reply",
    pattern: /CommandInputReply/,
  },
  {
    file: "backend-go/cmd/tank-operator/server.go",
    name: "POST /turns/{turn_id}/answer route",
    pattern: /\/turns\/\{turn_id\}\/answer/,
  },
  {
    file: "backend-go/internal/sessionbus/subjects.go",
    name: "input_reply routes to control plane",
    pattern: /CommandInputReply[\s\S]{0,200}ControlSubject/,
  },

  {
    file: "agent-runner/src/runner.ts",
    name: "Claude runner pauses AskUserQuestion",
    pattern: /\bpauseTurnForInput\b/,
  },
  {
    file: "agent-runner/src/runner.ts",
    name: "Claude runner accepts input_reply",
    pattern: /\bacceptInputReply\b[\s\S]{0,1200}updatedInput[\s\S]{0,400}answers/,
  },
  {
    file: "agent-runner/src/adapters/claude.ts",
    name: "Claude adapter normalizes questions via claudeQuestionsToTankShape",
    pattern: /\bclaudeQuestionsToTankShape\b/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex requestAppServerUserInput pauses the same turn",
    pattern: /requestAppServerUserInput[\s\S]{0,1200}pauseTurnForInput/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner accepts input_reply",
    pattern: /\bacceptInputReply\b[\s\S]{0,900}answersForCodexInput/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex normalizes questions via codexQuestionsToTankShape",
    pattern: /\bcodexQuestionsToTankShape\b/,
  },

  {
    file: "backend-go/cmd/tank-operator/transcript_projection.go",
    name: "projection emits the awaiting_input card",
    pattern: /projectAwaitingInputCard|"awaiting_input"/,
  },
  {
    file: "backend-go/cmd/tank-operator/transcript_projection.go",
    name: "projection marks card answered from turn.input_answered",
    pattern: /applyInputAnswered/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "RunAwaitingInputCard renders the question card",
    pattern: /\bRunAwaitingInputCard\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "RunAwaitingInputNotice is the transcript navigation surface",
    pattern: /\bRunAwaitingInputNotice\b/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "question page uses the semantic question_set page kind",
    pattern: /kind === "question_set"/,
  },
  {
    file: "backend-go/cmd/tank-operator/turn_pages.go",
    name: "turn.awaiting_input creates a semantic question_set page",
    pattern: /currentKind = "question_set"/,
  },
  {
    file: "backend-go/cmd/tank-operator/turn_pages.go",
    name: "needs_input defaults to pending question-set page",
    pattern: /defaultTurnActivityPageNumber/,
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
  {
    file: "docs/tank-conversation-protocol.md",
    name: "protocol documents same-turn AskUserQuestion pause",
    pattern: /turn\.input_answered[\s\S]{0,1200}input_reply/,
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
  console.error("Each FORBIDDEN entry is a retired AskUserQuestion surface that came back;");
  console.error("each REQUIRED entry is a piece of the same-turn pause/resume contract.");
  console.error("See scripts/check-askuserquestion-migration.mjs and docs/migration-policy.md.");
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
