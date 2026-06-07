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
//   source: USER_EXPLICIT (the user prompt) | SYSTEM (history/context) | MODEL
//   type (source=MODEL): PLANNER_RESPONSE (the model's reasoning step — carries
//     either `tool_calls` to invoke tools or `content` for prose/the final
//     answer) and per-tool result types (CODE_ACTION, VIEW_FILE, RUN_COMMAND, …)
//   status: IN_PROGRESS while the step is live, DONE when settled
//
// Mapping (docs/tank-conversation-protocol.md):
//   USER_EXPLICIT / SYSTEM            -> dropped (Tank owns user_message.created)
//   first MODEL step                  -> turn.started
//   PLANNER_RESPONSE + tool_calls     -> item.started (actor=tool) per call
//   tool result step (CODE_ACTION,…)  -> item.completed for the matching tool
//   PLANNER_RESPONSE + content        -> assistant item.completed; last one is
//                                        the turn's final answer
//   (turn end)                        -> turn.completed{final_answer, usage}
//
// The adapter is pure (no I/O, no metrics): the runner loop owns tailing,
// publishing, metrics, and interrupt/usage. That keeps the mapping unit-
// testable against a captured transcript fixture.

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

export function toolCallProviderItemID(stepIndex: number, index: number): string {
  return `tool-${stepIndex}-${index}`;
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

  constructor(private readonly sessionID: string) {}

  /** Map one agy step to zero or more Tank events. */
  stepEvents(turn: AntigravityTurn, step: AgyStep): TankConversationEvent[] {
    if (!this.markSeen(turn.turnID, step.step_index)) return [];

    const source = upper(step.source);
    // The user prompt is owned by Tank's durable user_message.created; agy's
    // echo of it (and its injected conversation history) is not a transcript
    // event.
    if (source === "USER_EXPLICIT" || source === "SYSTEM") return [];
    if (source !== "MODEL") return [];

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

    const type = upper(step.type);
    const toolCalls = Array.isArray(step.tool_calls) ? step.tool_calls : [];

    if (type === "PLANNER_RESPONSE" && toolCalls.length > 0) {
      events.push(...this.openToolCalls(turn, step, toolCalls));
      return events;
    }

    if (type === "PLANNER_RESPONSE") {
      // A planner step with prose and no tools: an assistant message. The
      // last such step in a turn is the settled final answer.
      events.push(this.assistantMessage(turn, step));
      return events;
    }

    // Any other MODEL step (CODE_ACTION, VIEW_FILE, RUN_COMMAND, …) is the
    // result of a tool the planner invoked. Complete the matching open tool
    // item; if none is open (defensive — agy normally pairs them), surface a
    // self-contained completed tool item rather than silently dropping it.
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
    const pending = this.pendingTools.get(turn.turnID) ?? [];
    toolCalls.forEach((call, index) => {
      const toolName =
        typeof call.name === "string" && call.name.trim()
          ? call.name.trim()
          : "tool";
      const providerItemID = toolCallProviderItemID(step.step_index, index);
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
    this.pendingTools.set(turn.turnID, pending);
    return events;
  }

  private closeToolResult(
    turn: AntigravityTurn,
    step: AgyStep,
  ): TankConversationEvent[] {
    const pending = this.pendingTools.get(turn.turnID) ?? [];
    const text = stepText(step);
    const open = pending.shift();
    if (open) {
      this.pendingTools.set(turn.turnID, pending);
      return [
        itemEvent({
          sessionID: this.sessionID,
          turnID: turn.turnID,
          providerItemID: open.providerItemID,
          actor: "tool",
          source: ANTIGRAVITY_SOURCE,
          type: "item.completed",
          providerEventID: `step-${step.step_index}`,
          payload: {
            kind: "tool",
            tool_name: open.toolName,
            title: open.title,
            ...(text ? { text } : {}),
            outcome: { kind: "ok" },
          },
        }) as TankConversationEvent,
      ];
    }
    // Orphan result (no open tool): emit a self-contained completed tool item.
    const providerItemID = `tool-${step.step_index}`;
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
        type: "item.completed",
        providerEventID: `step-${step.step_index}-orphan-done`,
        payload: {
          kind: "tool",
          tool_name: toolName,
          title: toolName,
          ...(text ? { text } : {}),
          outcome: { kind: "ok" },
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
    this.finalAnswer.set(turn.turnID, {
      timelineIDs: [event.timeline_id as string],
      providerItemIDs: [providerItemID],
    });
    return event;
  }

  private markSeen(turnID: string, stepIndex: number): boolean {
    const seen = this.seenSteps.get(turnID) ?? new Set<number>();
    if (seen.has(stepIndex)) return false;
    seen.add(stepIndex);
    this.seenSteps.set(turnID, seen);
    return true;
  }

  private clearTurn(turnID: string): void {
    this.seenSteps.delete(turnID);
    this.turnStarted.delete(turnID);
    this.pendingTools.delete(turnID);
    this.finalAnswer.delete(turnID);
  }
}
