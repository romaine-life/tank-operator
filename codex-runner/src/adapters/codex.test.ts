import { test } from "node:test";
import assert from "node:assert/strict";

import type { Config } from "../config.js";
import type { CodexEvent } from "../sessionEvents.js";
import { isTankConversationEvent } from "../../../runner-shared/conversation.js";
import { stampTankEvent } from "../../../runner-shared/conversation-builders.js";
import {
  CodexTankEventAdapter,
  canonicalEventsForCodexEvent,
  type CodexAdapterTurn,
} from "./codex.js";

function cfg(): Config {
  return {
    sessionId: "63",
    sessionStorageKey: "63",
    ownerEmail: "user@example.com",
    natsURL: "nats://example.invalid:4222",
    natsToken: "",
    natsStream: "TANK_SESSION_BUS",
    operatorInternalURL: "",
    operatorTokenPath: "",
    workspace: "/workspace",
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
  // Adapter output is pre-stamp; the runner stamps order_key + sequence
  // before publishing. Validate the full envelope here so the test
  // mirrors what the persister receives.
  assert.equal(isTankConversationEvent(stampTankEvent(events[0]!)), true);
  return events[0]!;
}

test("emits no Tank event for Codex item.updated frames (live-only retired)", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
    type: "item.updated",
    item: {
      id: "item_reasoning_1",
      type: "reasoning",
      text: "think",
    },
  });
  assert.deepEqual(events, []);
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
  assert.equal(event.timeline_id, "turn-run-123:item:item_agent_1");
  assert.equal(event.provider_item_id, "item_agent_1");
  assert.equal(event.visibility, "durable");
  assert.equal(event.payload?.kind, "agent_message");
  assert.equal(event.payload?.text, "All tests passed.");
  assert.equal(event.payload?.delta, undefined);
});

test("carries streamed Codex text into durable completion when final item omits text", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  // item.updated frames emit no Tank event, but the adapter must
  // remember the running text so item.completed can fall back to it.
  for (const frame of ["Partial", "Partial response"]) {
    const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
      type: "item.updated",
      item: { id: "item_agent_streamed", type: "agent_message", text: frame },
    });
    assert.deepEqual(events, [], "item.updated must not produce a Tank event");
  }

  const completed = mappedEvent(adapter, {
    type: "item.completed",
    item: {
      id: "item_agent_streamed",
      type: "agent_message",
    },
  });

  assert.equal(completed.type, "item.completed");
  assert.equal(completed.visibility, "durable");
  assert.equal(completed.payload?.kind, "agent_message");
  assert.equal(completed.payload?.text, "Partial response");
  assert.equal(completed.payload?.delta, undefined);
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
  assert.equal(event.timeline_id, "turn-run-123:item:item_command_1");
  assert.equal(event.provider_item_id, "item_command_1");
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
