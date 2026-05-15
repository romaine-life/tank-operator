import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildInputReplyMessage,
  dispatch,
  dispatchCreate,
  inputReplyTargetProviderItemID,
  inputReplyText,
  Runner,
} from "./runner.js";
import { isCanonical } from "./sessionEvents.js";
import {
  isTankConversationEvent,
  userSubmissionEvents,
  type TankConversationEvent,
} from "./conversation.js";

type Order = string[];

function makeSink(order: Order, opts: { throws?: Error } = {}) {
  return {
    async upsert() {
      if (opts.throws) throw opts.throws;
      order.push("sink");
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

function runnerConfig() {
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

function assertTankEventFixture(event: TankConversationEvent, label = event.type) {
  assert.equal(isTankConversationEvent(event), true, `${label} should satisfy the Tank envelope`);
}

function assertStampedTankEvent(event: TankConversationEvent & { order_key?: unknown; sequence?: unknown }) {
  assertTankEventFixture(event);
  assert.equal(
    typeof event.order_key === "string" || typeof event.sequence === "number",
    true,
    `${event.type} should have a replay order cursor`,
  );
}

test("canonical events are written to event ledger", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), { type: "assistant", uuid: "x" } as any);
  assert.equal(ok, true);
  assert.deepEqual(order, ["sink"]);
});

test("dispatch stamps Tank ordering metadata before the session-bus publish", async () => {
  let sinkMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        sinkMessage = message;
      },
    },
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, true);
  assert.equal(typeof sinkMessage.written_at, "string");
});

test("tank.user_message is canonical so Claude replay preserves user bubbles", () => {
  assert.equal(
    isCanonical({ type: "tank.user_message", message: "hello" }),
    true,
  );
});

test("dispatch assigns a uuid to Tank-owned user messages", async () => {
  let sinkMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        sinkMessage = message;
      },
    },
    { type: "tank.user_message", message: "hello" },
  );
  assert.equal(ok, true);
  assert.equal(typeof sinkMessage.uuid, "string");
});

test("dispatchCreate uses event_id as the durable id for canonical Tank events", async () => {
  let sinkMessage: any;
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for create path");
      },
      async create(message) {
        sinkMessage = message;
        return "created";
      },
    },
    userMessageEvent(),
  );
  assert.equal(ok, "created");
  assert.equal(sinkMessage.uuid, "turn_run-123:user_message.created");
  assert.equal(sinkMessage.event_id, sinkMessage.uuid);
  assert.equal(typeof sinkMessage.order_key, "string");
  assert.equal(typeof sinkMessage.sequence, "number");
  assertStampedTankEvent(sinkMessage);
});

test("dispatchCreate suppresses duplicate client_nonce submissions", async () => {
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for create path");
      },
      async create() {
        return "exists";
      },
    },
    userMessageEvent(),
  );
  assert.equal(ok, "exists");
});

test("dispatchCreate rejects malformed Tank-owned events before create", async () => {
  let createCalled = false;
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for malformed event");
      },
      async create() {
        createCalled = true;
        return "created";
      },
    },
    {
      ...userMessageEvent(),
      payload: { text: "hello" },
    },
  );
  assert.equal(ok, "failed");
  assert.equal(createCalled, false);
});

test("canonical session-bus publish failure returns false", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order, { throws: new Error("boom") }),
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, false);
  assert.deepEqual(order, []);
});

test("non-canonical stream_event is not written to event ledger", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), { type: "stream_event" } as any);
  assert.equal(ok, true);
  assert.deepEqual(order, []);
});

test("system without subtype is not canonical", async () => {
  let sinkCalled = false;
  const sink = {
    async upsert() {
      sinkCalled = true;
    },
  };
  await dispatch(sink, { type: "system" } as any);
  assert.equal(sinkCalled, false);
});

test("input reply records map to Claude tool_result messages", () => {
  const message = buildInputReplyMessage("toolu_ask", "Continue");

  assert.equal((message as any).type, "user");
  assert.equal((message as any).parent_tool_use_id, "toolu_ask");
  assert.deepEqual((message as any).message.content, [
    {
      type: "tool_result",
      tool_use_id: "toolu_ask",
      content: "Continue",
    },
  ]);
});

test("input reply record helpers trim durable control fields", () => {
  const record = {
    target_provider_item_id: " toolu_ask ",
    input_reply: " Continue ",
    prompt: "fallback",
  } as any;

  assert.equal(inputReplyTargetProviderItemID(record), "toolu_ask");
  assert.equal(inputReplyText(record), "Continue");
  assert.equal(inputReplyText({ ...record, input_reply: "" } as any), "");
});

test("terminal turn failures ack the durable submit command", async () => {
  const runner = new Runner(runnerConfig()) as any;
  const calls: string[] = [];
  runner.commandBus = {
    async markCompleted() {
      calls.push("ack");
    },
    async markFailed() {
      calls.push("fail");
    },
  };

  const turn = {
    commandRecord: {},
    stopCommandHeartbeat: () => calls.push("stop-heartbeat"),
  };

  await runner.markCommandTerminal(turn, "turn.failed");

  assert.deepEqual(calls, ["stop-heartbeat", "ack"]);
  assert.equal(turn.commandRecord, undefined);
  assert.equal(turn.stopCommandHeartbeat, undefined);
});
