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

const TANK_EVENT_TYPE_SET = new Set<string>(TANK_EVENT_TYPES);
const TANK_ACTOR_SET = new Set<string>(TANK_ACTORS);
const TANK_EVENT_SOURCE_SET = new Set<string>(TANK_EVENT_SOURCES);
const TANK_VISIBILITY_SET = new Set<string>(TANK_VISIBILITIES);

export interface TankProducerMetadata {
  name?: string;
  version?: string;
  runtime?: string;
  provider_event_id?: string;
}

export type UserMessageDisplay =
  | { kind: "plain" }
  | { kind: "skill_invocation"; skill_name: string; supplemental_text?: string };

export interface TankConversationEvent<
  TPayload extends Record<string, unknown> = Record<string, unknown>,
> {
  event_id: string;
  order_key?: string;
  sequence?: number;
  conversation_id?: string;
  session_id: string;
  turn_id?: string;
  timeline_id?: string;
  provider_item_id?: string;
  parent_id?: string;
  client_nonce?: string;
  actor: TankActor;
  source: TankEventSource;
  type: TankEventType;
  created_at: string;
  producer?: TankProducerMetadata;
  visibility: TankVisibility;
  payload?: TPayload;
}

export function isTankConversationEvent(event: unknown): event is TankConversationEvent {
  if (!event || typeof event !== "object") return false;
  const candidate = event as Record<string, unknown>;
  if (!(
    typeof candidate.event_id === "string" &&
    typeof candidate.session_id === "string" &&
    typeof candidate.type === "string" &&
    TANK_EVENT_TYPE_SET.has(candidate.type) &&
    typeof candidate.actor === "string" &&
    TANK_ACTOR_SET.has(candidate.actor) &&
    typeof candidate.source === "string" &&
    TANK_EVENT_SOURCE_SET.has(candidate.source) &&
    typeof candidate.created_at === "string" &&
    typeof candidate.visibility === "string" &&
    TANK_VISIBILITY_SET.has(candidate.visibility)
  )) {
    return false;
  }
  return hasOrderCursor(candidate) && isValidEventByType(candidate);
}

export function isDurableTankConversationEvent(event: unknown): event is TankConversationEvent {
  return isTankConversationEvent(event) && event.visibility !== "live-only";
}

function isValidEventByType(event: Record<string, unknown>): boolean {
  switch (event.type) {
    case "user_message.created":
      return event.actor === "user" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id", "timeline_id", "client_nonce"]) &&
        isUserMessagePayload(event.payload);
    case "turn.submitted":
      return event.actor === "runner" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id", "client_nonce"]) &&
        isStringPayload(event.payload, "status");
    case "turn.started":
    case "turn.completed":
    case "turn.failed":
    case "turn.interrupted":
      return event.actor === "runner" && hasStrings(event, ["turn_id"]);
    case "item.started":
    case "item.delta":
    case "item.completed":
    case "item.failed":
      return hasStrings(event, ["turn_id", "timeline_id"]) && isStringPayload(event.payload, "kind");
    case "tool.approval_requested":
    case "tool.approval_resolved":
      return event.actor === "tool" &&
        hasStrings(event, ["turn_id", "timeline_id"]) &&
        isStringPayload(event.payload, "kind");
    case "session.activity_updated":
      return isStringPayload(event.payload, "status");
    case "read_state.updated":
      return isStringPayload(event.payload, "last_read_order_key");
    default:
      return true;
  }
}

function hasOrderCursor(event: Record<string, unknown>): boolean {
  return typeof event.order_key === "string" && event.order_key
    ? true
    : typeof event.sequence === "number" && Number.isInteger(event.sequence) && event.sequence >= 0;
}

function hasStrings(event: Record<string, unknown>, keys: string[]): boolean {
  return keys.every((key) => typeof event[key] === "string" && event[key]);
}

function isUserMessagePayload(payload: unknown): boolean {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  const record = payload as Record<string, unknown>;
  if (typeof record.text !== "string" || !record.text) return false;
  return isUserMessageDisplay(record.display);
}

function isUserMessageDisplay(display: unknown): display is UserMessageDisplay {
  if (!display || typeof display !== "object" || Array.isArray(display)) return false;
  const record = display as Record<string, unknown>;
  if (record.kind === "plain") return true;
  return record.kind === "skill_invocation" &&
    typeof record.skill_name === "string" &&
    /^[A-Za-z0-9_-]{1,64}$/.test(record.skill_name) &&
    (record.supplemental_text === undefined || typeof record.supplemental_text === "string");
}

function isStringPayload(payload: unknown, key: string): boolean {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  const value = (payload as Record<string, unknown>)[key];
  return typeof value === "string" && value.length > 0;
}
