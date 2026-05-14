// Cosmos sink for canonical SDK events. The runner is the producer and the
// durable session-events container is the transcript source of truth.
//
// "Canonical" = events the SPA's history-replay path should see:
//   system (init / compact_boundary), user, assistant, tool_use_summary,
//   result, permission_denied, rate_limit, plugin_install, tank.user_message
//
// "Live-only" = transient deltas that drive the typewriter effect but
// have no durable value (Discord's "typing..." analog):
//   stream_event, tool_progress, hook_*, task_*, status, prompt_suggestion
//
// SDK type ref: https://code.claude.com/docs/en/agent-sdk/typescript

import { CosmosClient } from "@azure/cosmos";
import { DefaultAzureCredential } from "@azure/identity";
import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";

import type { Config } from "./config.js";
import { isDurableTankConversationEvent } from "./conversation.js";

export interface RunnerEvent {
  type: string;
  uuid?: string;
  event_id?: string;
  subtype?: string;
  [k: string]: unknown;
}

const CANONICAL_TYPES = new Set<string>([
  "system",
  "tank.user_message",
  "user",
  "assistant",
  "result",
]);

const CANONICAL_SYSTEM_SUBTYPES = new Set<string>([
  "init",
  "compact_boundary",
  "tool_use_summary",
  "permission_denied",
  "plugin_install",
]);

// Some SDK message types arrive top-level as their own discriminator
// rather than `system/<subtype>`. Treat them as canonical regardless.
const CANONICAL_TOP_LEVEL_TYPES = new Set<string>([
  "rate_limit",
]);

export function isCanonical(message: RunnerEvent | SDKMessage): boolean {
  if (isDurableTankConversationEvent(message as RunnerEvent)) return true;
  const t = (message as RunnerEvent).type;
  if (!t) return false;
  if (CANONICAL_TOP_LEVEL_TYPES.has(t)) return true;
  if (CANONICAL_TYPES.has(t)) {
    if (t === "system") {
      const subtype = (message as RunnerEvent).subtype;
      // Some system events (status, hook progress) are ephemeral — only
      // the documented turn-level subtypes are durable.
      return subtype ? CANONICAL_SYSTEM_SUBTYPES.has(subtype) : false;
    }
    return true;
  }
  return false;
}

export class CosmosSink {
  private readonly client: CosmosClient;
  private readonly container;

  constructor(private readonly cfg: Config) {
    this.client = new CosmosClient({
      endpoint: cfg.cosmosEndpoint,
      aadCredentials: new DefaultAzureCredential(),
    });
    this.container = this.client
      .database(cfg.cosmosDatabase)
      .container(cfg.sessionEventsContainer);
  }

  // Write a canonical event to Cosmos. Doc id is the SDK's uuid (v7,
  // monotonic, so it naturally sorts by emit order). Partition is Tank's
  // scoped session storage key, not the SDK's internal session_id. The SPA
  // still addresses sessions by public pod-level id, while runner-process
  // restarts may yield a new SDK session within the same still-live Tank
  // session. The SDK's session_id rides along as a field.
  async upsert(message: RunnerEvent & { uuid: string }): Promise<void> {
    const doc = this.docFromMessage(message);
    await this.container.items.upsert(doc);
  }

  async create(message: RunnerEvent & { uuid: string }): Promise<"created" | "exists"> {
    const doc = this.docFromMessage(message);
    try {
      await this.container.items.create(doc);
      return "created";
    } catch (err) {
      if (isConflict(err)) return "exists";
      throw err;
    }
  }

  async findTurnTerminal(turnID: string): Promise<RunnerEvent | null> {
    const iterator = this.container.items.query<RunnerEvent>(
      {
        query:
          "SELECT TOP 1 * FROM c WHERE c.tank_session_id = @session_id AND c.turn_id = @turn_id AND (c.type = @completed OR c.type = @failed OR c.type = @interrupted)",
        parameters: [
          { name: "@session_id", value: this.cfg.sessionStorageKey },
          { name: "@turn_id", value: turnID },
          { name: "@completed", value: "turn.completed" },
          { name: "@failed", value: "turn.failed" },
          { name: "@interrupted", value: "turn.interrupted" },
        ],
      },
      { partitionKey: this.cfg.sessionStorageKey },
    );
    const page = await iterator.fetchNext();
    return page.resources[0] ?? null;
  }

  private docFromMessage(message: RunnerEvent & { uuid: string }): Record<string, unknown> {
    return {
      ...message,
      id: message.uuid,
      tank_session_id: this.cfg.sessionStorageKey,
      tank_public_session_id: this.cfg.sessionId,
      email: this.cfg.ownerEmail,
      runtime: "claude",
      written_at:
        typeof message.written_at === "string"
          ? message.written_at
          : new Date().toISOString(),
    };
  }
}

function isConflict(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const statusCode = (err as { statusCode?: unknown; code?: unknown }).statusCode;
  const code = (err as { statusCode?: unknown; code?: unknown }).code;
  return statusCode === 409 || code === 409 || code === "Conflict";
}
