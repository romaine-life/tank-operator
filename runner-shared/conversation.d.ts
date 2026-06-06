export const TANK_ACTORS: readonly [
  "user",
  "assistant",
  "system",
  "tool",
  "runner",
];
export type TankActor = (typeof TANK_ACTORS)[number];

export const TANK_EVENT_SOURCES: readonly [
  "tank",
  "claude",
  "codex",
  "antigravity",
];
export type TankEventSource = (typeof TANK_EVENT_SOURCES)[number];

export const TANK_VISIBILITIES: readonly ["durable"];
export type TankVisibility = (typeof TANK_VISIBILITIES)[number];

export const TANK_EVENT_TYPES: readonly [
  "user_message.created",
  "assistant_message.created",
  "turn.submitted",
  "turn.claimed",
  "turn.started",
  "turn.usage",
  "turn.completed",
  "turn.failed",
  "turn.command_failed",
  "turn.interrupt_requested",
  "turn.interrupted",
  "turn.input_answered",
  "context.compacted",
  "session.status",
  "item.started",
  "item.completed",
  "item.failed",
  "shell_task.started",
  "shell_task.updated",
  "shell_task.exited",
  "turn.awaiting_input",
  "turn.awaiting_input.invocation",
];
export type TankEventType = (typeof TANK_EVENT_TYPES)[number];

export interface TankProducerMetadata {
  name?: string;
  version?: string;
  runtime?: string;
  provider_event_id?: string;
}

export type UserMessageDisplay =
  | { kind: "plain" }
  | { kind: "skill_invocation"; skill_name: string; supplemental_text?: string }
  | { kind: "ask_user_question" };

export interface UserMessageAttachmentDisplay {
  label: string;
  name: string;
  kind: "image" | "file";
  path?: string;
  absPath?: string;
  size?: number;
}

export type TankItemOutcome =
  | { kind: "ok" }
  | {
      kind: "result_failed";
      reason:
        | "claude_tool_result_is_error"
        | "codex_item_status_failed"
        | "exit_code";
      code?: string | number;
    }
  | { kind: "execution_failed"; reason: "provider_item_error" };

export interface TankFinalAnswer {
  timelineIDs: string[];
  providerItemIDs?: string[];
}

export interface TankConversationEvent<
  TPayload extends Record<string, unknown> = Record<string, unknown>,
> {
  event_id: string;
  uuid?: string;
  order_key?: string;
  sequence?: number;
  conversation_id?: string;
  session_id: string;
  turn_id?: string;
  timeline_id?: string;
  provider_item_id?: string;
  task_id?: string;
  parent_id?: string;
  client_nonce?: string;
  actor: TankActor;
  source: TankEventSource;
  type: TankEventType;
  created_at: string;
  written_at?: string;
  producer?: TankProducerMetadata;
  visibility: TankVisibility;
  payload?: TPayload;
  [key: string]: unknown;
}

export function isTankConversationEvent(
  event: unknown,
): event is TankConversationEvent;
export function isDurableTankConversationEvent(
  event: unknown,
): event is TankConversationEvent;

export function normalizeClientNonce(value: unknown): string | null;
