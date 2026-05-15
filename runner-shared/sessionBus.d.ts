export const SESSION_COMMAND_ACK_MS: number;
export const SESSION_COMMAND_MAX_DELIVER: number;

export interface SessionBusConfig {
  sessionId: string;
  sessionStorageKey?: string;
  ownerEmail: string;
  natsURL: string;
  natsToken?: string;
  natsStream: string;
  operatorInternalURL: string;
  operatorTokenPath: string;
}

export interface SessionBusDependencies {
  connect: (...args: any[]) => Promise<any>;
  jetstream: (...args: any[]) => any;
  jetstreamManager: (...args: any[]) => Promise<any>;
  AckPolicy: Record<string, string>;
  DeliverPolicy: Record<string, string>;
  ReplayPolicy: Record<string, string>;
  nanos: (millis: number) => unknown;
}

export interface SessionCommand {
  id: string;
  command_id: string;
  type: "submit_turn" | "interrupt_turn" | "input_reply" | string;
  session_id: string;
  session_storage_key?: string;
  email: string;
  provider: "claude" | "codex" | string;
  source?: string;
  client_nonce?: string;
  target_turn_id?: string;
  target_item_id?: string;
  target_provider_item_id?: string;
  input_reply?: string;
  prompt?: string;
  model?: string;
  permission_mode?: string;
  skill_name?: string;
  follow_up?: boolean;
  status?: string;
  attempt_count?: number;
  created_at?: string;
  available_at?: string | null;
  message?: unknown;
  [key: string]: unknown;
}

export class SessionCommandRecord implements SessionCommand {
  constructor(command: SessionCommand, message: unknown);
  id: string;
  command_id: string;
  type: string;
  session_id: string;
  session_storage_key?: string;
  email: string;
  provider: string;
  source?: string;
  client_nonce?: string;
  target_turn_id?: string;
  target_item_id?: string;
  target_provider_item_id?: string;
  input_reply?: string;
  prompt?: string;
  model?: string;
  permission_mode?: string;
  skill_name?: string;
  follow_up?: boolean;
  status?: string;
  attempt_count?: number;
  [key: string]: unknown;
  ack(): void;
  nak(delayMs?: number): void;
  term(reason?: string): void;
  working(): void;
}

export class SharedSessionBus {
  constructor(
    cfg: SessionBusConfig,
    provider: "claude" | "codex" | string,
    deps: SessionBusDependencies,
  );
  startCommandConsumer(
    handler: (record: SessionCommandRecord) => Promise<void>,
    signal?: AbortSignal,
  ): Promise<() => Promise<void>>;
  publishEvent(event: Record<string, unknown>): Promise<"created" | "exists">;
  enqueueDelayed(args: {
    prompt: string;
    clientNonce: string;
    availableAt: string;
  }): Promise<SessionCommand>;
  findTurnTerminal(turnID: string): Promise<Record<string, unknown> | null>;
  markCompleted(record: SessionCommandRecord): Promise<boolean>;
  markFailed(record: SessionCommandRecord, err: unknown): Promise<boolean>;
  startWorkHeartbeat(record: SessionCommandRecord): () => void;
  attemptsExceeded(record: SessionCommandRecord): boolean;
  close(): Promise<void>;
}

export function buildDelayedCommand(args: {
  sessionID: string;
  sessionStorageKey?: string;
  email: string;
  provider: "claude" | "codex" | string;
  prompt: string;
  clientNonce: string;
  availableAt: string;
}): SessionCommand;

export function isInterruptCommand(record: SessionCommand | null | undefined): boolean;
export function isInputReplyCommand(record: SessionCommand | null | undefined): boolean;
export function commandClientNonce(record: SessionCommand): string;
export function turnIDForClientNonce(clientNonce: string): string;
