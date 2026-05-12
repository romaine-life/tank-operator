// Cosmos sink for canonical codex SDK events. Same producer contract as
// agent-runner/src/cosmos.ts:
//   - One serialization at the producer (the runner)
//   - Same bytes go to Cosmos and to the WS broadcast
//   - DB write happens BEFORE the WS push (read-your-writes ordering)
//   - Idempotent receivers — dedupe by event id (stamped here)
//
// Difference from claude: the codex SDK's `ThreadEvent` union has no
// per-event `uuid` field — only contained `ThreadItem`s carry ids, and
// only some events have items. We stamp our own random v4 uuid as the
// doc id at production time. The producer is the only writer, so the
// id is unique by construction.
//
// "Canonical" = events the SPA's history-replay path should see:
//   thread.started     — session boot marker (analog of claude system/init)
//   turn.completed     — turn-end marker with usage
//   turn.failed        — turn-level error
//   item.completed     — main durable signal: agent_message, reasoning,
//                        command_execution, file_change, mcp_tool_call,
//                        web_search, todo_list, error items
//   error              — thread-level error (e.g. unrecoverable SDK error)
//
// "Live-only" = transient deltas the SPA may use for typewriter UX but
// have no durable value:
//   turn.started, item.started, item.updated
//
// SDK type ref:
//   https://github.com/openai/codex/blob/main/sdk/typescript/src/events.ts

import { CosmosClient } from "@azure/cosmos";
import { DefaultAzureCredential } from "@azure/identity";
import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";

const CANONICAL_TYPES = new Set<string>([
  "thread.started",
  "turn.completed",
  "turn.failed",
  "item.completed",
  "error",
]);

export interface CodexEvent {
  type: string;
  [k: string]: unknown;
}

export function isCanonical(event: CodexEvent): boolean {
  return CANONICAL_TYPES.has(event.type);
}

// Stamp a generated uuid on the event. The runner uses the same stamped
// value when dispatching to both Cosmos and the WS, so consumers can
// dedupe across the history+live join.
export function stampEventID(event: CodexEvent): CodexEvent & { uuid: string } {
  return { ...event, uuid: randomUUID() };
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

  // Write a canonical event. Doc id is the runner-stamped uuid; partition
  // is the orchestrator's session_id. Matches the shape agent-runner uses,
  // so the orchestrator's read endpoint and the SPA's chat pane consume
  // both claude- and codex-runner events out of the same container.
  async upsert(event: CodexEvent & { uuid: string }): Promise<void> {
    const doc: Record<string, unknown> = {
      ...event,
      id: event.uuid,
      tank_session_id: this.cfg.sessionId,
      email: this.cfg.ownerEmail,
      runtime: "codex",
      written_at: new Date().toISOString(),
    };
    await this.container.items.upsert(doc);
  }
}
