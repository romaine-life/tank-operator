import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  dispatch,
  interruptTargetMatchesTurn,
  Runner,
  takePendingInterruptForTurn,
} from "./runner.js";
import {
  isDurableTankConversationEvent,
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../runner-shared/conversation.js";
import {
  stampTankEvent,
  turnEvent,
  userSubmissionEvents,
} from "../../runner-shared/conversation-builders.js";

type Order = string[];

function makeSink(order: Order, opts: { throws?: Error } = {}) {
  return {
    async upsert() {
      if (opts.throws) throw opts.throws;
      order.push("sink");
    },
  };
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
  };
}

const fixturesPath = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../schemas/tank-conversation-event.fixtures.json",
);
const fixtures: { events: { name: string; event: TankConversationEvent }[] } = JSON.parse(
  readFileSync(fixturesPath, "utf8"),
);

test("dispatch publishes a built Tank event and stamps order metadata", async () => {
  const order: Order = [];
  let sinkEvent: TankConversationEvent | undefined;
  const sink = {
    async upsert(event: TankConversationEvent & { uuid: string; order_key: string }) {
      sinkEvent = event;
      order.push("sink");
    },
  };
  const built = turnEvent({
    sessionID: "63",
    turnID: "turn_run-123",
    clientNonce: "run-123",
    source: "codex",
    type: "turn.completed",
  });
  const ok = await dispatch(sink, built);
  assert.equal(ok, true);
  assert.deepEqual(order, ["sink"]);
  assert.ok(sinkEvent);
  assert.equal(typeof sinkEvent!.uuid, "string");
  assert.equal(typeof sinkEvent!.order_key, "string");
  assert.equal(typeof sinkEvent!.written_at, "string");
  assert.equal(typeof sinkEvent!.sequence, "number");
});

test("dispatch refuses to publish events the persister would reject", async () => {
  // A built event missing required envelope fields cannot reach the sink:
  // stampTankEvent throws so the malformed event never goes to NATS. This
  // matches the persister's ValidateEventMap rejection, by design.
  const order: Order = [];
  const sink = {
    async upsert() {
      order.push("sink");
    },
  };
  await assert.rejects(
    () => dispatch(sink, { type: "error", message: "boom" } as unknown as TankConversationEvent),
    /event_id is required/,
  );
  assert.deepEqual(order, []);
});

test("dispatch drops live-only Tank events without persisting", async () => {
  const order: Order = [];
  const sink = {
    async upsert() {
      order.push("sink");
    },
  };
  const liveOnly: TankConversationEvent = {
    event_id: "turn_run-123:item.delta:tool-1:p1",
    session_id: "63",
    turn_id: "turn_run-123",
    timeline_id: "tool-1",
    actor: "tool",
    source: "codex",
    type: "item.delta",
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "live-only",
    payload: { kind: "tool" },
  };
  const ok = await dispatch(sink, liveOnly);
  assert.equal(ok, true);
  assert.deepEqual(order, [], "live-only events must not reach the durable sink");
});

test("dispatch reports failure when the sink throws", async () => {
  const ok = await dispatch(
    makeSink([], { throws: new Error("boom") }),
    turnEvent({
      sessionID: "63",
      turnID: "turn_run-123",
      clientNonce: "run-123",
      source: "codex",
      type: "turn.completed",
    }),
  );
  assert.equal(ok, false);
});

test("stampTankEvent throws when envelope fields are missing", () => {
  assert.throws(
    () => stampTankEvent({ type: "user_message.created" } as unknown as TankConversationEvent),
    /event_id is required/,
  );
});

test("durable Tank fixtures pass the shared filter and dispatch end-to-end", async () => {
  for (const { name, event } of fixtures.events) {
    if (event.visibility === "live-only") continue;
    const order: Order = [];
    const sink = makeSink(order);
    const stamped = stampTankEvent(event);
    if (!isDurableTankConversationEvent(stamped)) {
      assert.fail(`${name}: stamped fixture should satisfy isDurableTankConversationEvent`);
    }
    const ok = await dispatch(sink, event);
    assert.equal(ok, true, `${name}: dispatch should succeed`);
    assert.deepEqual(order, ["sink"], `${name}: should reach sink`);
  }
});

test("userSubmissionEvents produces Tank-shape boundary events", () => {
  const { userMessage, turnSubmitted } = userSubmissionEvents({
    sessionID: "63",
    clientNonce: "run-123",
    text: "hello",
    message: { role: "user", content: "hello" },
    runtime: "codex",
    now: "2026-05-12T00:00:00.000Z",
  });
  for (const event of [stampTankEvent(userMessage), stampTankEvent(turnSubmitted)]) {
    assert.equal(isTankConversationEvent(event), true);
  }
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

test("pending Codex interrupts are consumed when their turn becomes current", () => {
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

test("terminal Codex interrupts ack submit and interrupt commands after publish", async () => {
  type Record = { kind?: string };
  const runner = new Runner(runnerConfig()) as unknown as {
    commandBus: { markCompleted: (record: Record) => Promise<void>; markFailed: (record: Record) => Promise<void> };
    markCommandTerminal: (turn: unknown, type: string) => Promise<void>;
  };
  const calls: string[] = [];
  runner.commandBus = {
    async markCompleted(record: Record) {
      calls.push(`ack:${record.kind}`);
    },
    async markFailed(record: Record) {
      calls.push(`fail:${record.kind}`);
    },
  };

  const turn = {
    commandRecord: { kind: "submit" },
    interruptRecords: [{ kind: "interrupt" }],
    stopCommandHeartbeat: () => calls.push("stop-heartbeat"),
  };

  await runner.markCommandTerminal(turn, "turn.interrupted");

  assert.deepEqual(calls, ["stop-heartbeat", "ack:submit", "ack:interrupt"]);
  assert.equal(turn.commandRecord, undefined);
  assert.equal(turn.interruptRecords, undefined);
  assert.equal(turn.stopCommandHeartbeat, undefined);
});
