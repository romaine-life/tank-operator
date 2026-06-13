import type {
  TankFinalAnswer,
  TankActor,
  TankConversationEvent,
  UserMessageAttachmentDisplay,
} from "./conversation.js";

export function turnIDForClientNonce(clientNonce: string): string;
export function userTimelineID(turnID: string): string;
export function itemTimelineID(turnID: string, providerItemID: string): string;
export function questionClientNonce(
  askingTurnID: string,
  providerTimelineID: string,
): string;
export function questionMessageTimelineID(
  askingTurnID: string,
  providerTimelineID: string,
): string;
export function shellTaskTimelineID(turnID: string, taskID: string): string;

export interface UserSubmissionArgs {
  sessionID: string;
  clientNonce: string;
  text: string;
  message: unknown;
  attachments?: UserMessageAttachmentDisplay[];
  runtime: "claude" | "codex";
  skillName?: string;
  now?: string;
}

export interface UserSubmissionEvents {
  turnID: string;
  userMessage: TankConversationEvent;
  turnSubmitted: TankConversationEvent;
}

export function userSubmissionEvents(
  args: UserSubmissionArgs,
): UserSubmissionEvents;

export interface TurnEventArgs {
  sessionID: string;
  turnID: string;
  clientNonce?: string;
  source: "claude" | "codex";
  type:
    | "turn.started"
    | "turn.claimed"
    | "turn.usage"
    | "turn.completed"
    | "turn.failed"
    | "turn.interrupted"
    | "turn.awaiting_input";
  reason?: string;
  usage?: unknown;
  usageObservation?: unknown;
  error?: unknown;
  finalAnswer?: TankFinalAnswer;
  // turn.awaiting_input handoff payload: the Tank-canonical questions the
  // agent asked, plus the AskUserQuestion item ids the /answer endpoint
  // targets.
  questions?: unknown;
  awaitingProviderItemID?: string;
  awaitingTimelineID?: string;
  awaitingProviderTimelineID?: string;
  askingTurnID?: string;
  questionTurnID?: string;
  providerEventID?: string;
  backgroundWorkPending?: boolean;
}

export function turnEvent(args: TurnEventArgs): TankConversationEvent;

export interface AskUserQuestionHandoffEventArgs {
  sessionID: string;
  askingTurnID: string;
  askingClientNonce: string;
  source: "claude" | "codex";
  providerItemID: string;
  providerTimelineID: string;
  questions: unknown[];
}

export interface AskUserQuestionHandoffEvents {
  questionClientNonce: string;
  questionTurnID: string;
  questionTimelineID: string;
  questionMessage: TankConversationEvent;
  invocation: TankConversationEvent;
  questionSubmitted: TankConversationEvent;
  awaitingInput: TankConversationEvent;
}

export function askUserQuestionHandoffEvents(
  args: AskUserQuestionHandoffEventArgs,
): AskUserQuestionHandoffEvents;

export interface ItemEventArgs {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  type: "item.started" | "item.completed" | "item.failed";
  providerItemID: string;
  parentID?: string;
  actor: TankActor;
  providerEventID?: string;
  payload?: Record<string, unknown>;
}

export function itemEvent(args: ItemEventArgs): TankConversationEvent;

export interface ShellTaskEventArgs {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  type: "shell_task.started" | "shell_task.updated" | "shell_task.exited";
  taskID: string;
  status: string;
  providerItemID?: string;
  parentID?: string;
  providerEventID?: string;
  payload?: Record<string, unknown>;
}

export function shellTaskEvent(args: ShellTaskEventArgs): TankConversationEvent;

export interface ContextCompactedEventArgs {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  trigger: "auto" | "manual";
  preTokens?: number;
  providerEventID?: string;
}

export function contextCompactedEvent(
  args: ContextCompactedEventArgs,
): TankConversationEvent;

export function stampTankEvent(
  event: TankConversationEvent,
): TankConversationEvent & {
  uuid: string;
  order_key: string;
  sequence: number;
  written_at: string;
};

export function stableIDPart(value: string): string;
