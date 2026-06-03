import { createHash } from "node:crypto";

import type { Config } from "../config.js";
import type { TankConversationEvent, TankFinalAnswer } from "../../../runner-shared/conversation.js";
import { contextCompactedEvent, itemEvent, shellTaskEvent, turnEvent } from "../../../runner-shared/conversation-builders.js";
import { itemOutcomeTotal, turnUsageEmittedTotal } from "../metrics.js";

// ClaudeProviderEvent is the runner's view of the raw Claude SDK message
// shape consumed by this adapter. Kept loose because the adapter has to
// inspect provider-specific fields that the SDK's narrow union types hide.
export interface ClaudeProviderEvent {
  type: string;
  subtype?: string;
  uuid?: string;
  [k: string]: unknown;
}

export interface ClaudeTurnContext {
  turnID: string;
  clientNonce: string;
  interrupted: boolean;
  terminalEmitted: boolean;
  finalAnswer?: TankFinalAnswer;
}

export function claudeUserMessageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (
          part &&
          typeof part === "object" &&
          "type" in part &&
          (part as { type?: unknown }).type === "text" &&
          "text" in part
        ) {
          return String((part as { text?: unknown }).text ?? "");
        }
        return "";
      })
      .filter(Boolean)
      .join("\n");
  }
  return String(content ?? "");
}

export function startsClaudeTurn(event: ClaudeProviderEvent): boolean {
  return event.type === "assistant" || event.type === "user" || event.type === "result";
}

export function canonicalEventsForClaudeMessage(
  cfg: Config,
  turn: ClaudeTurnContext | null,
  message: ClaudeProviderEvent,
): TankConversationEvent[] {
  if (!turn) return [];
  const providerID = providerEventID(message);
  // Context compaction is a durable, user-visible event, not a silent
  // provider-internal action. Mapping it here is the close of the architectural
  // gap that hid it: the SDK's system/compact_boundary used to fall through to
  // `return []` (logged-but-dropped) like any unrecognized message. See
  // docs/tank-conversation-protocol.md → "Context Compaction Notice".
  if (message.type === "system" && message.subtype === "compact_boundary") {
    return [
      contextCompactedEvent({
        sessionID: cfg.sessionId,
        turnID: turn.turnID,
        source: "claude",
        trigger: claudeCompactTrigger(message),
        preTokens: claudeCompactPreTokens(message),
        providerEventID: providerID,
      }),
    ];
  }
  if (message.type === "system" && isClaudeTaskLifecycleMessage(message)) {
    return canonicalEventsForClaudeTaskLifecycle(cfg, turn, message, providerID);
  }
  if (message.type === "assistant") {
    const events: TankConversationEvent[] = [];
    const finalAnswerTimelineIDs: string[] = [];
    const finalAnswerProviderItemIDs: string[] = [];
    let hasToolUse = false;
    for (const [index, block] of claudeMessageContent(message).entries()) {
      if (!block || typeof block !== "object") continue;
      const item = block as Record<string, unknown>;
      if (item.type === "text") {
        const text = typeof item.text === "string" ? item.text : "";
        if (!text) continue;
        const providerItemID = claudeBlockProviderItemID({
          turnID: turn.turnID,
          actorPart: "assistant",
          providerID,
          blockType: "text",
          index,
          block: item,
        });
        const event = itemEvent({
          sessionID: cfg.sessionId,
          turnID: turn.turnID,
          source: "claude",
          type: "item.completed",
          providerItemID,
          actor: "assistant",
          providerEventID: providerID,
          payload: { kind: "message", text },
        });
        events.push(event);
        if (event.timeline_id) {
          finalAnswerTimelineIDs.push(event.timeline_id);
          finalAnswerProviderItemIDs.push(providerItemID);
        }
      } else if (item.type === "tool_use") {
        hasToolUse = true;
        const providerItemID =
          typeof item.id === "string" && item.id
            ? item.id
            : claudeBlockProviderItemID({
                turnID: turn.turnID,
                actorPart: "tool",
                providerID,
                blockType: "tool_use",
                index,
                block: item,
              });
        const name = typeof item.name === "string" ? item.name : "tool";
        // AskUserQuestion produces no item events. The runner's canUseTool
        // ends the asking turn with a durable turn.awaiting_input handoff
        // carrying the Tank-canonical questions (claudeQuestionsToTankShape);
        // the transcript renders the question card from that terminal, so
        // there is no dangling "started" tool item on a settled turn.
        // (Previously this emitted item.started + tool.approval_requested.)
        if (name !== "AskUserQuestion") {
          events.push(
            itemEvent({
              sessionID: cfg.sessionId,
              turnID: turn.turnID,
              source: "claude",
              type: "item.started",
              providerItemID,
              actor: "tool",
              providerEventID: providerID,
              payload: {
                kind: "tool",
                title: name,
                name,
                input: item.input,
              },
            }),
          );
        }
      }
    }
    if (hasToolUse) {
      turn.finalAnswer = undefined;
    } else if (finalAnswerTimelineIDs.length > 0) {
      turn.finalAnswer = {
        timelineIDs: finalAnswerTimelineIDs,
        providerItemIDs: finalAnswerProviderItemIDs,
      };
    }
    // Emit a per-message context-occupancy snapshot. Claude reports usage
    // only on the cumulative terminal (result.usage), whose input_tokens is
    // the tiny uncached sliver once prompt caching folds the prompt into
    // cache_read/cache_creation. Each assistant message's own usage is the
    // size of THAT model call's prompt (input + cache_read + cache_creation),
    // i.e. the live context-window occupancy at that step. Forwarding it as a
    // durable turn.usage — tagged claude.message so the reader distinguishes
    // it from the cumulative claude.result terminal — mirrors the codex
    // runner's thread.tokenUsage.updated stream and satisfies the transcript
    // contract's "mid-turn token usage updates are durable turn activity".
    const messageUsage = claudeMessageUsage(message);
    if (messageUsage) {
      events.push(
        turnEvent({
          sessionID: cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "claude",
          type: "turn.usage",
          usage: messageUsage,
          usageObservation: { usage_source: "claude.message", terminal_had_usage: false },
          providerEventID: providerID,
        }),
      );
      turnUsageEmittedTotal.labels("snapshot").inc();
    }
    return events;
  }
  if (message.type === "user") {
    turn.finalAnswer = undefined;
    return claudeMessageContent(message).flatMap((block, index): TankConversationEvent[] => {
      if (!block || typeof block !== "object") return [];
      const item = block as Record<string, unknown>;
      if (item.type !== "tool_result") return [];
      const providerItemID =
        typeof item.tool_use_id === "string" && item.tool_use_id
          ? item.tool_use_id
          : claudeBlockProviderItemID({
              turnID: turn.turnID,
              actorPart: "tool",
              providerID,
              blockType: "tool_result",
              index,
              block: item,
            });
      const failed = item.is_error === true;
      const outcome = failed
        ? { kind: "result_failed", reason: "claude_tool_result_is_error" }
        : { kind: "ok" };
      itemOutcomeTotal.labels(outcome.kind, failed ? "claude_tool_result_is_error" : "none").inc();
      const completed = itemEvent({
        sessionID: cfg.sessionId,
        turnID: turn.turnID,
        source: "claude",
        type: "item.completed",
        providerItemID,
        actor: "tool",
        providerEventID: providerID,
        payload: {
          kind: "tool_result",
          output: item.content,
          is_error: failed,
          outcome,
        },
      });
      return [completed];
    });
  }
  if (message.type === "result") {
    // If the runner already published a terminal for this turn — a Stop
    // interrupt or an AskUserQuestion turn.awaiting_input handoff — swallow
    // the SDK's (interrupted) result so we don't emit a second terminal.
    if (turn.terminalEmitted) return [];
    const failed = message.is_error === true || message.subtype === "error";
    const completed = !turn.interrupted && !failed;
    // result.usage is CUMULATIVE across the whole turn (it sums cache reads
    // over every tool-loop iteration), so it is the correct basis for cost
    // and total-token accounting but NOT for context-window occupancy.
    // Tag it claude.result so the reader uses it for cost and ignores it for
    // occupancy (which comes from the per-message claude.message snapshots).
    const hasUsage = message.usage !== undefined && message.usage !== null;
    if (hasUsage) turnUsageEmittedTotal.labels("terminal").inc();
    return [
      turnEvent({
        sessionID: cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: turn.interrupted ? "turn.interrupted" : failed ? "turn.failed" : "turn.completed",
        reason: turn.interrupted ? "client_interrupt" : failed ? "provider_failure" : undefined,
        usage: message.usage,
        usageObservation: hasUsage
          ? { usage_source: "claude.result", terminal_had_usage: true }
          : undefined,
        error: failed ? message.result ?? message.error : undefined,
        finalAnswer: completed ? turn.finalAnswer : undefined,
        providerEventID: providerID,
      }),
    ];
  }
  return [];
}

export function claudeTaskIdentifiers(
  message: ClaudeProviderEvent,
): { taskID: string | null; toolUseID: string | null } {
  return {
    taskID: nonEmptyString(message.task_id),
    toolUseID: nonEmptyString(message.tool_use_id),
  };
}

export function isClaudeTaskLifecycleMessage(message: ClaudeProviderEvent): boolean {
  return (
    message.type === "system" &&
    (
      message.subtype === "task_started" ||
      message.subtype === "task_progress" ||
      message.subtype === "task_notification" ||
      message.subtype === "task_updated"
    )
  );
}

// claudeCompactTrigger / claudeCompactPreTokens read the Claude Agent SDK's
// `compact_metadata` off a system/compact_boundary message: whether compaction
// was auto-triggered (context filled) or manual (/compact), and the
// pre-compaction token count. Unknown/missing shapes default to "auto" with no
// token count rather than throwing, so malformed metadata still yields a
// renderable notice instead of a turn-failing crash.
function claudeCompactTrigger(message: ClaudeProviderEvent): "auto" | "manual" {
  const meta = message.compact_metadata;
  if (meta && typeof meta === "object" && (meta as Record<string, unknown>).trigger === "manual") {
    return "manual";
  }
  return "auto";
}

function claudeCompactPreTokens(message: ClaudeProviderEvent): number | undefined {
  const meta = message.compact_metadata;
  if (!meta || typeof meta !== "object") return undefined;
  const value = (meta as Record<string, unknown>).pre_tokens;
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;
}

function canonicalEventsForClaudeTaskLifecycle(
  cfg: Config,
  turn: ClaudeTurnContext,
  message: ClaudeProviderEvent,
  providerID: string | undefined,
): TankConversationEvent[] {
  const taskID = nonEmptyString(message.task_id);
  if (!taskID) return [];
  const status = shellTaskStatus(message);
  const type =
    message.subtype === "task_started"
      ? "shell_task.started"
      : isTerminalShellTaskStatus(status)
        ? "shell_task.exited"
        : "shell_task.updated";
  const toolUseID = nonEmptyString(message.tool_use_id);
  const payload: Record<string, unknown> = {
    status,
    provider_subtype: message.subtype,
  };
  for (const key of ["summary", "description", "last_tool_name", "error"] as const) {
    if (message[key] !== undefined) payload[key] = message[key];
  }
  if (toolUseID) payload.tool_use_id = toolUseID;
  return [
    shellTaskEvent({
      sessionID: cfg.sessionId,
      turnID: turn.turnID,
      source: "claude",
      type,
      taskID,
      status,
      providerItemID: toolUseID ?? taskID,
      providerEventID: providerID,
      payload,
    }),
  ];
}

function shellTaskStatus(message: ClaudeProviderEvent): string {
  const status = nonEmptyString(message.status);
  if (status) return status;
  return message.subtype === "task_started" ? "running" : "updated";
}

function isTerminalShellTaskStatus(status: string): boolean {
  return ["completed", "failed", "stopped", "cancelled", "canceled", "exited"].includes(
    status.toLowerCase(),
  );
}

function providerEventID(message: ClaudeProviderEvent): string | undefined {
  for (const key of ["uuid", "id", "message_id", "session_id"]) {
    const value = message[key];
    if (typeof value === "string" && value) return value;
  }
  return undefined;
}

function nonEmptyString(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value : null;
}

// claudeQuestionsToTankShape normalizes the Claude SDK's AskUserQuestion
// `input.questions[]` payload into the Tank-canonical question shape the
// frontend renders. Field names are stable across both runner adapters:
// the codex runner emits the same shape from a different provider input.
//
// allowFreeForm=true on every Claude question mirrors Claude Code's native
// host UI, which always offers a free-form "Other" reply. The host UI is
// the contract; Tank renders that contract.
//
// secret=false because the Claude SDK has no secret-input flag on
// AskUserQuestion. If that ever changes, route it through here so the
// codex/claude shapes stay aligned at the adapter boundary.
//
// multiSelect, options[], header, question, and option label/description/
// preview pass through with type-narrowing — no field is dropped silently,
// and any unknown shape becomes an empty options[] rather than throwing,
// so a malformed provider payload still produces a renderable Tank event
// instead of a turn-failing crash.
export function claudeQuestionsToTankShape(input: unknown): TankAskUserQuestion[] {
  const questions = (input as { questions?: unknown })?.questions;
  if (!Array.isArray(questions)) return [];
  return questions.flatMap((q): TankAskUserQuestion[] => {
    if (!q || typeof q !== "object") return [];
    const record = q as Record<string, unknown>;
    const question = typeof record.question === "string" ? record.question : "";
    if (!question) return [];
    const options = Array.isArray(record.options)
      ? record.options.flatMap((opt): TankAskUserQuestionOption[] => {
          if (!opt || typeof opt !== "object") return [];
          const optRecord = opt as Record<string, unknown>;
          const label = typeof optRecord.label === "string" ? optRecord.label : "";
          if (!label) return [];
          return [
            {
              label,
              ...(typeof optRecord.description === "string" && optRecord.description
                ? { description: optRecord.description }
                : {}),
              ...(typeof optRecord.preview === "string" && optRecord.preview
                ? { preview: optRecord.preview }
                : {}),
            },
          ];
        })
      : [];
    return [
      {
        question,
        ...(typeof record.header === "string" && record.header ? { header: record.header } : {}),
        multiSelect: record.multiSelect === true,
        options,
        allowFreeForm: true,
        secret: false,
      },
    ];
  });
}

export interface TankAskUserQuestionOption {
  label: string;
  description?: string;
  preview?: string;
}

export interface TankAskUserQuestion {
  question: string;
  header?: string;
  multiSelect: boolean;
  options: TankAskUserQuestionOption[];
  // True when the question allows a free-form / "say something else" reply
  // instead of (or in addition to) selecting one of the listed options.
  // Claude SDK: always true (Claude Code's host UI ships an Other path).
  // Codex SDK: mirrors codex's isOther flag.
  allowFreeForm: boolean;
  // True when the answer should be masked in the UI (codex `isSecret`).
  // No-op for Claude today; reserved for future SDK growth.
  secret: boolean;
}

function claudeMessageContent(message: ClaudeProviderEvent): unknown[] {
  const body = message.message;
  if (body && typeof body === "object" && "content" in body) {
    const content = (body as { content?: unknown }).content;
    return Array.isArray(content) ? content : [];
  }
  return [];
}

// claudeMessageUsage returns the per-call usage carried by a Claude
// `assistant` SDK message (the Anthropic API message object's `usage`),
// or null when absent/malformed. This is the single model call's usage —
// distinct from the cumulative `result.usage` on the terminal — so its
// input + cache_read + cache_creation is the prompt size of that call,
// i.e. the context-window occupancy at that step.
function claudeMessageUsage(message: ClaudeProviderEvent): Record<string, unknown> | null {
  const body = message.message;
  if (body && typeof body === "object" && "usage" in body) {
    const usage = (body as { usage?: unknown }).usage;
    if (usage && typeof usage === "object" && !Array.isArray(usage)) {
      return usage as Record<string, unknown>;
    }
  }
  return null;
}

function claudeBlockProviderItemID(args: {
  turnID: string;
  actorPart: "assistant" | "tool";
  providerID: string | undefined;
  blockType: string;
  index: number;
  block: unknown;
}): string {
  const messagePart = args.providerID ?? `message_${stableBlockDigest(args.block)}`;
  return `${args.actorPart}:${messagePart}:${args.blockType}:${args.index}`;
}

function stableBlockDigest(block: unknown): string {
  let serialized: string;
  try {
    serialized = JSON.stringify(block) ?? "";
  } catch {
    serialized = String(block);
  }
  return createHash("sha256").update(serialized).digest("hex").slice(0, 12);
}
