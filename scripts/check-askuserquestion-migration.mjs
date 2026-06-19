#!/usr/bin/env node

// Migration guard for the AskUserQuestion handoff + continuation-turn cutover.
//
// OLD models (deleted): browser-local /input-reply routes, synthetic
// tool_result answers, AskUserQuestion-specific user-message display kinds, or
// a provider callback that permanently owns the Tank-visible turn boundary.
//
// NEW model: invoking AskUserQuestion routes to Tank's SDK MCP tool
// (`mcp__tank__AskUserQuestion`), which publishes a runner invocation event
// plus a distinct derived `assistant_message.created` event on the asking turn.
// That assistant message is the terminal transcript response and links to a
// normal numbered question turn whose durable `turn.awaiting_input` owns the
// question card/pages. Answering records `turn.input_answered` for the question
// turn, writes a normal `user_message.created` + `turn.submitted` continuation
// turn for the visible answer, and delivers the answer to the paused MCP tool
// over the control plane as `input_reply`. Claude SDK permissions remain in
// bypass mode; AskUserQuestion must not be implemented through canUseTool.
//
// This guard forbids retired AskUserQuestion surfaces and requires the durable
// handoff/continuation pieces so neither model can drift back. Fail-on-match is
// the contract; there are no warnings.

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);

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

const ignoredRelativePaths = new Set([
  "scripts/check-askuserquestion-migration.mjs",
  "scripts/check-removed-chat-runtime.mjs",
  "scripts/check-stop-request-migration.mjs",
  "frontend/src/migrationPolicy.test.ts",
  "docs/session-list-redesign.md",
]);

const forbidden = [
  { name: "removed /input-reply HTTP route", pattern: /\/input-reply\b/ },
  {
    name: "removed handleInputReplySessionTurn backend handler",
    pattern: /\bhandleInputReplySessionTurn\b/,
  },
  { name: "removed frontend sendInputReply", pattern: /\bsendInputReply\b/ },
  {
    name: "removed resolvedInputReplies map",
    pattern: /\bresolvedInputReplies\b/,
  },
  {
    name: "removed markInputReplyCompleted",
    pattern: /\bmarkInputReplyCompleted\b/,
  },
  {
    name: "removed buildInputReplyMessage synthetic tool_result helper",
    pattern: /\bbuildInputReplyMessage\b/,
  },

  {
    name: "removed tool.approval_requested event type",
    pattern: /tool\.approval_requested/,
  },
  {
    name: "removed tool.approval_resolved event type",
    pattern: /tool\.approval_resolved/,
  },
  {
    name: "removed EventApprovalRequested/Resolved Go consts",
    pattern: /\bEventApproval(Requested|Resolved)\b/,
  },

  {
    name: "removed needs_input_announcement metaKind/row",
    pattern: /needs_input_announcement/,
  },
  {
    name: "removed projectNeedsInputAnnouncement projection",
    pattern: /\bprojectNeedsInputAnnouncement\b/,
  },
  {
    name: "removed isProjectionNeedsInputAnnouncement predicate",
    pattern: /\bisProjectionNeedsInputAnnouncement\b/,
  },

  {
    name: "removed ask_user_answer user-message display kind",
    pattern: /ask_user_answer/,
  },
  {
    name: "removed askUserAnswers transcript decoration",
    pattern: /\baskUserAnswers\b/,
  },
  {
    name: "removed AskUserAnswerDisplay builder",
    pattern: /\bAskUserAnswerDisplay\b/,
  },
  {
    name: "removed endTurnAwaitingInput runner method",
    pattern: /\bendTurnAwaitingInput\b/,
  },
  {
    name: "removed RunNeedsInputAnnouncement transcript row",
    pattern: /\bRunNeedsInputAnnouncement\b/,
  },
  {
    name: "removed needs-input announcement CSS",
    pattern: /run-needs-input-announcement/,
  },
  {
    name: "removed turn.awaiting_input terminal registration",
    pattern: /func IsTurnTerminalEvent[\s\S]{0,500}EventTurnAwaitingInput/,
  },
  {
    name: "removed AskUserQuestion deny+interrupt handoff",
    pattern: /AskUserQuestion[\s\S]{0,900}deny[\s\S]{0,300}interrupt:\s*true/,
  },
  {
    name: "removed Claude canUseTool AskUserQuestion handoff",
    pattern: /canUseTool[\s\S]{0,1000}AskUserQuestion|AskUserQuestion[\s\S]{0,1000}canUseTool/,
  },
  {
    name: "removed AskUserQuestion permission-callback parking",
    pattern: /permission callback[\s\S]{0,500}(pending|parked)|pending[\s\S]{0,500}permission callback/,
  },

  // The question widget renders inline in the main transcript; the
  // navigate-to-Turns question shortcuts are retired. The general TurnViewButton
  // (per-message "Open turn in Turns") is a separate, still-live affordance.
  {
    name: "removed run-msg-question-action assistant-message shortcut",
    pattern: /run-msg-question-action/,
  },
  {
    name: "removed AskUserQuestionToolTargetButton tool-row shortcut",
    pattern: /\bAskUserQuestionToolTargetButton\b/,
  },
  {
    name: "removed askUserQuestionTargetHref helper",
    pattern: /\baskUserQuestionTargetHref\b/,
  },
  {
    name: "removed run-tool-question-target shortcut",
    pattern: /run-tool-question-target/,
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
    name: "assistant_message.created event type",
    pattern: /"assistant_message\.created"/,
  },
  {
    file: "schemas/tank-conversation-event.schema.json",
    name: "turn.awaiting_input.invocation event type",
    pattern: /"turn\.awaiting_input\.invocation"/,
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
    name: "answer endpoint writes a normal continuation user turn",
    pattern: /UserSubmissionEventMaps[\s\S]{0,700}ClientNonce:\s+clientNonce/,
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
    file: "claude-runner/src/runner.ts",
    name: "Claude runner records AskUserQuestion handoff",
    pattern: /\bpauseTurnForInput\b/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner routes AskUserQuestion through Tank MCP alias",
    pattern: /TANK_MCP_SERVER_NAME\s*=\s*"tank"[\s\S]{0,400}TANK_ASK_USER_QUESTION_TOOL\s*=\s*"AskUserQuestion"[\s\S]{0,400}TANK_ASK_USER_QUESTION_TOOL_ALIAS[\s\S]{0,200}mcp__/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude SDK options install AskUserQuestion alias",
    pattern: /toolAliases:\s*\{[\s\S]{0,200}TANK_ASK_USER_QUESTION_TOOL[\s\S]{0,200}TANK_ASK_USER_QUESTION_TOOL_ALIAS/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner uses SDK bypass mode after removing permission interception",
    pattern: /permissionMode:\s*"bypassPermissions"[\s\S]{0,120}allowDangerouslySkipPermissions:\s*true/,
  },
  {
    // #1078 reshaped acceptInputReply into a thin gate over
    // deliverInputReply (exact-key match, restart fallback, parking);
    // the answer-conversion contract now lives in deliverInputReply.
    file: "claude-runner/src/runner.ts",
    name: "Claude runner accepts input_reply",
    pattern: /\bacceptInputReply\b[\s\S]{0,600}deliverInputReply/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner delivers input_reply answers to the paused callback",
    pattern: /\bdeliverInputReply\b[\s\S]{0,4000}answersForClaudeInput/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner rotates resumed provider output to continuation turn",
    pattern: /\brotateTurnForInputReply\b[\s\S]{0,700}turnIDForClientNonce/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner delivers answer annotations to provider",
    pattern: /answersForClaudeInput\(record\.answers,\s*record\.annotations\)/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner treats Other as a synthetic free-form label",
    pattern: /label\.toLowerCase\(\)\s*!==\s*"other"/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude runner counts input_reply answer shape",
    pattern: /inputReplyAnswerShapeTotal[\s\S]{0,120}\.labels[\s\S]{0,120}inputReplyAnswerShape/,
  },
  {
    file: "claude-runner/src/adapters/claude.ts",
    name: "Claude adapter normalizes questions via claudeQuestionsToTankShape",
    pattern: /\bclaudeQuestionsToTankShape\b/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude AskUserQuestion schema accepts array and single-question shorthand",
    pattern: /questions:\s*z[\s\S]{0,900}\.array\([\s\S]{0,900}\.min\(1\)[\s\S]{0,120}\.optional\(\)[\s\S]{0,400}top-level question fields[\s\S]{0,500}question:\s*z[\s\S]{0,120}\.string\(\)[\s\S]{0,120}\.optional\(\)/,
  },
  {
    file: "claude-runner/src/adapters/claude.ts",
    name: "Claude adapter wraps top-level question shorthand",
    pattern: /Array\.isArray\(rawQuestions\)[\s\S]{0,200}typeof inputRecord\?\.question === "string"[\s\S]{0,120}\[inputRecord\]/,
  },
  {
    file: "claude-runner/src/runner.ts",
    name: "Claude AskUserQuestion rejects empty normalized question sets before pausing",
    pattern: /questions\.length\s*===\s*0[\s\S]{0,300}top-level question string/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex requestAppServerUserInput records a question handoff",
    pattern: /requestAppServerUserInput[\s\S]{0,1200}pauseTurnForInput/,
  },
  {
    // #1078 reshaped acceptInputReply into a thin gate over
    // deliverInputReply (exact-key match, restart fallback, parking);
    // the answer-conversion contract now lives in deliverInputReply.
    file: "codex-runner/src/runner.ts",
    name: "Codex runner accepts input_reply",
    pattern: /\bacceptInputReply\b[\s\S]{0,600}deliverInputReply/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner delivers input_reply answers to the paused request",
    pattern: /\bdeliverInputReply\b[\s\S]{0,4000}answersForCodexInput/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner rotates resumed provider output to continuation turn",
    pattern: /\brotateTurnForInputReply\b[\s\S]{0,700}turnIDForClientNonce/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner delivers answer annotations to provider",
    pattern: /answersForCodexInput\(record\.answers,\s*record\.annotations\)/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner treats Other as a synthetic free-form label",
    pattern: /label\.toLowerCase\(\)\s*!==\s*"other"/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex runner counts input_reply answer shape",
    pattern: /inputReplyAnswerShapeTotal[\s\S]{0,120}\.labels[\s\S]{0,120}inputReplyAnswerShape/,
  },
  {
    file: "codex-runner/src/runner.ts",
    name: "Codex normalizes questions via codexQuestionsToTankShape",
    pattern: /\bcodexQuestionsToTankShape\b/,
  },

  {
    file: "backend-go/cmd/tank-operator/transcript_projection.go",
    name: "projection renders derived assistant question messages",
    pattern: /applyAssistantMessage[\s\S]{0,1200}awaitingInput/,
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
    name: "pending question is resolved as an inline entry",
    pattern: /const inlineQuestionEntry/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "inline question widget renders in the main transcript",
    pattern: /run-turn-activity-question/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "needs_input activity group is emitted inline (not condensed to a pointer)",
    pattern: /if \(needsInput\) \{[\s\S]{0,400}groups\.push\(group\)/,
  },
  {
    file: "backend-go/cmd/tank-operator/turn_pages.go",
    name: "asking turn's triggering prompt is projected onto the question turn",
    pattern: /projectQuestionPromptContextEntry/,
  },
  {
    file: "backend-go/cmd/tank-operator/turn_pages.go",
    name: "continued prompt context marked turnContextContinued",
    pattern: /turnContextContinued/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "Q2+ pages label the carried prompt as continued",
    pattern: /Question prompt continued from previous turn/,
  },
  {
    file: "frontend/src/App.tsx",
    name: "question page uses the semantic question page kind",
    pattern: /kind === "question"/,
  },
  {
    file: "backend-go/cmd/tank-operator/turn_pages.go",
    name: "turn.awaiting_input creates semantic question pages",
    pattern: /awaitingInputQuestionPages[\s\S]{0,700}Kind:\s*"question"/,
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
    name: "protocol documents AskUserQuestion continuation turns",
    pattern: /turn\.input_answered[\s\S]*continuation turn[\s\S]*input_reply/,
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
    failures.push(
      `REQUIRED   ${rule.file}: missing "${rule.name}" (pattern ${rule.pattern})`,
    );
  }
}

if (failures.length > 0) {
  console.error("AskUserQuestion migration guard failed:");
  for (const failure of failures) console.error(`- ${failure}`);
  console.error("");
  console.error(
    "Each FORBIDDEN entry is a retired AskUserQuestion surface that came back;",
  );
  console.error(
    "each REQUIRED entry is a piece of the handoff/continuation-turn contract.",
  );
  console.error(
    "See scripts/check-askuserquestion-migration.mjs and docs/migration-policy.md.",
  );
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
