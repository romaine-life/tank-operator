import { CosmosClient, type Container } from "@azure/cosmos";
import { DefaultAzureCredential } from "@azure/identity";

import type { Config } from "./config.js";

export interface TurnRecord {
  id: string;
  run_id: string;
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
  completed_at?: string | null;
  last_error?: string;
  _etag?: string;
  [key: string]: unknown;
}

export class TurnQueue {
  private readonly client: CosmosClient;
  private readonly container: Container;

  constructor(private readonly cfg: Config, private readonly provider: "claude" | "codex") {
    this.client = new CosmosClient({
      endpoint: cfg.cosmosEndpoint,
      aadCredentials: new DefaultAzureCredential(),
    });
    this.container = this.client
      .database(cfg.cosmosDatabase)
      .container(cfg.turnQueueContainer);
  }

  async claimNext(): Promise<TurnRecord | null> {
    const iterator = this.container.items.query<TurnRecord>(
      {
        query:
          "SELECT TOP 1 * FROM c WHERE c.session_id = @session_id AND c.status = @status AND c.source = @source AND c.provider = @provider ORDER BY c.created_at ASC",
        parameters: [
          { name: "@session_id", value: this.cfg.sessionId },
          { name: "@status", value: "pending" },
          { name: "@source", value: "sdk" },
          { name: "@provider", value: this.provider },
        ],
      },
      { partitionKey: this.cfg.sessionId },
    );
    const page = await iterator.fetchNext();
    const record = page.resources[0];
    if (!record) return null;
    return this.claim(record);
  }

  async markCompleted(record: TurnRecord): Promise<void> {
    await this.markStatus(record, "completed");
  }

  async markFailed(record: TurnRecord, err: unknown): Promise<void> {
    await this.markStatus(record, "failed", errorText(err));
  }

  private async claim(record: TurnRecord): Promise<TurnRecord | null> {
    const now = new Date().toISOString();
    const claimed: TurnRecord = {
      ...record,
      status: "claimed",
      claimed_at: record.claimed_at ?? now,
    };
    try {
      const response = await this.container
        .item(record.id, record.session_id)
        .replace<TurnRecord>(claimed, etagOptions(record));
      return response.resource ?? claimed;
    } catch (err) {
      if (isClaimRace(err)) return null;
      throw err;
    }
  }

  private async markStatus(
    record: TurnRecord,
    status: "completed" | "failed",
    lastError?: string,
  ): Promise<void> {
    const item = this.container.item(record.id, record.session_id);
    const response = await item.read<TurnRecord>();
    const current = response.resource;
    if (!current) return;
    if (current.status === "completed" || current.status === "failed") return;
    const now = new Date().toISOString();
    await item.replace<TurnRecord>(
      {
        ...current,
        status,
        completed_at: now,
        ...(lastError ? { last_error: lastError } : {}),
      },
      etagOptions(current),
    );
  }
}

function etagOptions(record: TurnRecord) {
  return record._etag
    ? { accessCondition: { type: "IfMatch", condition: record._etag } }
    : undefined;
}

function isClaimRace(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const statusCode = (err as { statusCode?: unknown; code?: unknown }).statusCode;
  const code = (err as { statusCode?: unknown; code?: unknown }).code;
  return statusCode === 409 || statusCode === 412 || code === 409 || code === 412;
}

function errorText(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

export function turnClientNonce(record: TurnRecord): string {
  return record.client_nonce?.trim() || record.run_id;
}
