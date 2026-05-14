import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildInputReplyMessage,
  dispatch,
  dispatchCreate,
  inputReplyTargetProviderItemID,
  inputReplyText,
} from "./runner.js";
import { isCanonical } from "./cosmos.js";
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
      order.push("cosmos");
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

test("canonical events are written to Cosmos", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), { type: "assistant", uuid: "x" } as any);
  assert.equal(ok, true);
  assert.deepEqual(order, ["cosmos"]);
});

test("dispatch stamps Tank ordering metadata before the Cosmos write", async () => {
  let cosmosMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        cosmosMessage = message;
      },
    },
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, true);
  assert.equal(typeof cosmosMessage.written_at, "string");
});

test("tank.user_message is canonical so Claude replay preserves user bubbles", () => {
  assert.equal(
    isCanonical({ type: "tank.user_message", message: "hello" }),
    true,
  );
});

test("dispatch assigns a uuid to Tank-owned user messages", async () => {
  let cosmosMessage: any;
  const ok = await dispatch(
    {
      async upsert(message) {
        cosmosMessage = message;
      },
    },
    { type: "tank.user_message", message: "hello" },
  );
  assert.equal(ok, true);
  assert.equal(typeof cosmosMessage.uuid, "string");
});

test("dispatchCreate uses event_id as the durable id for canonical Tank events", async () => {
  let cosmosMessage: any;
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
    userMessageEvent(),
  );
  assert.equal(ok, "created");
  assert.equal(cosmosMessage.uuid, "turn_run-123:user_message.created");
  assert.equal(cosmosMessage.event_id, cosmosMessage.uuid);
  assert.equal(typeof cosmosMessage.order_key, "string");
  assert.equal(typeof cosmosMessage.sequence, "number");
  assertStampedTankEvent(cosmosMessage);
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

test("canonical Cosmos failure returns false", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order, { throws: new Error("boom") }),
    { type: "assistant", uuid: "x" } as any,
  );
  assert.equal(ok, false);
  assert.deepEqual(order, []);
});

test("non-canonical stream_event is not written to Cosmos", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), { type: "stream_event" } as any);
  assert.equal(ok, true);
  assert.deepEqual(order, []);
});

test("system without subtype is not canonical", async () => {
  let cosmosCalled = false;
  const sink = {
    async upsert() {
      cosmosCalled = true;
    },
  };
  await dispatch(sink, { type: "system" } as any);
  assert.equal(cosmosCalled, false);
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
