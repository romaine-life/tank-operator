import { test } from "node:test";
import assert from "node:assert/strict";

import type { Config } from "../config.js";
import type { CodexEvent } from "../cosmos.js";
import { isTankConversationEvent } from "../conversation.js";
import {
  CodexTankEventAdapter,
  canonicalEventsForCodexEvent,
  type CodexAdapterTurn,
} from "./codex.js";

function cfg(): Config {
  return {
    sessionId: "63",
    ownerEmail: "user@example.com",
    cosmosEndpoint: "https://example.invalid",
    cosmosDatabase: "tank-operator",
    sessionEventsContainer: "session-events",
    workspace: "/workspace",
    wsPort: 8090,
  };
}

function acceptedTurn(fields: Partial<CodexAdapterTurn> = {}): CodexAdapterTurn {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    turnSeq: 7,
    ...fields,
  };
}

function mappedEvent(adapter: CodexTankEventAdapter, event: CodexEvent) {
  const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), event);
  assert.equal(events.length, 1);
  assert.equal(isTankConversationEvent(events[0]!), true);
  return events[0]!;
}

test("maps Codex item updates to live-only Tank deltas with delta payload", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const first = mappedEvent(adapter, {
    type: "item.updated",
    item: {
      id: "item_reasoning_1",
      type: "reasoning",
      text: "think",
    },
  });
  const second = mappedEvent(adapter, {
    type: "item.updated",
    item: {
      id: "item_reasoning_1",
      type: "reasoning",
      text: "thinking",
    },
  });

  assert.equal(first.type, "item.delta");
  assert.equal(first.actor, "assistant");
  assert.equal(first.visibility, "live-only");
  assert.equal(first.payload?.kind, "reasoning");
  assert.equal(first.payload?.delta, "think");
  assert.equal(first.payload?.text, undefined);
  assert.equal(second.payload?.delta, "ing");
  assert.equal(second.payload?.text, undefined);
});

test("preserves completed Codex text as durable Tank item text", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_agent_1",
      type: "agent_message",
      text: "All tests passed.",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.equal(event.source, "codex");
  assert.equal(event.actor, "assistant");
  assert.equal(event.item_id, "item_agent_1");
  assert.equal(event.visibility, "durable");
  assert.equal(event.payload?.kind, "agent_message");
  assert.equal(event.payload?.text, "All tests passed.");
  assert.equal(event.payload?.delta, undefined);
});

test("maps Codex tool items to Tank tool items with command payload", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_1",
      type: "command_execution",
      command: "npm test",
      aggregated_output: "ok",
      exit_code: 0,
      status: "completed",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.equal(event.actor, "tool");
  assert.equal(event.item_id, "item_command_1");
  assert.equal(event.payload?.kind, "command_execution");
  assert.equal(event.payload?.title, "npm test");
  assert.equal(event.payload?.text, "ok");
  assert.deepEqual(event.payload?.raw_item, {
    id: "item_command_1",
    type: "command_execution",
    command: "npm test",
    aggregated_output: "ok",
    exit_code: 0,
    status: "completed",
  });
});

test("maps errored Codex items to Tank item.failed", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_2",
      type: "command_execution",
      command: "npm test",
      error: "failed",
    },
  });

  assert.equal(event.type, "item.failed");
  assert.equal(event.actor, "tool");
  assert.equal(event.visibility, "durable");
  assert.equal(event.payload?.error, "failed");
});

test("maps Codex terminal events to Tank turn lifecycle", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const completed = mappedEvent(adapter, { type: "turn.completed", usage: { input_tokens: 10 } });
  assert.equal(completed.type, "turn.completed");
  assert.deepEqual(completed.payload?.usage, { input_tokens: 10 });

  const failed = mappedEvent(adapter, { type: "error", message: "quota exceeded" });
  assert.equal(failed.type, "turn.failed");
  assert.equal(failed.payload?.reason, "provider_failure");
  assert.equal(failed.payload?.error, "quota exceeded");
});

test("ignores unknown Codex provider event types", () => {
  const events = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    { type: "future.experimental.event", value: true },
  );
  assert.deepEqual(events, []);
});
