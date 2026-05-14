// Pins the producer-contract invariants the SPA depends on: one
// serialization → two sinks, DB-first ordering, WS skipped on Cosmos
// failure. Same shape as agent-runner/src/runner.test.ts — different
// event types underneath.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  dispatch,
  dispatchCreate,
} from "./runner.js";
import { isCanonical, nextSortableEventID, type CodexEvent } from "./cosmos.js";
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
    runtime: "codex",
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
  const ok = await dispatch(makeSink(order), makeWS(order), {
    type: "item.completed",
    item: { id: "i1", type: "agent_message" },
  } as CodexEvent);
  assert.equal(ok, true);
  assert.deepEqual(order, ["cosmos", "ws"]);
});

test("dispatch stamps same tank ordering metadata to cosmos and ws", async () => {
  let cosmosEvent: any;
  let wsEvent: any;
  const ok = await dispatch(
    {
      async upsert(event) {
        cosmosEvent = event;
      },
    },
    {
      broadcastEvent(event) {
        wsEvent = event;
      },
    },
    { type: "item.completed", item: { id: "i1" } } as CodexEvent,
  );
  assert.equal(ok, true);
  assert.equal(cosmosEvent.uuid, wsEvent.uuid);
  assert.equal(cosmosEvent.tank_event_seq, wsEvent.tank_event_seq);
  assert.equal(cosmosEvent.tank_order_key, wsEvent.tank_order_key);
  assert.equal(cosmosEvent.written_at, wsEvent.written_at);
});

test("canonical: cosmos failure suppresses ws broadcast", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order, { throws: new Error("boom") }),
    makeWS(order),
    { type: "item.completed", item: { id: "i1" } } as CodexEvent,
  );
  // SPA must never see a live event that wasn't persisted, or history
  // replay diverges from what was rendered.
  assert.equal(ok, false);
  assert.deepEqual(order, []);
});

test("dispatchCreate uses event_id as the durable id for canonical Tank events", async () => {
  let cosmosEvent: any;
  let wsEvent: any;
  const ok = await dispatchCreate(
    {
      async upsert() {
        throw new Error("upsert should not be used for create path");
      },
      async create(event) {
        cosmosEvent = event;
        return "created";
      },
    },
    {
      broadcastEvent(event) {
        wsEvent = event;
      },
    },
    userMessageEvent(),
  );
  assert.equal(ok, "created");
  assert.equal(cosmosEvent.uuid, "turn_run-123:user_message.created");
  assert.equal(cosmosEvent.event_id, cosmosEvent.uuid);
  assert.equal(cosmosEvent.order_key, cosmosEvent.tank_order_key);
  assert.equal(cosmosEvent.sequence, cosmosEvent.tank_event_seq);
  assert.equal(wsEvent.uuid, cosmosEvent.uuid);
  assertStampedTankEvent(cosmosEvent);
  assertStampedTankEvent(wsEvent);
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

test("dispatchCreate rejects malformed Tank-owned events before fanout", async () => {
  let createCalled = false;
  let broadcasted = false;
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
      broadcastEvent() {
        broadcasted = true;
      },
    },
    {
      ...userMessageEvent(),
      payload: { text: "hello" },
    },
  );
  assert.equal(ok, "failed");
  assert.equal(createCalled, false);
  assert.equal(broadcasted, false);
});

test("live-only: turn.started broadcasts without a cosmos write", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order),
    makeWS(order),
    { type: "turn.started" } as CodexEvent,
  );
  assert.equal(ok, true);
  // No "cosmos" entry — structural markers stay out of the durable log.
  assert.deepEqual(order, ["ws"]);
});

test("live-only: item.started + item.updated bypass cosmos entirely", async () => {
  for (const type of ["item.started", "item.updated"]) {
    let cosmosCalled = false;
    const ws = { broadcastEvent() {} };
    const sink = {
      async upsert() {
        cosmosCalled = true;
      },
    };
    await dispatch(sink, ws, { type, item: {} } as CodexEvent);
    assert.equal(cosmosCalled, false, `${type} should not write to cosmos`);
  }
});

test("error events ARE canonical (durable error log)", async () => {
  const order: Order = [];
  await dispatch(
    makeSink(order),
    makeWS(order),
    { type: "error", message: "x" } as CodexEvent,
  );
  assert.deepEqual(order, ["cosmos", "ws"]);
});

test("tank.user_message is canonical so replay preserves user bubbles", () => {
  assert.equal(
    isCanonical({ type: "tank.user_message", message: "hello" }),
    true,
  );
});

test("generated event ids sort by production order", () => {
  const first = nextSortableEventID(1000);
  const second = nextSortableEventID(1000);
  const third = nextSortableEventID(1001);
  assert.deepEqual([third, first, second].sort(), [first, second, third]);
});
