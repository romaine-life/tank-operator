// Canonical event sink for the Claude runner. Runners publish durable events
// to NATS JetStream; the backend session-bus persister writes the
// session-events ledger and wakes SSE streams after the write commits.

import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";
import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";
import { isDurableTankConversationEvent } from "./conversation.js";
import { SessionBus } from "./sessionBus.js";

export interface RunnerEvent {
  type: string;
  uuid?: string;
  event_id?: string;
  subtype?: string;
  [k: string]: unknown;
}

const CANONICAL_TYPES = new Set<string>([
  "system",
  "tank.user_message",
  "user",
  "assistant",
  "result",
]);

const CANONICAL_SYSTEM_SUBTYPES = new Set<string>([
  "init",
  "compact_boundary",
  "tool_use_summary",
  "permission_denied",
  "plugin_install",
]);

const CANONICAL_TOP_LEVEL_TYPES = new Set<string>(["rate_limit"]);

export function isCanonical(message: RunnerEvent | SDKMessage): boolean {
  if (isDurableTankConversationEvent(message as RunnerEvent)) return true;
  const t = (message as RunnerEvent).type;
  if (!t) return false;
  if (CANONICAL_TOP_LEVEL_TYPES.has(t)) return true;
  if (CANONICAL_TYPES.has(t)) {
    if (t === "system") {
      const subtype = (message as RunnerEvent).subtype;
      return subtype ? CANONICAL_SYSTEM_SUBTYPES.has(subtype) : false;
    }
    return true;
  }
  return false;
}

export class SessionEventSink {
  private readonly bus: SessionBus;

  constructor(cfg: Config) {
    this.bus = new SessionBus(cfg, "claude");
  }

  async upsert(message: RunnerEvent & { uuid: string }): Promise<void> {
    await this.bus.publishEvent(message);
  }

  async create(message: RunnerEvent & { uuid: string }): Promise<"created" | "exists"> {
    return this.bus.publishEvent(message);
  }

  async findTurnTerminal(turnID: string): Promise<RunnerEvent | null> {
    return (await this.bus.findTurnTerminal(turnID)) as RunnerEvent | null;
  }
}

let tankEventSeq = 0;

export function stampTankEvent(message: RunnerEvent): RunnerEvent & { uuid: string } {
  tankEventSeq += 1;
  const now = Date.now();
  const eventID = typeof message.event_id === "string" && message.event_id ? message.event_id : "";
  const uuid = typeof message.uuid === "string" && message.uuid ? message.uuid : eventID || randomUUID();
  const writtenAt = new Date(now).toISOString();
  const tankOrderKey = [
    String(now).padStart(13, "0"),
    String(tankEventSeq).padStart(8, "0"),
    uuid,
  ].join("-");
  return {
    ...message,
    uuid,
    ...(eventID ? { event_id: eventID } : {}),
    written_at: writtenAt,
    ...(isTankEvent(message)
      ? {
          order_key:
            typeof message.order_key === "string" && message.order_key ? message.order_key : tankOrderKey,
          sequence: typeof message.sequence === "number" ? message.sequence : tankEventSeq,
          created_at:
            typeof message.created_at === "string" && message.created_at ? message.created_at : writtenAt,
        }
      : {}),
  };
}

function isTankEvent(message: RunnerEvent): boolean {
  return typeof message.event_id === "string" && typeof message.visibility === "string";
}

