import type { Config } from "../config.js";
import type { CodexEvent } from "../cosmos.js";
import { itemEvent, turnEvent, type TankConversationEvent } from "../conversation.js";

export interface CodexAdapterTurn {
  turnID: string;
  clientNonce: string;
  turnSeq: number;
}

function codexProviderEventID(event: CodexEvent): string | undefined {
  const item = event.item;
  if (item && typeof item === "object") {
    const itemID = (item as { id?: unknown }).id;
    if (typeof itemID === "string" && itemID) return itemID;
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
    if (event.type !== "item.started" && event.type !== "item.updated" && event.type !== "item.completed") {
      return [];
    }
    const item = event.item;
    if (!item || typeof item !== "object") return [];
    const itemRecord = item as Record<string, unknown>;
    const itemID =
      typeof itemRecord.id === "string" && itemRecord.id ? itemRecord.id : `${turn.turnID}:item:${providerID ?? event.type}`;
    const itemFailed = itemRecord.error !== undefined;
    const actor = itemRecord.type === "agent_message" || itemRecord.type === "reasoning" ? "assistant" : "tool";
    const type =
      event.type === "item.started"
        ? "item.started"
        : event.type === "item.updated"
          ? "item.delta"
          : itemFailed
            ? "item.failed"
            : "item.completed";
    const payload = this.codexItemPayload(itemID, itemRecord, {
      delta: event.type === "item.updated",
      fallbackText: event.type === "item.completed" ? this.itemTextByID.get(itemID) : undefined,
    });
    if (event.type === "item.started") this.rememberItemText(itemID, codexItemText(itemRecord));
    if (event.type === "item.completed") this.itemTextByID.delete(itemID);
    return [
      itemEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        source: "codex",
        type,
        itemID,
        actor,
        providerEventID: providerID,
        visibility: event.type === "item.updated" ? "live-only" : "durable",
        payload,
      }),
    ];
  }

  private codexItemPayload(
    itemID: string,
    item: Record<string, unknown>,
    opts: { delta?: boolean; fallbackText?: string } = {},
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
      ...(opts.delta ? { delta: this.deltaForItemText(itemID, text) } : { text }),
      command: item.command,
      arguments: item.arguments,
      result: item.result,
      error: item.error,
      raw_item: item,
    };
  }

  private rememberItemText(itemID: string, text: string | undefined): void {
    if (text !== undefined) this.itemTextByID.set(itemID, text);
  }

  private deltaForItemText(itemID: string, text: string | undefined): string | undefined {
    if (text === undefined) return undefined;
    const previous = this.itemTextByID.get(itemID) ?? "";
    this.itemTextByID.set(itemID, text);
    return previous && text.startsWith(previous) ? text.slice(previous.length) : text;
  }
}

export function canonicalEventsForCodexEvent(
  cfg: Config,
  turn: CodexAdapterTurn,
  event: CodexEvent,
): TankConversationEvent[] {
  return new CodexTankEventAdapter(cfg).canonicalEventsForCodexEvent(turn, event);
}
