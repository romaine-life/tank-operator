// Cosmos sink for canonical codex SDK events. The runner is the producer and
// the durable session-events container is the transcript source of truth.
//
// Difference from claude: the codex SDK's `ThreadEvent` union has no
// per-event `uuid` field — only contained `ThreadItem`s carry ids, and
// only some events have items. We stamp our own sortable id as the doc id
// at production time. The producer is the only writer, so the id is unique
// by construction. The id is lexicographically sortable and
// every dispatched event also carries a Tank order key so replay and SSE
// delivery can be reconciled without depending on render timing.
//
// "Canonical" = events the SPA's history-replay path should see:
//   thread.started     — session boot marker (analog of claude system/init)
//   tank.user_message  - user prompt submitted through Tank's turn queue
//   turn.completed     — turn-end marker with usage
//   turn.failed        — turn-level error
//   turn.interrupted   — Tank interrupt/cancel marker
//   item.completed     — main durable signal: agent_message, reasoning,
//                        command_execution, file_change, mcp_tool_call,
//                        web_search, todo_list, error items
//   error              — thread-level error (e.g. unrecoverable SDK error)
//
// "Live-only" = transient deltas the SPA may use for typewriter UX but
// have no durable value:
//   turn.started, item.started, item.updated
//
// SDK type ref:
//   https://github.com/openai/codex/blob/main/sdk/typescript/src/events.ts

import { CosmosClient } from "@azure/cosmos";
import { DefaultAzureCredential } from "@azure/identity";
import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";
import { isDurableTankConversationEvent } from "./conversation.js";

const CANONICAL_TYPES = new Set<string>([
  "thread.started",
  "tank.user_message",
  "turn.completed",
  "turn.failed",
  "turn.interrupted",
  "item.completed",
  "error",
]);

export interface CodexEvent {
  type: string;
  [k: string]: unknown;
}

export function isCanonical(event: CodexEvent): boolean {
  if (isDurableTankConversationEvent(event)) return true;
  return CANONICAL_TYPES.has(event.type);
}

let lastEventMs = 0;
let eventSeq = 0;
let tankEventSeq = 0;

export function nextSortableEventID(now = Date.now()): string {
  const ms = Math.max(now, lastEventMs);
  if (ms === lastEventMs) {
    eventSeq += 1;
  } else {
    lastEventMs = ms;
    eventSeq = 0;
  }
  return [
    String(ms).padStart(13, "0"),
    String(eventSeq).padStart(6, "0"),
    randomUUID(),
  ].join("-");
}

function nextTankEventSeq(): number {
  tankEventSeq += 1;
  return tankEventSeq;
}

// Stamp a generated, lexicographically sortable id on the event. The runner
// uses the same stamped value when dispatching to both Cosmos and the WS, so
// consumers can dedupe across the history+live join. This is intentionally
// not UUIDv4: replay sorts by id, so random ids reorder multi-turn history.
export function stampEventID(
  event: CodexEvent,
): CodexEvent & {
  uuid: string;
  written_at: string;
} {
  const now = Date.now();
  const eventID = typeof event.event_id === "string" && event.event_id ? event.event_id : "";
  const uuid = typeof event.uuid === "string" && event.uuid ? event.uuid : eventID || nextSortableEventID(now);
  const seq = nextTankEventSeq();
  const writtenAt = new Date(now).toISOString();
  const tankOrderKey = [
    String(now).padStart(13, "0"),
    String(seq).padStart(8, "0"),
    uuid,
  ].join("-");
  return {
    ...event,
    uuid,
    ...(eventID ? { event_id: eventID } : {}),
    written_at: writtenAt,
    ...(hasTankEventEnvelope(event)
      ? {
          order_key: typeof event.order_key === "string" && event.order_key ? event.order_key : tankOrderKey,
          sequence: typeof event.sequence === "number" ? event.sequence : seq,
          created_at:
            typeof event.created_at === "string" && event.created_at ? event.created_at : writtenAt,
        }
      : {}),
  };
}

function hasTankEventEnvelope(event: CodexEvent): boolean {
  return typeof event.event_id === "string" && typeof event.visibility === "string";
}

export class CosmosSink {
  private readonly client: CosmosClient;
  private readonly container;

  constructor(private readonly cfg: Config) {
    this.client = new CosmosClient({
      endpoint: cfg.cosmosEndpoint,
      aadCredentials: new DefaultAzureCredential(),
    });
    this.container = this.client
      .database(cfg.cosmosDatabase)
      .container(cfg.sessionEventsContainer);
  }

  // Write a canonical event. Doc id is the runner-stamped uuid; partition
  // is Tank's scoped session storage key. Matches the shape agent-runner
  // uses, so the orchestrator's read endpoint and the SPA's chat pane
  // consume both claude- and codex-runner events out of the same container.
  async upsert(event: CodexEvent & { uuid: string }): Promise<void> {
    const doc = this.docFromEvent(event);
    await this.container.items.upsert(doc);
  }

  async create(event: CodexEvent & { uuid: string }): Promise<"created" | "exists"> {
    const doc = this.docFromEvent(event);
    try {
      await this.container.items.create(doc);
      return "created";
    } catch (err) {
      if (isConflict(err)) return "exists";
      throw err;
    }
  }

  async findTurnTerminal(turnID: string): Promise<CodexEvent | null> {
    const iterator = this.container.items.query<CodexEvent>(
      {
        query:
          "SELECT TOP 1 * FROM c WHERE c.tank_session_id = @session_id AND c.turn_id = @turn_id AND (c.type = @completed OR c.type = @failed OR c.type = @interrupted)",
        parameters: [
          { name: "@session_id", value: this.cfg.sessionStorageKey },
          { name: "@turn_id", value: turnID },
          { name: "@completed", value: "turn.completed" },
          { name: "@failed", value: "turn.failed" },
          { name: "@interrupted", value: "turn.interrupted" },
        ],
      },
      { partitionKey: this.cfg.sessionStorageKey },
    );
    const page = await iterator.fetchNext();
    return page.resources[0] ?? null;
  }

  private docFromEvent(event: CodexEvent & { uuid: string }): Record<string, unknown> {
    return {
      ...event,
      id: event.uuid,
      tank_session_id: this.cfg.sessionStorageKey,
      tank_public_session_id: this.cfg.sessionId,
      email: this.cfg.ownerEmail,
      runtime: "codex",
      written_at:
        typeof event.written_at === "string"
          ? event.written_at
          : new Date().toISOString(),
    };
  }
}

function isConflict(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const statusCode = (err as { statusCode?: unknown; code?: unknown }).statusCode;
  const code = (err as { statusCode?: unknown; code?: unknown }).code;
  return statusCode === 409 || code === 409 || code === "Conflict";
}
