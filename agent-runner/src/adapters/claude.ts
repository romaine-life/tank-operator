import { createHash } from "node:crypto";

import type { Config } from "../config.js";
import type { TankConversationEvent } from "../../../runner-shared/conversation.js";
import { itemEvent, turnEvent } from "../../../runner-shared/conversation-builders.js";

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
  needsInputProviderItemIDs: Set<string>,
): TankConversationEvent[] {
  if (!turn) return [];
  const providerID = providerEventID(message);
  if (message.type === "assistant") {
    const events: TankConversationEvent[] = [];
    for (const [index, block] of claudeMessageContent(message).entries()) {
      if (!block || typeof block !== "object") continue;
      const item = block as Record<string, unknown>;
      if (item.type === "text") {
        const text = typeof item.text === "string" ? item.text : "";
        if (!text) continue;
        events.push(
          itemEvent({
            sessionID: cfg.sessionId,
            turnID: turn.turnID,
            source: "claude",
            type: "item.completed",
            providerItemID: claudeBlockProviderItemID({
              turnID: turn.turnID,
              actorPart: "assistant",
              providerID,
              blockType: "text",
              index,
              block: item,
            }),
            actor: "assistant",
            providerEventID: providerID,
            payload: { kind: "message", text },
          }),
        );
      } else if (item.type === "tool_use") {
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
        if (name === "AskUserQuestion") {
          needsInputProviderItemIDs.add(providerItemID);
          events.push(
            itemEvent({
              sessionID: cfg.sessionId,
              turnID: turn.turnID,
              source: "claude",
              type: "tool.approval_requested",
              providerItemID,
              actor: "tool",
              providerEventID: providerID,
              payload: {
                kind: "needs_input",
                title: "Ask user question",
                name,
                input: item.input,
              },
            }),
          );
        }
      }
    }
    return events;
  }
  if (message.type === "user") {
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
      const completed = itemEvent({
        sessionID: cfg.sessionId,
        turnID: turn.turnID,
        source: "claude",
        type: failed ? "item.failed" : "item.completed",
        providerItemID,
        actor: "tool",
        providerEventID: providerID,
        payload: {
          kind: "tool_result",
          output: item.content,
          is_error: failed,
        },
      });
      if (!needsInputProviderItemIDs.has(providerItemID)) return [completed];
      needsInputProviderItemIDs.delete(providerItemID);
      return [
        completed,
        itemEvent({
          sessionID: cfg.sessionId,
          turnID: turn.turnID,
          source: "claude",
          type: "tool.approval_resolved",
          providerItemID,
          actor: "tool",
          providerEventID: providerID,
          payload: {
            kind: "needs_input",
            resolved: true,
            is_error: failed,
          },
        }),
      ];
    });
  }
  if (message.type === "result") {
    if (turn.interrupted && turn.terminalEmitted) return [];
    const failed = message.is_error === true || message.subtype === "error";
    return [
      turnEvent({
        sessionID: cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: turn.interrupted ? "turn.interrupted" : failed ? "turn.failed" : "turn.completed",
        reason: turn.interrupted ? "client_interrupt" : failed ? "provider_failure" : undefined,
        usage: message.usage,
        error: failed ? message.result ?? message.error : undefined,
        providerEventID: providerID,
      }),
    ];
  }
  return [];
}

function providerEventID(message: ClaudeProviderEvent): string | undefined {
  for (const key of ["uuid", "id", "message_id", "session_id"]) {
    const value = message[key];
    if (typeof value === "string" && value) return value;
  }
  return undefined;
}

function claudeMessageContent(message: ClaudeProviderEvent): unknown[] {
  const body = message.message;
  if (body && typeof body === "object" && "content" in body) {
    const content = (body as { content?: unknown }).content;
    return Array.isArray(content) ? content : [];
  }
  return [];
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
