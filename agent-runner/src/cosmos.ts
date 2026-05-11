// Cosmos sink for canonical SDK events. Producer contract per the
// design discussion:
//   - One serialization at the producer (the runner)
//   - Same bytes go to Cosmos and to the WS broadcast
//   - DB write happens BEFORE the WS push (read-your-writes ordering)
//   - Idempotent receivers — dedupe by event uuid
//
// "Canonical" = events the SPA's history-replay path should see:
//   system (init / compact_boundary), user, assistant, tool_use_summary,
//   result, permission_denied, rate_limit, plugin_install
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

const CANONICAL_TYPES = new Set<string>([
  "system",
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

export function isCanonical(message: SDKMessage): boolean {
  const t = (message as any).type as string | undefined;
  if (!t) return false;
  if (CANONICAL_TOP_LEVEL_TYPES.has(t)) return true;
  if (CANONICAL_TYPES.has(t)) {
    if (t === "system") {
      const subtype = (message as any).subtype as string | undefined;
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
  // monotonic — naturally sorts by emit order). Partition is the
  // orchestrator's session_id (the integer "63") not the SDK's internal
  // session_id — the SPA queries on the integer (it knows the pod-level
  // id), and pod restart may yield a new SDK session within the same
  // tank-operator session. The SDK's session_id rides along as a field.
  async upsert(message: SDKMessage): Promise<void> {
    const doc: Record<string, unknown> = {
      ...(message as Record<string, unknown>),
      id: (message as any).uuid as string,
      tank_session_id: this.cfg.sessionId,
      email: this.cfg.ownerEmail,
      written_at: new Date().toISOString(),
    };
    await this.container.items.upsert(doc);
  }
}
