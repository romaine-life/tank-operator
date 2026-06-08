// Antigravity transcript adapter: maps agy's structured agentic step stream
// (the `transcript_full.jsonl` agy writes under
// brain/<conversation>/.system_generated/logs/) onto the Tank conversation
// protocol. agy has no streaming SDK; instead it appends ordered, status-
// bearing JSON step records to a durable file as a turn executes. The runner
// tails that file and feeds each new line here. This module is the single
// place that understands agy's wire vocabulary — nothing downstream of it sees
// a raw agy step (docs/product-inspirations.md: "Provider-specific event
// streams are adapter inputs. The frontend renders the Tank conversation
// protocol, not raw provider wire formats").
//
// Step vocabulary (confirmed live against agy 1.0.5, Gemini-Ultra):
//   { step_index, type, source, status, content?, tool_calls?, created_at }
//   source: USER_EXPLICIT (the user prompt) | SYSTEM (history/context/tool-call
//     validation errors) | MODEL
//   type (source=MODEL): PLANNER_RESPONSE (the model's reasoning step — carries
//     either `tool_calls` to invoke tools or `content` for prose/the final
//     answer) and per-tool result types (CODE_ACTION, VIEW_FILE, RUN_COMMAND, …)
//   status: IN_PROGRESS while the step is live, DONE when settled
//
// Mapping (docs/tank-conversation-protocol.md):
//   USER_EXPLICIT / SYSTEM history    -> dropped (Tank owns user_message.created)
//   first MODEL step                  -> turn.started
//   PLANNER_RESPONSE + DONE + tool_calls
//                                      -> item.started (actor=tool) per call
//   settled tool result step (CODE_ACTION,…)
//                                      -> item.completed/item.failed for the
//                                         matching tool
//   settled SYSTEM ERROR_MESSAGE       -> item.failed for the matching tool
//   PLANNER_RESPONSE + DONE + content  -> assistant item.completed; last one is
//                                        the turn's final answer
//   (turn end)                        -> turn.completed{final_answer, usage}

import type { TankConversationEvent } from "../../../runner-shared/conversation.js";
import {
  itemEvent,
  turnEvent,
} from "../../../runner-shared/conversation-builders.js";

export const ANTIGRAVITY_SOURCE = "antigravity";

/** One record from agy's transcript_full.jsonl. */
export interface AgyStep {
  step_index: number;
  type?: string;
  source?: string;
  status?: string;
  content?: string | null;
  tool_calls?: AgyToolCall[] | null;
  created_at?: string;
  conversation_id?: string;
  transcript_path?: string;
}

export interface AgyToolCall {
  name?: string;
  args?: Record<string, unknown> | null;
}

/** The Tank turn an agy run is executing. */
export interface AntigravityTurn {
  turnID: string;
  clientNonce: string;
}

interface PendingTool {
  providerItemID: string;
  toolName: string;
  title: string;
}

interface FinalAnswer {
  timelineIDs: string[];
  providerItemIDs: string[];
}

export type AgyAdapterCorrelationKind =
  | "closed_tool_result"
  | "failed_tool_result"
  | "orphan_tool_result"
  | "unclosed_tool_at_terminal";

export interface AgyAdapterObserver {
  recordCorrelation?: (kind: AgyAdapterCorrelationKind, count?: number) => void;
}

// agy folds two UI-hint keys into every tool call's args. They are display
// strings, not model-authored tool input, so the adapter lifts them to the
// item title and drops them from the rendered tool_input.
const TITLE_ARG = "toolSummary";
const ACTION_ARG = "toolAction";

function upper(value: string | undefined): string {
  return (value ?? "").trim().toUpperCase();
}

function stepText(step: AgyStep): string {
  return typeof step.content === "string" ? step.content : "";
}

function toolTitle(call: AgyToolCall, fallback: string): string {
  const args = call.args ?? {};
  const summary = args[TITLE_ARG];
  if (typeof summary === "string" && summary.trim()) return summary.trim();
  const action = args[ACTION_ARG];
  if (typeof action === "string" && action.trim()) return action.trim();
  return fallback;
}

function toolInput(call: AgyToolCall): Record<string, unknown> | undefined {
  const args = call.args;
  if (!args || typeof args !== "object") return undefined;
  const cleaned: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(args)) {
    if (key === TITLE_ARG || key === ACTION_ARG) continue;
    cleaned[key] = value;
  }
  return Object.keys(cleaned).length > 0 ? cleaned : undefined;
}

export function toolCallProviderItemID(
  stepIndex: number,
  index: number,
  conversationID?: string,
): string {
  return conversationID
    ? `conversation-${conversationID}:tool-${stepIndex}-${index}`
    : `tool-${stepIndex}-${index}`;
}

/**
 * Stateful mapping from agy steps to Tank events. One instance per session.
 * Idempotent per (turnID, step_index): re-feeding a step the adapter has
 * already mapped returns `[]`, so the runner can re-read the growing transcript
 * file without emitting duplicates.
 */
export class AntigravityTranscriptAdapter {
  private readonly seenSteps = new Map<string, Set<number>>();
  private readonly turnStarted = new Set<string>();
  private readonly pendingTools = new Map<string, PendingTool[]>();
  private readonly finalAnswer = new Map<string, FinalAnswer>();

  constructor(
    private readonly sessionID: string,
    private readonly observer: AgyAdapterObserver = {},
  ) {}

  /** Map one agy step to zero or more Tank events. */
  stepEvents(turn: AntigravityTurn, step: AgyStep): TankConversationEvent[] {
    const source = upper(step.source);
    const type = upper(step.type);
    // The user prompt is owned by Tank's durable user_message.created; agy's
    // echo of it (and its injected conversation history) is not a transcript
    // event. SYSTEM/ERROR_MESSAGE is different: agy uses it as the terminal
    // result for invalid provider tool calls, so it must close the pending
    // Tank item instead of being discarded as context.
    if (source === "USER_EXPLICIT") return [];
    if (source === "SYSTEM" && type !== "ERROR_MESSAGE") return [];
    if (source !== "MODEL" && !(source === "SYSTEM" && type === "ERROR_MESSAGE"))
      return [];

    const events: TankConversationEvent[] = [];
    if (!this.turnStarted.has(turn.turnID)) {
      this.turnStarted.add(turn.turnID);
      events.push(
        turnEvent({
          sessionID: this.sessionID,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: ANTIGRAVITY_SOURCE,
          type: "turn.started",
          providerEventID: `step-${step.step_index}`,
        }) as TankConversationEvent,
      );
    }

    const toolCalls = Array.isArray(step.tool_calls) ? step.tool_calls : [];

    if (source === "SYSTEM" && type === "ERROR_MESSAGE") {
      if (!isSettled(step)) return events;
      if (!this.markSeen(turn, step)) return events;
      events.push(...this.closeToolResult(turn, step));
      return events;
    }

    if (type === "PLANNER_RESPONSE" && toolCalls.length > 0) {
      if (!isDone(step)) return events;
      if (!this.markSeen(turn, step)) return events;
      events.push(...this.openToolCalls(turn, step, toolCalls));
      return events;
    }

    if (type === "PLANNER_RESPONSE") {
      if (!isCompleteAssistantResponse(step)) return events;
      if (!this.markSeen(turn, step)) return events;
      // A done planner step with prose and no tools: an assistant message.
      // The last such step in a turn is the settled final answer.
      events.push(this.assistantMessage(turn, step));
      return events;
    }

    // Any other MODEL step (CODE_ACTION, VIEW_FILE, RUN_COMMAND, …) is the
    // result of a tool the planner invoked. Complete the matching open tool
    // item; if none is open (defensive — agy normally pairs them), surface a
    // self-contained completed tool item rather than silently dropping it.
    if (!isSettled(step)) return events;
    if (!this.markSeen(turn, step)) return events;
    events.push(...this.closeToolResult(turn, step));
    return events;
  }

  /**
   * Terminal event for a turn that finished cleanly. The final answer is the
   * last assistant prose step; `usage` is the runner's loadCodeAssist
   * observation (Tank's turn.completed carries usage when present).
   */
  completeTurn(
    turn: AntigravityTurn,
    usage?: Record<string, unknown>,
  ): TankConversationEvent {
    this.recordUnclosedTools(turn.turnID);
    const final = this.finalAnswer.get(turn.turnID);
    const event = turnEvent({
      sessionID: this.sessionID,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: ANTIGRAVITY_SOURCE,
      type: "turn.completed",
      ...(usage !== undefined ? { usage } : {}),
      ...(final && final.timelineIDs.length > 0
        ? {
            finalAnswer: {
              timelineIDs: final.timelineIDs,
              providerItemIDs: final.providerItemIDs,
            },
          }
        : {}),
    }) as TankConversationEvent;
    this.clearTurn(turn.turnID);
    return event;
  }

  /** True once this turn has assistant prose that can be promoted as final. */
  hasFinalAnswer(turn: AntigravityTurn): boolean {
    const final = this.finalAnswer.get(turn.turnID);
    return (
      Array.isArray(final?.timelineIDs) &&
      final.timelineIDs.length > 0 &&
      Array.isArray(final.providerItemIDs) &&
      final.providerItemIDs.length > 0
    );
  }

  /** Terminal event for a turn that failed (agy nonzero exit / error). */
  failTurn(turn: AntigravityTurn, reason: string): TankConversationEvent {
    const event = turnEvent({
      sessionID: this.sessionID,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: ANTIGRAVITY_SOURCE,
      type: "turn.failed",
      reason,
    }) as TankConversationEvent;
    this.clearTurn(turn.turnID);
    return event;
  }

  /** Terminal event for a user-initiated interrupt (Stop). */
  interruptTurn(turn: AntigravityTurn): TankConversationEvent {
    const event = turnEvent({
      sessionID: this.sessionID,
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      source: ANTIGRAVITY_SOURCE,
      type: "turn.interrupted",
      reason: "client_interrupt",
    }) as TankConversationEvent;
    this.clearTurn(turn.turnID);
    return event;
  }

  private openToolCalls(
    turn: AntigravityTurn,
    step: AgyStep,
    toolCalls: AgyToolCall[],
  ): TankConversationEvent[] {
    const events: TankConversationEvent[] = [];
    const pendingKey = this.pendingKey(turn, step);
    const conversationID = stepConversationID(step);
    const pending = this.pendingTools.get(pendingKey) ?? [];
    toolCalls.forEach((call, index) => {
      const toolName =
        typeof call.name === "string" && call.name.trim()
          ? call.name.trim()
          : "tool";
      const providerItemID = toolCallProviderItemID(
        step.step_index,
        index,
        conversationID,
      );
      const title = toolTitle(call, toolName);
      const input = toolInput(call);
      pending.push({ providerItemID, toolName, title });
      events.push(
        itemEvent({
          sessionID: this.sessionID,
          turnID: turn.turnID,
          providerItemID,
          actor: "tool",
          source: ANTIGRAVITY_SOURCE,
          type: "item.started",
          providerEventID: `step-${step.step_index}-${index}`,
          payload: {
            kind: "tool",
            tool_name: toolName,
            title,
            ...(input !== undefined ? { tool_input: input } : {}),
          },
        }) as TankConversationEvent,
      );
    });
    this.pendingTools.set(pendingKey, pending);
    return events;
  }

  private closeToolResult(
    turn: AntigravityTurn,
    step: AgyStep,
  ): TankConversationEvent[] {
    const pendingKey = this.pendingKey(turn, step);
    const pending = this.pendingTools.get(pendingKey) ?? [];
    const text = stepText(step);
    const open = pending.shift();
    const failed = isToolResultFailure(step);
    const eventType = failed ? "item.failed" : "item.completed";
    const outcome = failed
      ? { kind: "execution_failed", reason: "provider_item_error" }
      : { kind: "ok" };
    this.observer.recordCorrelation?.(
      open ? (failed ? "failed_tool_result" : "closed_tool_result") : "orphan_tool_result",
    );
    if (open) {
      this.pendingTools.set(pendingKey, pending);
      return [
        itemEvent({
          sessionID: this.sessionID,
          turnID: turn.turnID,
          providerItemID: open.providerItemID,
          actor: "tool",
          source: ANTIGRAVITY_SOURCE,
          type: eventType,
          providerEventID: `step-${step.step_index}`,
          payload: {
            kind: "tool",
            tool_name: open.toolName,
            title: open.title,
            ...(text ? { text } : {}),
            ...(failed ? { error: resultErrorText(step) } : {}),
            outcome,
          },
        }) as TankConversationEvent,
      ];
    }
    // Orphan result (no open tool): emit a self-contained completed tool item.
    const conversationID = stepConversationID(step);
    const providerItemID = conversationID
      ? `conversation-${conversationID}:tool-${step.step_index}`
      : `tool-${step.step_index}`;
    const toolName = (step.type ?? "tool").toLowerCase();
    return [
        itemEvent({
        sessionID: this.sessionID,
        turnID: turn.turnID,
        providerItemID,
        actor: "tool",
        source: ANTIGRAVITY_SOURCE,
        type: "item.started",
        providerEventID: `step-${step.step_index}-orphan-start`,
        payload: { kind: "tool", tool_name: toolName, title: toolName },
      }) as TankConversationEvent,
      itemEvent({
        sessionID: this.sessionID,
        turnID: turn.turnID,
        providerItemID,
        actor: "tool",
        source: ANTIGRAVITY_SOURCE,
          type: eventType,
          providerEventID: `step-${step.step_index}-orphan-done`,
          payload: {
            kind: "tool",
            tool_name: toolName,
            title: toolName,
            ...(text ? { text } : {}),
            ...(failed ? { error: resultErrorText(step) } : {}),
            outcome,
          },
        }) as TankConversationEvent,
    ];
  }

  private assistantMessage(
    turn: AntigravityTurn,
    step: AgyStep,
  ): TankConversationEvent {
    const providerItemID = `msg-${step.step_index}`;
    const text = stepText(step);
    const event = itemEvent({
      sessionID: this.sessionID,
      turnID: turn.turnID,
      providerItemID,
      actor: "assistant",
      source: ANTIGRAVITY_SOURCE,
      type: "item.completed",
      providerEventID: `step-${step.step_index}`,
      payload: {
        kind: "message",
        ...(text ? { text } : {}),
        outcome: { kind: "ok" },
      },
    }) as TankConversationEvent;
    // The most recent assistant prose step is the running final-answer
    // candidate; it is promoted by completeTurn() at the turn terminal.
    if (text.trim()) {
      this.finalAnswer.set(turn.turnID, {
        timelineIDs: [event.timeline_id as string],
        providerItemIDs: [providerItemID],
      });
    }
    return event;
  }

  private markSeen(turn: AntigravityTurn, step: AgyStep): boolean {
    const key = this.pendingKey(turn, step);
    const seen = this.seenSteps.get(key) ?? new Set<number>();
    if (seen.has(step.step_index)) return false;
    seen.add(step.step_index);
    this.seenSteps.set(key, seen);
    return true;
  }

  private clearTurn(turnID: string): void {
    for (const key of [...this.seenSteps.keys()]) {
      if (keyBelongsToTurn(key, turnID)) this.seenSteps.delete(key);
    }
    this.turnStarted.delete(turnID);
    for (const key of [...this.pendingTools.keys()]) {
      if (keyBelongsToTurn(key, turnID)) this.pendingTools.delete(key);
    }
    this.finalAnswer.delete(turnID);
  }

  private pendingKey(turn: AntigravityTurn, step: AgyStep): string {
    return `${turn.turnID}\0${stepConversationID(step) ?? ""}`;
  }

  private recordUnclosedTools(turnID: string): void {
    let count = 0;
    for (const [key, pending] of this.pendingTools.entries()) {
      if (keyBelongsToTurn(key, turnID)) count += pending.length;
    }
    if (count > 0) {
      this.observer.recordCorrelation?.("unclosed_tool_at_terminal", count);
    }
  }
}

function stepConversationID(step: AgyStep): string | undefined {
  const id = step.conversation_id;
  if (typeof id === "string" && id.trim()) return id.trim();
  return undefined;
}

function keyBelongsToTurn(key: string, turnID: string): boolean {
  return key === turnID || key.startsWith(`${turnID}\0`);
}

function isToolResultFailure(step: AgyStep): boolean {
  return (
    upper(step.status) === "ERROR" ||
    upper(step.status) === "TERMINAL_ERROR" ||
    upper(step.type) === "ERROR_MESSAGE"
  );
}

function resultErrorText(step: AgyStep): string {
  const text = stepText(step).trim();
  if (!text) return `${step.type ?? "tool"} failed`;
  return text.length > 1000 ? `${text.slice(0, 1000)}...` : text;
}

function isDone(step: AgyStep): boolean {
  return upper(step.status) === "DONE";
}

function isSettled(step: AgyStep): boolean {
  const status = upper(step.status);
  return status === "DONE" || status === "ERROR" || status === "TERMINAL_ERROR";
}

function isCompleteAssistantResponse(step: AgyStep): boolean {
  return (
    upper(step.source) === "MODEL" &&
    upper(step.type) === "PLANNER_RESPONSE" &&
    isDone(step) &&
    stepText(step).trim().length > 0
  );
}
