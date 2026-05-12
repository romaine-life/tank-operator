export type TankActor = "user" | "assistant" | "system" | "tool" | "runner";

export type TankEventSource = "tank" | "claude" | "codex" | "legacy-run";

export type TankVisibility = "durable" | "live-only" | "audit-only";

export type TankEventType =
  | "conversation.started"
  | "conversation.archived"
  | "user_message.created"
  | "turn.submitted"
  | "turn.started"
  | "turn.completed"
  | "turn.failed"
  | "turn.interrupted"
  | "item.started"
  | "item.delta"
  | "item.completed"
  | "item.failed"
  | "tool.approval_requested"
  | "tool.approval_resolved"
  | "session.activity_updated"
  | "read_state.updated";

const TANK_EVENT_TYPES = new Set<string>([
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
]);

const TANK_ACTORS = new Set<string>(["user", "assistant", "system", "tool", "runner"]);
const TANK_EVENT_SOURCES = new Set<string>(["tank", "claude", "codex", "legacy-run"]);
const TANK_VISIBILITIES = new Set<string>(["durable", "live-only", "audit-only"]);

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
    TANK_EVENT_TYPES.has(candidate.type) &&
    typeof candidate.actor === "string" &&
    TANK_ACTORS.has(candidate.actor) &&
    typeof candidate.source === "string" &&
    TANK_EVENT_SOURCES.has(candidate.source) &&
    typeof candidate.created_at === "string" &&
    typeof candidate.visibility === "string" &&
    TANK_VISIBILITIES.has(candidate.visibility)
  );
}

export function isDurableTankConversationEvent(event: unknown): event is TankConversationEvent {
  return isTankConversationEvent(event) && event.visibility !== "live-only";
}
