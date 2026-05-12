// Pins the producer-contract invariants the SPA depends on: one
// serialization → two sinks, DB-first ordering, WS skipped on Cosmos
// failure. Same shape as agent-runner/src/runner.test.ts — different
// event types underneath.

import { test } from "node:test";
import assert from "node:assert/strict";

import { dispatch } from "./runner.js";
import type { CodexEvent } from "./cosmos.js";

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

test("canonical: cosmos before ws (read-your-writes ordering)", async () => {
  const order: Order = [];
  const ok = await dispatch(makeSink(order), makeWS(order), {
    type: "item.completed",
    item: { id: "i1", type: "agent_message" },
  } as CodexEvent);
  assert.equal(ok, true);
  assert.deepEqual(order, ["cosmos", "ws"]);
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
