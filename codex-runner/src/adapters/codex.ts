import type { Config } from "../config.js";
import type { CodexEvent } from "../sessionEvents.js";
import type { TankConversationEvent } from "../../../runner-shared/conversation.js";
import { itemEvent, turnEvent } from "../../../runner-shared/conversation-builders.js";

export interface CodexAdapterTurn {
  turnID: string;
  clientNonce: string;
  turnSeq: number;
}

function codexProviderEventID(event: CodexEvent): string | undefined {
  const item = event.item;
  if (item && typeof item === "object") {
    const providerItemID = (item as { id?: unknown }).id;
    if (typeof providerItemID === "string" && providerItemID) return providerItemID;
  }
  for (const key of ["turn_id", "thread_id", "id", "uuid"]) {
    const value = event[key];
    if (typeof value === "string" && value) return value;
  }
  return undefined;
}

function codexItemText(item: Record<string, unknown>): string | undefined {
  if (typeof item.text === "string") return item.text;
  if (typeof item.aggregated_output === "string") return item.aggregated_output;
  if (typeof item.message === "string") return item.message;
  return undefined;
}

export class CodexTankEventAdapter {
  private readonly itemTextByID = new Map<string, string>();

  constructor(private readonly cfg: Config) {}

  canonicalEventsForCodexEvent(
    turn: CodexAdapterTurn,
    event: CodexEvent,
  ): TankConversationEvent[] {
    const providerID = codexProviderEventID(event);
    if (event.type === "turn.completed") {
      this.itemTextByID.clear();
      return [
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "codex",
          type: "turn.completed",
          usage: event.usage,
          providerEventID: providerID,
        }),
      ];
    }
    if (event.type === "turn.failed" || event.type === "error") {
      this.itemTextByID.clear();
      return [
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "codex",
          type: "turn.failed",
          reason: "provider_failure",
          error: event.error ?? event.message ?? event,
          providerEventID: providerID,
        }),
      ];
    }
    // Codex `item.updated` provider events were the per-token typewriter
    // stream. They were always emitted as Tank `item.delta` with
    // visibility=live-only, then dropped at the runner sink — no consumer
    // ever subscribed. The visibility distinction has been retired
    // alongside the producer; if/when a future live channel for partial
    // tokens lands, restore both the `item.delta` Tank event type and the
    // `live-only` visibility together.
    //
    // We still observe the frames here (no Tank event emitted) so the
    // running text is accumulated — Codex sometimes finalizes with
    // `item.completed` carrying no text, expecting the consumer to have
    // captured it via the prior `item.updated` frames.
    if (event.type === "item.updated") {
      const item = event.item;
      if (item && typeof item === "object") {
        const itemRecord = item as Record<string, unknown>;
        const providerItemID =
          typeof itemRecord.id === "string" && itemRecord.id
            ? itemRecord.id
            : `${turn.turnID}:item:${providerID ?? event.type}`;
        this.rememberItemText(providerItemID, codexItemText(itemRecord));
      }
      return [];
    }
    if (event.type !== "item.started" && event.type !== "item.completed") {
      return [];
    }
    const item = event.item;
    if (!item || typeof item !== "object") return [];
    const itemRecord = item as Record<string, unknown>;
    const providerItemID =
      typeof itemRecord.id === "string" && itemRecord.id ? itemRecord.id : `${turn.turnID}:item:${providerID ?? event.type}`;
    const itemFailed = itemRecord.error !== undefined;
    const actor = itemRecord.type === "agent_message" || itemRecord.type === "reasoning" ? "assistant" : "tool";
    const type =
      event.type === "item.started"
        ? "item.started"
        : itemFailed
          ? "item.failed"
          : "item.completed";
    const payload = this.codexItemPayload(providerItemID, itemRecord, {
      fallbackText: event.type === "item.completed" ? this.itemTextByID.get(providerItemID) : undefined,
    });
    if (event.type === "item.started") this.rememberItemText(providerItemID, codexItemText(itemRecord));
    if (event.type === "item.completed") this.itemTextByID.delete(providerItemID);
    return [
      itemEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        source: "codex",
        type,
        providerItemID,
        actor,
        providerEventID: providerID,
        payload,
      }),
    ];
  }

  private codexItemPayload(
    _providerItemID: string,
    item: Record<string, unknown>,
    opts: { fallbackText?: string } = {},
  ): Record<string, unknown> {
    const text = codexItemText(item) ?? opts.fallbackText;
    return {
      kind: typeof item.type === "string" && item.type ? item.type : "item",
      title:
        typeof item.command === "string"
          ? item.command
          : typeof item.tool === "string"
            ? item.tool
            : typeof item.type === "string"
              ? item.type
              : "item",
      text,
      command: item.command,
      arguments: item.arguments,
      result: item.result,
      error: item.error,
      raw_item: item,
    };
  }

  private rememberItemText(providerItemID: string, text: string | undefined): void {
    if (text !== undefined) this.itemTextByID.set(providerItemID, text);
  }
}

export function canonicalEventsForCodexEvent(
  cfg: Config,
  turn: CodexAdapterTurn,
  event: CodexEvent,
): TankConversationEvent[] {
  return new CodexTankEventAdapter(cfg).canonicalEventsForCodexEvent(turn, event);
}
