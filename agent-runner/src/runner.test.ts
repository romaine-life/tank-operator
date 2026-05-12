// Pins the producer-contract invariants that the SPA depends on
// (one serialization → two sinks, DB-first ordering, ws skipped on
// cosmos failure). Drift in any of these silently breaks the
// history-replay / live join in RunPaneSDK.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  canonicalEventsForClaudeMessage,
  dispatch,
  dispatchCreate,
  type PendingTurn,
} from "./runner.js";
import { isCanonical } from "./cosmos.js";
import { userSubmissionEvents } from "./conversation.js";
import type { Config } from "./config.js";

type Order = string[];
function makeSink(order: Order, opts: { throws?: Error } = {}) {
  return {
    async upsert() {
      if (opts.throws) throw opts.throws;
      order.push("cosmos");
    },
  };
}
function makeWS(order: Order) {
  return {
    broadcastEvent() {
      order.push("ws");
    },
  };
}

function userMessageEvent() {
  return userSubmissionEvents({
    sessionID: "63",
    clientNonce: "run-123",
    text: "hello",
    message: { role: "user", content: "hello" },
    runtime: "claude",
    now: "2026-05-12T00:00:00.000Z",
  }).userMessage;
}

function pendingTurn(fields: Partial<PendingTurn> = {}): PendingTurn {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
    ...fields,
  };
}

function cfg(): Config {
  return {
    sessionId: "63",
    ownerEmail: "user@example.com",
    cosmosEndpoint: "https://example.invalid",
    cosmosDatabase: "tank-operator",
    sessionEventsContainer: "session-events",
    workspace: "/workspace",
    mcpConfig: "/workspace/.mcp.json",
    wsPort: 8090,
  };
}

test("canonical: cosmos before ws (read-your-writes ordering)", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order),
    makeWS(order),
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, true);
  assert.deepEqual(order, ["cosmos", "ws"]);
});

test("dispatch stamps same tank ordering metadata to cosmos and ws", async () => {
  let cosmosMessage: any;
  let wsMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        cosmosMessage = message;
      },
    },
    {
      broadcastEvent(message) {
        wsMessage = message;
      },
    },
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, true);
  assert.equal(cosmosMessage.tank_event_seq, wsMessage.tank_event_seq);
  assert.equal(cosmosMessage.tank_order_key, wsMessage.tank_order_key);
  assert.equal(cosmosMessage.written_at, wsMessage.written_at);
});

test("tank.user_message is canonical so Claude replay preserves user bubbles", () => {
  assert.equal(
    isCanonical({ type: "tank.user_message", message: "hello" }),
    true,
  );
});

test("dispatch assigns a shared uuid to Tank-owned user messages", async () => {
  let cosmosMessage: any;
  let wsMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        cosmosMessage = message;
      },
    },
    {
      broadcastEvent(message) {
        wsMessage = message;
      },
    },
    { type: "tank.user_message", message: "hello" },
  );
  assert.equal(ok, true);
  assert.equal(typeof cosmosMessage.uuid, "string");
  assert.equal(cosmosMessage.uuid, wsMessage.uuid);
  assert.equal(cosmosMessage.tank_order_key, wsMessage.tank_order_key);
});

test("dispatchCreate uses event_id as the durable id for canonical Tank events", async () => {
  let cosmosMessage: any;
  let wsMessage: any;
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for create path");
      },
      async create(message) {
        cosmosMessage = message;
        return "created";
      },
    },
    {
      broadcastEvent(message) {
        wsMessage = message;
      },
    },
    userMessageEvent(),
  );
  assert.equal(ok, "created");
  assert.equal(cosmosMessage.uuid, "turn_run-123:user_message.created");
  assert.equal(cosmosMessage.event_id, cosmosMessage.uuid);
  assert.equal(cosmosMessage.order_key, cosmosMessage.tank_order_key);
  assert.equal(cosmosMessage.sequence, cosmosMessage.tank_event_seq);
  assert.equal(wsMessage.uuid, cosmosMessage.uuid);
});

test("dispatchCreate suppresses duplicate client_nonce submissions", async () => {
  let broadcasted = false;
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for create path");
      },
      async create() {
        return "exists";
      },
    },
    {
      broadcastEvent() {
        broadcasted = true;
      },
    },
    userMessageEvent(),
  );
  assert.equal(ok, "exists");
  assert.equal(broadcasted, false);
});

test("canonical: cosmos failure suppresses ws broadcast", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order, { throws: new Error("boom") }),
    makeWS(order),
    { type: "assistant", uuid: "x" } as any,
  );
  // The SPA must never see a live event that wasn't persisted — otherwise
  // history-replay on the next reload would diverge from what was rendered.
  assert.equal(ok, false);
  assert.deepEqual(order, []);
});

test("live-only: stream_event broadcast skips cosmos entirely", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order),
    makeWS(order),
    { type: "stream_event" } as any,
  );
  assert.equal(ok, true);
  // No "cosmos" entry — the live-only types (typewriter deltas, status
  // pings, hook lifecycle) deliberately stay out of the durable log.
  assert.deepEqual(order, ["ws"]);
});

test("live-only: cosmos is never even called for non-canonical", async () => {
  let cosmosCalled = false;
  const ws = { broadcastEvent() {} };
  const sink = {
    async upsert() {
      cosmosCalled = true;
    },
  };
  await dispatch(sink, ws, { type: "system", subtype: "status" } as any);
  assert.equal(cosmosCalled, false);
});

test("system without subtype: not canonical (defensive default)", async () => {
  let cosmosCalled = false;
  let broadcasted = false;
  const sink = {
    async upsert() {
      cosmosCalled = true;
    },
  };
  const ws = {
    broadcastEvent() {
      broadcasted = true;
    },
  };
  // Unknown system events with no subtype shouldn't be persisted — better
  // to silently drop than to pollute the canonical stream with junk a
  // future SDK release may have meant as transient.
  await dispatch(sink, ws, { type: "system" } as any);
  assert.equal(cosmosCalled, false);
  assert.equal(broadcasted, true);
});

test("adapter maps Claude assistant text and tool_use blocks to Tank items", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    pendingTurn(),
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
  assert.equal(events[0]?.actor, "assistant");
  assert.equal(events[0]?.payload?.kind, "message");
  assert.equal(events[0]?.payload?.text, "I will inspect the workspace.");
  assert.equal(events[1]?.actor, "tool");
  assert.equal(events[1]?.item_id, "toolu_read");
  assert.equal(events[1]?.payload?.title, "Read");
});

test("adapter maps Claude AskUserQuestion to needs-input lifecycle", () => {
  const needsInputItemIDs = new Set<string>();
  const requested = canonicalEventsForClaudeMessage(
    cfg(),
    pendingTurn(),
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
    needsInputItemIDs,
  );

  assert.deepEqual(requested.map((event) => event.type), [
    "item.started",
    "tool.approval_requested",
  ]);
  assert.equal(needsInputItemIDs.has("toolu_ask"), true);

  const resolved = canonicalEventsForClaudeMessage(
    cfg(),
    pendingTurn(),
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
    needsInputItemIDs,
  );

  assert.deepEqual(resolved.map((event) => event.type), [
    "item.completed",
    "tool.approval_resolved",
  ]);
  assert.equal(needsInputItemIDs.has("toolu_ask"), false);
});

test("adapter maps Claude result failures and interrupts to terminal turn events", () => {
  const failed = canonicalEventsForClaudeMessage(
    cfg(),
    pendingTurn(),
    {
      type: "result",
      subtype: "error",
      result: "provider failed",
      uuid: "claude-result-failed",
    },
    new Set<string>(),
  );
  assert.equal(failed.length, 1);
  assert.equal(failed[0]?.type, "turn.failed");
  assert.equal(failed[0]?.payload?.reason, "provider_failure");
  assert.equal(failed[0]?.payload?.error, "provider failed");

  const interrupted = canonicalEventsForClaudeMessage(
    cfg(),
    pendingTurn({ interrupted: true }),
    {
      type: "result",
      subtype: "success",
      result: "stopped",
      uuid: "claude-result-interrupted",
    },
    new Set<string>(),
  );
  assert.equal(interrupted.length, 1);
  assert.equal(interrupted[0]?.type, "turn.interrupted");
  assert.equal(interrupted[0]?.payload?.reason, "client_interrupt");
});
