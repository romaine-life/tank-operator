import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  buildInputReplyMessage,
  dispatch,
  inputReplyTargetProviderItemID,
  inputReplyText,
  Runner,
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
    mcpConfig: "/workspace/.mcp.json",
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
    source: "claude",
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
  const order: Order = [];
  const sink = {
    async upsert() {
      order.push("sink");
    },
  };
  await assert.rejects(
    () => dispatch(sink, { type: "assistant" } as unknown as TankConversationEvent),
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
    source: "claude",
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
      source: "claude",
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
    runtime: "claude",
    now: "2026-05-12T00:00:00.000Z",
  });
  for (const event of [stampTankEvent(userMessage), stampTankEvent(turnSubmitted)]) {
    assert.equal(isTankConversationEvent(event), true);
  }
});

test("input reply records map to Claude tool_result messages", () => {
  const message = buildInputReplyMessage("toolu_ask", "Continue");

  assert.equal((message as { type?: string }).type, "user");
  assert.equal((message as { parent_tool_use_id?: string }).parent_tool_use_id, "toolu_ask");
  assert.deepEqual((message as { message?: { content: unknown[] } }).message?.content, [
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
  };

  assert.equal(inputReplyTargetProviderItemID(record as never), "toolu_ask");
  assert.equal(inputReplyText(record as never), "Continue");
  assert.equal(inputReplyText({ ...record, input_reply: "" } as never), "");
});

test("terminal turn failures ack the durable submit command", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    commandBus: { markCompleted: () => Promise<void>; markFailed: () => Promise<void> };
    markCommandTerminal: (turn: unknown, type: string) => Promise<void>;
  };
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
