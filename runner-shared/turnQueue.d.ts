export const TURN_QUEUE_LEASE_MS: number;
export const TURN_QUEUE_MAX_ATTEMPTS: number;

export interface TurnQueueConfig {
  sessionId: string;
  ownerEmail: string;
  cosmosEndpoint: string;
  cosmosDatabase: string;
  turnQueueContainer: string;
}

export interface TurnRecord {
  id: string;
  turn_id: string;
  session_id: string;
  email: string;
  provider: "claude" | "codex" | string;
  source?: string;
  client_nonce?: string;
  prompt: string;
  model?: string;
  permission_mode?: string;
  skill_name?: string;
  follow_up?: boolean;
  status: "pending" | "claimed" | "completed" | "failed" | string;
  created_at?: string;
  claimed_at?: string | null;
  claim_id?: string | null;
  claimed_by?: string | null;
  claim_expires_at?: string | null;
  attempt_count?: number;
  available_at?: string | null;
  completed_at?: string | null;
  last_error?: string;
  _etag?: string;
  [key: string]: unknown;
}

export interface TurnQueueDependencies {
  CosmosClient: new (...args: any[]) => any;
  DefaultAzureCredential: new () => unknown;
}

export class SharedTurnQueue {
  constructor(
    cfg: TurnQueueConfig,
    provider: "claude" | "codex" | string,
    deps: TurnQueueDependencies,
  );
  claimNext(): Promise<TurnRecord | null>;
  enqueueDelayed(args: {
    prompt: string;
    clientNonce: string;
    availableAt: string;
  }): Promise<TurnRecord>;
  markCompleted(record: TurnRecord): Promise<boolean>;
  markFailed(record: TurnRecord, err: unknown): Promise<boolean>;
  renewLease(record: TurnRecord): Promise<boolean>;
  startLeaseRenewal(record: TurnRecord): () => void;
  attemptsExceeded(record: TurnRecord): boolean;
}

export function buildClaimedRecord(
  record: TurnRecord,
  args: { claimID: string; claimedBy: string; now: Date; leaseMs: number },
): TurnRecord;

export function buildDelayedTurnRecord(args: {
  sessionID: string;
  email: string;
  provider: "claude" | "codex" | string;
  prompt: string;
  clientNonce: string;
  availableAt: string;
  now: Date;
}): TurnRecord;

export function claimAttemptsExceeded(
  record: TurnRecord,
  maxAttempts?: number,
): boolean;

export function turnClientNonce(record: TurnRecord): string;
