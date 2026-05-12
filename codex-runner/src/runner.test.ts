// Pins the producer-contract invariants the SPA depends on: one
// serialization → two sinks, DB-first ordering, WS skipped on Cosmos
// failure. Same shape as agent-runner/src/runner.test.ts — different
// event types underneath.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  canonicalEventsForCodexEvent,
  dispatch,
  dispatchCreate,
  type AcceptedTurn,
} from "./runner.js";
import { isCanonical, nextSortableEventID, type CodexEvent } from "./cosmos.js";
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
    runtime: "codex",
    now: "2026-05-12T00:00:00.000Z",
  }).userMessage;
}

function acceptedTurn(fields: Partial<AcceptedTurn> = {}): AcceptedTurn {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    turnSeq: 7,
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
    wsPort: 8090,
  };
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

test("adapter maps Codex agent messages to durable Tank assistant items", () => {
  const events = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    {
      type: "item.completed",
      item: {
        id: "item_agent_1",
        type: "agent_message",
        text: "All tests passed.",
      },
    },
  );

  assert.equal(events.length, 1);
  assert.equal(events[0]?.type, "item.completed");
  assert.equal(events[0]?.source, "codex");
  assert.equal(events[0]?.actor, "assistant");
  assert.equal(events[0]?.item_id, "item_agent_1");
  assert.equal(events[0]?.visibility, "durable");
  assert.equal(events[0]?.payload?.kind, "agent_message");
  assert.equal(events[0]?.payload?.text, "All tests passed.");
});

test("adapter maps Codex item updates to live-only Tank deltas", () => {
  const events = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    {
      type: "item.updated",
      item: {
        id: "item_reasoning_1",
        type: "reasoning",
        text: "thinking",
      },
    },
  );

  assert.equal(events.length, 1);
  assert.equal(events[0]?.type, "item.delta");
  assert.equal(events[0]?.actor, "assistant");
  assert.equal(events[0]?.visibility, "live-only");
});

test("adapter maps Codex terminal events to Tank turn lifecycle", () => {
  const completed = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    { type: "turn.completed", usage: { input_tokens: 10 } },
  );
  assert.equal(completed.length, 1);
  assert.equal(completed[0]?.type, "turn.completed");
  assert.deepEqual(completed[0]?.payload?.usage, { input_tokens: 10 });

  const failed = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    { type: "error", message: "quota exceeded" },
  );
  assert.equal(failed.length, 1);
  assert.equal(failed[0]?.type, "turn.failed");
  assert.equal(failed[0]?.payload?.reason, "provider_failure");
  assert.equal(failed[0]?.payload?.error, "quota exceeded");
});

test("adapter explicitly ignores unknown Codex provider event types", () => {
  const events = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    { type: "future.experimental.event", value: true },
  );
  assert.deepEqual(events, []);
});
