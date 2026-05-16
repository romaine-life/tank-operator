import type {
  TankActor,
  TankConversationEvent,
} from "./conversation.js";

export function turnIDForClientNonce(clientNonce: string): string;
export function userTimelineID(turnID: string): string;
export function itemTimelineID(turnID: string, providerItemID: string): string;

export interface UserSubmissionArgs {
  sessionID: string;
  clientNonce: string;
  text: string;
  message: unknown;
  runtime: "claude" | "codex";
  skillName?: string;
  now?: string;
}

export interface UserSubmissionEvents {
  turnID: string;
  userMessage: TankConversationEvent;
  turnSubmitted: TankConversationEvent;
}

export function userSubmissionEvents(args: UserSubmissionArgs): UserSubmissionEvents;

export interface TurnEventArgs {
  sessionID: string;
  turnID: string;
  clientNonce?: string;
  source: "claude" | "codex";
  type: "turn.started" | "turn.completed" | "turn.failed" | "turn.interrupted";
  reason?: string;
  usage?: unknown;
  error?: unknown;
  providerEventID?: string;
}

export function turnEvent(args: TurnEventArgs): TankConversationEvent;

export interface ItemEventArgs {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  type:
    | "item.started"
    | "item.completed"
    | "item.failed"
    | "tool.approval_requested"
    | "tool.approval_resolved";
  providerItemID: string;
  parentID?: string;
  actor: TankActor;
  providerEventID?: string;
  payload?: Record<string, unknown>;
}

export function itemEvent(args: ItemEventArgs): TankConversationEvent;

export function stampTankEvent(event: TankConversationEvent): TankConversationEvent & {
  uuid: string;
  order_key: string;
  sequence: number;
  written_at: string;
};

export function stableIDPart(value: string): string;
