import { createHash } from "node:crypto";

export const TANK_ACTORS = ["user", "assistant", "system", "tool", "runner"] as const;

export type TankActor = (typeof TANK_ACTORS)[number];

export const TANK_EVENT_SOURCES = ["tank", "claude", "codex"] as const;

export type TankEventSource = (typeof TANK_EVENT_SOURCES)[number];

export const TANK_VISIBILITIES = ["durable", "live-only", "audit-only"] as const;

export type TankVisibility = (typeof TANK_VISIBILITIES)[number];

export const TANK_EVENT_TYPES = [
  "conversation.started",
  "conversation.archived",
  "user_message.created",
  "turn.submitted",
  "turn.started",
  "turn.completed",
  "turn.failed",
  "turn.interrupted",
  "item.started",
  "item.delta",
  "item.completed",
  "item.failed",
  "tool.approval_requested",
  "tool.approval_resolved",
  "session.activity_updated",
  "read_state.updated",
] as const;

export type TankEventType = (typeof TANK_EVENT_TYPES)[number];

export interface TankConversationEvent {
  event_id: string;
  order_key?: string;
  sequence?: number;
  conversation_id?: string;
  session_id: string;
  turn_id?: string;
  item_id?: string;
  parent_id?: string;
  client_nonce?: string;
  actor: TankActor;
  source: TankEventSource;
  type: TankEventType;
  created_at: string;
  producer?: {
    name?: string;
    version?: string;
    runtime?: string;
    provider_event_id?: string;
  };
  visibility: TankVisibility;
  payload?: Record<string, unknown>;
  [key: string]: unknown;
}

const TANK_EVENT_TYPE_SET = new Set<string>(TANK_EVENT_TYPES);
const TANK_ACTOR_SET = new Set<string>(TANK_ACTORS);
const TANK_EVENT_SOURCE_SET = new Set<string>(TANK_EVENT_SOURCES);
const TANK_VISIBILITY_SET = new Set<string>(TANK_VISIBILITIES);

export function isTankConversationEvent(event: { [key: string]: unknown }): event is TankConversationEvent {
  return (
    typeof event.event_id === "string" &&
    typeof event.session_id === "string" &&
    typeof event.type === "string" &&
    TANK_EVENT_TYPE_SET.has(event.type) &&
    typeof event.actor === "string" &&
    TANK_ACTOR_SET.has(event.actor) &&
    typeof event.source === "string" &&
    TANK_EVENT_SOURCE_SET.has(event.source) &&
    typeof event.created_at === "string" &&
    typeof event.visibility === "string" &&
    TANK_VISIBILITY_SET.has(event.visibility)
  );
}

export function isDurableTankConversationEvent(event: { [key: string]: unknown }): boolean {
  return isTankConversationEvent(event) && event.visibility !== "live-only";
}

export function normalizeClientNonce(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

export function turnIDForClientNonce(clientNonce: string): string {
  return `turn_${stableIDPart(clientNonce)}`;
}

export function userItemID(turnID: string): string {
  return `${turnID}:user`;
}

export function userSubmissionEvents(args: {
  sessionID: string;
  clientNonce: string;
  text: string;
  message: unknown;
  runtime: "claude" | "codex";
  now?: string;
}): { turnID: string; userMessage: TankConversationEvent; turnSubmitted: TankConversationEvent } {
  const createdAt = args.now ?? new Date().toISOString();
  const turnID = turnIDForClientNonce(args.clientNonce);
  const producer = { name: `${args.runtime}-runner`, runtime: args.runtime };
  return {
    turnID,
    userMessage: {
      event_id: `${turnID}:user_message.created`,
      conversation_id: args.sessionID,
      session_id: args.sessionID,
      turn_id: turnID,
      item_id: userItemID(turnID),
      client_nonce: args.clientNonce,
      actor: "user",
      source: "tank",
      type: "user_message.created",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        text: args.text,
        message: args.message,
      },
    },
    turnSubmitted: {
      event_id: `${turnID}:turn.submitted`,
      conversation_id: args.sessionID,
      session_id: args.sessionID,
      turn_id: turnID,
      client_nonce: args.clientNonce,
      actor: "runner",
      source: "tank",
      type: "turn.submitted",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        status: "submitted",
      },
    },
  };
}

export function turnEvent(args: {
  sessionID: string;
  turnID: string;
  clientNonce?: string;
  source: "claude" | "codex";
  type: "turn.started" | "turn.completed" | "turn.failed" | "turn.interrupted";
  reason?: string;
  usage?: unknown;
  error?: unknown;
  providerEventID?: string;
}): TankConversationEvent {
  const payload: Record<string, unknown> = {};
  if (args.reason) payload.reason = args.reason;
  if (args.usage !== undefined) payload.usage = args.usage;
  if (args.error !== undefined) payload.error = args.error;
  return {
    event_id: `${args.turnID}:${args.type}:${args.reason ?? args.providerEventID ?? "runner"}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    ...(args.clientNonce ? { client_nonce: args.clientNonce } : {}),
    actor: "runner",
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
      ...(args.providerEventID ? { provider_event_id: args.providerEventID } : {}),
    },
    visibility: "durable",
    ...(Object.keys(payload).length > 0 ? { payload } : {}),
  };
}

export function itemEvent(args: {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  type:
    | "item.started"
    | "item.delta"
    | "item.completed"
    | "item.failed"
    | "tool.approval_requested"
    | "tool.approval_resolved";
  itemID: string;
  parentID?: string;
  actor: TankActor;
  visibility?: TankVisibility;
  providerEventID?: string;
  payload?: Record<string, unknown>;
}): TankConversationEvent {
  return {
    event_id: `${args.turnID}:${args.type}:${stableIDPart(args.itemID)}:${args.providerEventID ?? "runner"}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    item_id: args.itemID,
    parent_id: args.parentID ?? args.turnID,
    actor: args.actor,
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
      ...(args.providerEventID ? { provider_event_id: args.providerEventID } : {}),
    },
    visibility: args.visibility ?? "durable",
    ...(args.payload ? { payload: args.payload } : {}),
  };
}

function stableIDPart(value: string): string {
  const safe = value
    .trim()
    .replace(/[^A-Za-z0-9_.:-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
  const hash = createHash("sha256").update(value).digest("hex").slice(0, 12);
  if (safe.length >= 6 && safe.length <= 80) return safe;
  if (safe.length > 80) return `${safe.slice(0, 64)}-${hash}`;
  return hash;
}
