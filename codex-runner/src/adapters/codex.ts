import type { Config } from "../config.js";
import type { CodexEvent } from "../sessionEvents.js";
import type { TankConversationEvent } from "../../../runner-shared/conversation.js";
import { itemEvent, shellTaskEvent, turnEvent } from "../../../runner-shared/conversation-builders.js";
import { itemOutcomeTotal } from "../metrics.js";

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
    if (event.type === "turn.interrupted") {
      this.itemTextByID.clear();
      return [
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "codex",
          type: "turn.interrupted",
          reason: "client_interrupt",
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
        if (isCodexUserMessageEchoItem(itemRecord)) return [];
        const providerItemID =
          typeof itemRecord.id === "string" && itemRecord.id
            ? itemRecord.id
            : `${turn.turnID}:item:${providerID ?? event.type}`;
        if (isCodexBackgroundShellItem(itemRecord)) {
          return this.codexBackgroundShellEvents(turn, event, itemRecord, providerItemID);
        }
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
    if (isCodexUserMessageEchoItem(itemRecord)) return [];
    const providerItemID =
      typeof itemRecord.id === "string" && itemRecord.id ? itemRecord.id : `${turn.turnID}:item:${providerID ?? event.type}`;
    if (isCodexBackgroundShellItem(itemRecord)) {
      return this.codexBackgroundShellEvents(turn, event, itemRecord, providerItemID);
    }
    const outcome = codexItemOutcome(itemRecord);
    const actor = itemRecord.type === "agent_message" || itemRecord.type === "reasoning" ? "assistant" : "tool";
    const type =
      event.type === "item.started"
        ? "item.started"
        : outcome.kind === "execution_failed"
          ? "item.failed"
          : "item.completed";
    const payload = this.codexItemPayload(providerItemID, itemRecord, {
      fallbackText: event.type === "item.completed" ? this.itemTextByID.get(providerItemID) : undefined,
      outcome: event.type === "item.completed" ? outcome : undefined,
    });
    if (event.type === "item.completed") itemOutcomeTotal.labels(outcome.kind, outcome.reason ?? "none").inc();
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
    opts: { fallbackText?: string; outcome?: ItemOutcome } = {},
  ): Record<string, unknown> {
    const text = codexItemText(item) ?? opts.fallbackText;
    const exitCode = itemExitCode(item);
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
      exit_code: exitCode,
      status: item.status,
      outcome: opts.outcome,
      raw_item: item,
    };
  }

  private rememberItemText(providerItemID: string, text: string | undefined): void {
    if (text !== undefined) this.itemTextByID.set(providerItemID, text);
  }

  private codexBackgroundShellEvents(
    turn: CodexAdapterTurn,
    event: CodexEvent,
    item: Record<string, unknown>,
    providerItemID: string,
  ): TankConversationEvent[] {
    const taskID = codexBackgroundTaskID(item, providerItemID);
    const status = codexBackgroundTaskStatus(event.type, item);
    const type =
      event.type === "item.started"
        ? "shell_task.started"
        : isTerminalShellTaskStatus(status)
          ? "shell_task.exited"
          : "shell_task.updated";
    return [
      shellTaskEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        source: "codex",
        type,
        taskID,
        status,
        providerItemID,
        payload: {
          status,
          provider_item_id: providerItemID,
          source: item.source,
          command: item.command,
          cwd: item.cwd,
          process_id: item.process_id ?? item.processId,
          output: codexItemText(item),
          exit_code: itemExitCode(item),
          duration_ms: item.duration_ms ?? item.durationMs,
          raw_item: item,
        },
      }),
    ];
  }
}

type ItemOutcome =
  | { kind: "ok"; reason?: undefined; code?: undefined }
  | { kind: "result_failed"; reason: "exit_code" | "codex_item_status_failed"; code?: number }
  | { kind: "execution_failed"; reason: "provider_item_error"; code?: undefined };

function codexItemOutcome(item: Record<string, unknown>): ItemOutcome {
  if (hasExecutionError(item.error)) {
    return { kind: "execution_failed", reason: "provider_item_error" };
  }
  const exitCode = itemExitCode(item);
  if (exitCode !== undefined && exitCode !== 0) {
    return { kind: "result_failed", reason: "exit_code", code: exitCode };
  }
  if (item.status === "failed") {
    return { kind: "result_failed", reason: "codex_item_status_failed" };
  }
  return { kind: "ok" };
}

function itemExitCode(item: Record<string, unknown>): number | undefined {
  const direct = numericExitCode(item.exit_code) ?? numericExitCode(item.exitCode);
  if (direct !== undefined) return direct;
  const result = item.result;
  if (result && typeof result === "object" && !Array.isArray(result)) {
    const resultRecord = result as Record<string, unknown>;
    return numericExitCode(resultRecord.exit_code) ?? numericExitCode(resultRecord.exitCode);
  }
  return undefined;
}

function hasExecutionError(error: unknown): boolean {
  if (error === undefined || error === null) return false;
  if (typeof error === "string") return error.trim().length > 0;
  return true;
}

function numericExitCode(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isInteger(value)) return value;
  if (typeof value === "string" && /^-?\d+$/.test(value)) return Number(value);
  return undefined;
}

function isCodexUserMessageEchoItem(item: Record<string, unknown>): boolean {
  // Tank owns the durable user-message boundary via user_message.created.
  // Codex app-server may also echo the submitted user input as a provider
  // item; forwarding that item would make the frontend render it as a tool.
  return item.type === "userMessage" || item.type === "user_message";
}

function isCodexBackgroundShellItem(item: Record<string, unknown>): boolean {
  if (item.type !== "command_execution") return false;
  const source = String(item.source ?? "").toLowerCase();
  return source === "unifiedexecstartup" || source === "unifiedexecinteraction";
}

function codexBackgroundTaskID(item: Record<string, unknown>, providerItemID: string): string {
  for (const key of ["process_id", "processId", "id"]) {
    const value = item[key];
    if (typeof value === "string" && value) return value;
  }
  return providerItemID;
}

function codexBackgroundTaskStatus(eventType: string, item: Record<string, unknown>): string {
  const status = typeof item.status === "string" && item.status ? item.status : "";
  if (status) return status;
  return eventType === "item.started" ? "running" : "updated";
}

function isTerminalShellTaskStatus(status: string): boolean {
  return ["completed", "failed", "stopped", "cancelled", "canceled", "exited", "declined"].includes(
    status.toLowerCase(),
  );
}

export function canonicalEventsForCodexEvent(
  cfg: Config,
  turn: CodexAdapterTurn,
  event: CodexEvent,
): TankConversationEvent[] {
  return new CodexTankEventAdapter(cfg).canonicalEventsForCodexEvent(turn, event);
}
