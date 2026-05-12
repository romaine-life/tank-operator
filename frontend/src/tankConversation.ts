export const TANK_ACTORS = ["user", "assistant", "system", "tool", "runner"] as const;

export type TankActor = (typeof TANK_ACTORS)[number];

export const TANK_EVENT_SOURCES = ["tank", "claude", "codex", "legacy-run"] as const;

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

export interface TankConversationEvent<
  TPayload extends Record<string, unknown> = Record<string, unknown>,
> {
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
  producer?: TankProducerMetadata;
  visibility: TankVisibility;
  payload?: TPayload;
}

export function isTankConversationEvent(event: unknown): event is TankConversationEvent {
  if (!event || typeof event !== "object") return false;
  const candidate = event as Record<string, unknown>;
  return (
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
  );
}

export function isDurableTankConversationEvent(event: unknown): event is TankConversationEvent {
  return isTankConversationEvent(event) && event.visibility !== "live-only";
}
