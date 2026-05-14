import { test } from "node:test";
import assert from "node:assert/strict";

import {
  dispatch,
  dispatchCreate,
  interruptTargetMatchesTurn,
  takePendingInterruptForTurn,
} from "./runner.js";
import { isCanonical, nextSortableEventID, type CodexEvent } from "./cosmos.js";
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

test("canonical events are written to Cosmos", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), {
    type: "item.completed",
    item: { id: "i1", type: "agent_message" },
  } as CodexEvent);
  assert.equal(ok, true);
  assert.deepEqual(order, ["cosmos"]);
});

test("dispatch stamps Tank ordering metadata before the Cosmos write", async () => {
  let cosmosEvent: any;
  const ok = await dispatch(
    {
      async upsert(event) {
        cosmosEvent = event;
      },
    },
    { type: "item.completed", item: { id: "i1" } } as CodexEvent,
  );
  assert.equal(ok, true);
  assert.equal(typeof cosmosEvent.uuid, "string");
  assert.equal(typeof cosmosEvent.written_at, "string");
});

test("canonical Cosmos failure returns false", async () => {
  const order: Order = [];
  const ok = await dispatch(
    makeSink(order, { throws: new Error("boom") }),
    { type: "item.completed", item: { id: "i1" } } as CodexEvent,
  );
  assert.equal(ok, false);
  assert.deepEqual(order, []);
});

test("dispatchCreate uses event_id as the durable id for canonical Tank events", async () => {
  let cosmosEvent: any;
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
    userMessageEvent(),
  );
  assert.equal(ok, "created");
  assert.equal(cosmosEvent.uuid, "turn_run-123:user_message.created");
  assert.equal(cosmosEvent.event_id, cosmosEvent.uuid);
  assert.equal(typeof cosmosEvent.order_key, "string");
  assert.equal(typeof cosmosEvent.sequence, "number");
  assertStampedTankEvent(cosmosEvent);
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

test("non-canonical turn.started is not written to Cosmos", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), { type: "turn.started" } as CodexEvent);
  assert.equal(ok, true);
  assert.deepEqual(order, []);
});

test("non-canonical item.started and item.updated bypass Cosmos", async () => {
  for (const type of ["item.started", "item.updated"]) {
    let cosmosCalled = false;
    const sink = {
      async upsert() {
        cosmosCalled = true;
      },
    };
    await dispatch(sink, { type, item: {} } as CodexEvent);
    assert.equal(cosmosCalled, false, `${type} should not write to cosmos`);
  }
});

test("error events ARE canonical", async () => {
  const order: Order = [];
  await dispatch(makeSink(order), { type: "error", message: "x" } as CodexEvent);
  assert.deepEqual(order, ["cosmos"]);
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

test("pending interrupt targets match either turn id or client nonce", () => {
  const turn = {
    turnID: "turn_client-123",
    clientNonce: "client-123",
  };

  assert.equal(interruptTargetMatchesTurn("turn_client-123", turn), true);
  assert.equal(interruptTargetMatchesTurn("client-123", turn), true);
  assert.equal(interruptTargetMatchesTurn("other-turn", turn), false);
});

test("queued Codex interrupts are consumed when their turn becomes current", () => {
  const pendingInterrupts = [
    { target_turn_id: "client-123", client_nonce: "client-123" },
    { target_turn_id: "client-other", client_nonce: "client-other" },
  ];
  const turn = {
    turnID: "turn_client-123",
    clientNonce: "client-123",
  };

  assert.deepEqual(takePendingInterruptForTurn(pendingInterrupts, turn), {
    target_turn_id: "client-123",
    client_nonce: "client-123",
  });
  assert.deepEqual(pendingInterrupts, [
    { target_turn_id: "client-other", client_nonce: "client-other" },
  ]);
  assert.equal(takePendingInterruptForTurn(pendingInterrupts, turn), null);
});
