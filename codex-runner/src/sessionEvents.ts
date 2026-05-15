// Canonical event sink for the Codex runner. Runners publish durable events
// to NATS JetStream; the backend session-bus persister writes the
// session-events ledger and wakes SSE streams after the write commits.

import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";
import { isDurableTankConversationEvent } from "./conversation.js";
import { SessionBus } from "./sessionBus.js";

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

export class SessionEventSink {
  private readonly bus: SessionBus;

  constructor(cfg: Config) {
    this.bus = new SessionBus(cfg, "codex");
  }

  async upsert(event: CodexEvent & { uuid: string }): Promise<void> {
    await this.bus.publishEvent(event);
  }

  async create(event: CodexEvent & { uuid: string }): Promise<"created" | "exists"> {
    return this.bus.publishEvent(event);
  }

  async findTurnTerminal(turnID: string): Promise<CodexEvent | null> {
    return (await this.bus.findTurnTerminal(turnID)) as CodexEvent | null;
  }
}

