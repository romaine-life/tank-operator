// Pins the producer-contract invariants that the SPA depends on
// (one serialization → two sinks, DB-first ordering, ws skipped on
// cosmos failure). Drift in any of these silently breaks the
// history-replay / live join in RunPaneSDK.

import { test } from "node:test";
import assert from "node:assert/strict";

import { dispatch, dispatchCreate } from "./runner.js";
import { isCanonical } from "./cosmos.js";
import { isTankConversationEvent, userSubmissionEvents, type TankConversationEvent } from "./conversation.js";

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
  assertStampedTankEvent(cosmosMessage);
  assertStampedTankEvent(wsMessage);
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
