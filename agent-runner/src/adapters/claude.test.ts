import { test } from "node:test";
import assert from "node:assert/strict";

import {
  canonicalEventsForClaudeMessage,
  type ClaudeTurnContext,
} from "./claude.js";
import type { Config } from "../config.js";
import {
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../../runner-shared/conversation.js";
import { stampTankEvent } from "../../../runner-shared/conversation-builders.js";

// Adapter output doesn't carry order_key/sequence until dispatch stamps
// it; running the event through stampTankEvent here mirrors what the
// runner does before publishing to the bus, so the assertion validates
// the full post-stamp envelope the persister sees.
function assertTankEventFixture(event: TankConversationEvent, label = event.type) {
  const stamped = stampTankEvent(event);
  assert.equal(isTankConversationEvent(stamped), true, `${label} should satisfy the Tank envelope`);
}

function turn(fields: Partial<ClaudeTurnContext> = {}): ClaudeTurnContext {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    interrupted: false,
    terminalEmitted: false,
    ...fields,
  };
}

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
    mcpConfig: "/workspace/.mcp.json",
  };
}

test("adapter maps Claude assistant text and tool_use blocks to Tank items", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-1",
      message: {
        content: [
          { type: "text", text: "I will inspect the workspace." },
          {
            type: "tool_use",
            id: "toolu_read",
            name: "Read",
            input: { file_path: "README.md" },
          },
        ],
      },
    },
    new Set<string>(),
  );

  assert.deepEqual(events.map((event) => event.type), [
    "item.completed",
    "item.started",
  ]);
  for (const event of events) assertTankEventFixture(event);
  assert.equal(events[0]?.actor, "assistant");
  assert.equal(events[0]?.timeline_id, "turn-run-123:item:assistant:claude-msg-1:text:0");
  assert.equal(events[0]?.provider_item_id, "assistant:claude-msg-1:text:0");
  assert.equal(events[0]?.payload?.kind, "message");
  assert.equal(events[0]?.payload?.text, "I will inspect the workspace.");
  assert.equal(events[1]?.actor, "tool");
  assert.equal(events[1]?.timeline_id, "turn-run-123:item:toolu_read");
  assert.equal(events[1]?.provider_item_id, "toolu_read");
  assert.equal(events[1]?.payload?.title, "Read");
});

test("adapter gives each text block in one Claude message a unique canonical id", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-multi-text",
      message: {
        content: [
          { type: "text", text: "First paragraph." },
          { type: "text", text: "Second paragraph." },
        ],
      },
    },
    new Set<string>(),
  );

  assert.deepEqual(events.map((event) => event.payload?.text), [
    "First paragraph.",
    "Second paragraph.",
  ]);
  assert.equal(new Set(events.map((event) => event.timeline_id)).size, 2);
  assert.equal(new Set(events.map((event) => event.event_id)).size, 2);
  assert.equal(events[0]?.timeline_id, "turn-run-123:item:assistant:claude-msg-multi-text:text:0");
  assert.equal(events[1]?.timeline_id, "turn-run-123:item:assistant:claude-msg-multi-text:text:1");
  for (const event of events) assertTankEventFixture(event);
});

test("adapter maps Claude AskUserQuestion to needs-input lifecycle", () => {
  const needsInputProviderItemIDs = new Set<string>();
  const requested = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-approval",
      message: {
        content: [
          {
            type: "tool_use",
            id: "toolu_ask",
            name: "AskUserQuestion",
            input: { question: "Proceed?" },
          },
        ],
      },
    },
    needsInputProviderItemIDs,
  );

  assert.deepEqual(requested.map((event) => event.type), [
    "item.started",
    "tool.approval_requested",
  ]);
  for (const event of requested) assertTankEventFixture(event);
  assert.equal(needsInputProviderItemIDs.has("toolu_ask"), true);

  const resolved = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "user",
      uuid: "claude-tool-result",
      message: {
        content: [
          {
            type: "tool_result",
            tool_use_id: "toolu_ask",
            content: "yes",
            is_error: false,
          },
        ],
      },
    },
    needsInputProviderItemIDs,
  );

  assert.deepEqual(resolved.map((event) => event.type), [
    "item.completed",
    "tool.approval_resolved",
  ]);
  for (const event of resolved) assertTankEventFixture(event);
  assert.equal(needsInputProviderItemIDs.has("toolu_ask"), false);
});

test("adapter maps Claude result failures and interrupts to terminal turn events", () => {
  const failed = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "result",
      subtype: "error",
      result: "provider failed",
      uuid: "claude-result-failed",
    },
    new Set<string>(),
  );
  assert.equal(failed.length, 1);
  assertTankEventFixture(failed[0]!);
  assert.equal(failed[0]?.type, "turn.failed");
  assert.equal(failed[0]?.payload?.reason, "provider_failure");
  assert.equal(failed[0]?.payload?.error, "provider failed");

  const interrupted = canonicalEventsForClaudeMessage(
    cfg(),
    turn({ interrupted: true }),
    {
      type: "result",
      subtype: "success",
      result: "stopped",
      uuid: "claude-result-interrupted",
    },
    new Set<string>(),
  );
  assert.equal(interrupted.length, 1);
  assertTankEventFixture(interrupted[0]!);
  assert.equal(interrupted[0]?.type, "turn.interrupted");
  assert.equal(interrupted[0]?.payload?.reason, "client_interrupt");
});
