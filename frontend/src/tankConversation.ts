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
