import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  dispatch,
  inputReplyAnnotations,
  inputReplyAnswers,
  inputReplyTargetProviderItemID,
  joinAnswersForSDK,
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

test("inputReplyAnswers parses durable command answers map and drops empties", () => {
  const record = {
    target_provider_item_id: " toolu_ask ",
    answers: {
      "  Which auth method?  ": ["  OAuth  ", ""],
      "  ": ["ignored"],
      "Drop me": [],
    },
  };

  assert.equal(inputReplyTargetProviderItemID(record as never), "toolu_ask");
  assert.deepEqual(inputReplyAnswers(record as never), { "Which auth method?": ["OAuth"] });
});

test("inputReplyAnswers tolerates a missing or malformed answers field", () => {
  assert.deepEqual(inputReplyAnswers({} as never), {});
  assert.deepEqual(inputReplyAnswers({ answers: null } as never), {});
  assert.deepEqual(inputReplyAnswers({ answers: "not-a-map" } as never), {});
  assert.deepEqual(inputReplyAnswers({ answers: [] } as never), {});
});

test("inputReplyAnnotations trims preview and notes, drops empty entries", () => {
  const record = {
    annotations: {
      "Q1": { preview: "  <div/>  ", notes: "  hi  " },
      "Q2": { notes: "" },
      "  ": { preview: "x" },
    },
  };
  assert.deepEqual(inputReplyAnnotations(record as never), {
    Q1: { preview: "<div/>", notes: "hi" },
  });
});

test("joinAnswersForSDK joins multi-select arrays with the SDK preprocess separator", () => {
  assert.deepEqual(
    joinAnswersForSDK({
      "Single": ["OAuth"],
      "Multi": ["A", "B", "C"],
    }),
    {
      Single: "OAuth",
      Multi: "A, B, C",
    },
  );
});

// Regression test for the "Stop doesn't interrupt deep tool-use loops"
// failure mode that PR #481's durable-stop migration left open. Before
// the data/control plane split, both submit_turn and interrupt_turn rode
// the same JetStream subject through a single consumer with
// max_ack_pending=1: while submit_turn was in-flight (the runner held
// the message via working() heartbeats for the full duration of the
// turn), the consumer would not deliver interrupt_turn at all. The fix
// runs two consumers — one per plane — so an interrupt arrives on its
// own subscription regardless of the data-plane consumer's slot state.
//
// This test pins the shape directly: stub both consumer-registration
// methods on the bus, simulate "data handler never gets invoked"
// (in-flight submit blocking the data plane), invoke the control
// handler with an interrupt record, and assert acceptInterrupt fires.
// If a future refactor folds the planes back together, the control
// handler will be the same callable as the data handler and the test
// will fail loudly instead of leaving the regression silent.
test("dispatchInterruptIndependentlyOfSubmit: control handler dispatches interrupts without waiting for the data plane", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    commandBus: {
      startCommandConsumer: (h: (r: unknown) => Promise<void>, s?: AbortSignal) => Promise<() => Promise<void>>;
      startControlConsumer: (h: (r: unknown) => Promise<void>, s?: AbortSignal) => Promise<() => Promise<void>>;
      markCompleted: () => Promise<void>;
    };
    startCommandConsumer: (signal: AbortSignal) => () => void;
    startControlConsumer: (signal: AbortSignal) => () => void;
    acceptInterrupt: (record: unknown) => Promise<void>;
    acceptCommandTurn: (record: unknown) => Promise<void>;
    acceptInputReply: (record: unknown) => Promise<void>;
  };

  type RecordHandler = (record: unknown) => Promise<void>;
  const handlers: { data: RecordHandler | null; control: RecordHandler | null } = {
    data: null,
    control: null,
  };
  const calls: string[] = [];

  runner.commandBus = {
    async startCommandConsumer(h: RecordHandler) {
      handlers.data = h;
      return async () => {};
    },
    async startControlConsumer(h: RecordHandler) {
      handlers.control = h;
      return async () => {};
    },
    async markCompleted() {
      calls.push("ack");
    },
  };
  runner.acceptInterrupt = async () => {
    calls.push("acceptInterrupt");
  };
  runner.acceptCommandTurn = async () => {
    calls.push("acceptCommandTurn");
  };
  runner.acceptInputReply = async () => {
    calls.push("acceptInputReply");
  };

  const ctl = new AbortController();
  runner.startCommandConsumer(ctl.signal);
  runner.startControlConsumer(ctl.signal);
  // Yield so the .then() callbacks that capture the consumer handlers
  // get a chance to run before we read them.
  await new Promise((resolve) => setImmediate(resolve));

  const dataFn = handlers.data;
  const controlFn = handlers.control;
  if (!dataFn) throw new Error("startCommandConsumer should register a data handler");
  if (!controlFn) throw new Error("startControlConsumer should register a separate control handler");
  assert.notEqual(
    dataFn,
    controlFn,
    "data and control handlers must be distinct callables; folding them back together restores the regression",
  );

  // Simulate the regression environment: the data-plane consumer's slot
  // is held by an in-flight submit (working() heartbeats keep it
  // unacked), so dataFn is NOT invoked. We invoke the control handler
  // directly; the assertion is that acceptInterrupt fires without
  // acceptCommandTurn ever running.
  await controlFn({
    type: "interrupt_turn",
    id: "control-1",
    target_turn_id: "turn-active",
  });

  assert.deepEqual(
    calls,
    ["acceptInterrupt"],
    "interrupt must reach acceptInterrupt independently of the data-plane consumer's slot state",
  );
  ctl.abort();
});

// ensureSdkQuery is the load-bearing pinning point for model + effort.
// These tests pin the contract:
//   1. First submit_turn with values pins them into SDK Options.
//   2. First submit_turn with empty values falls back to DEFAULT_MODEL /
//      DEFAULT_EFFORT — the wire shape is additive so empty must keep
//      working for legacy clients.
//   3. Subsequent submit_turns are a no-op (the SDK Options are sealed
//      by the running query iterator). The override is silently honored
//      for telemetry only.
// If a future change wires per-turn setModel/applyFlagSettings to make
// the dropdown switchable mid-session, these tests will need to flip from
// "ignore overrides" to "apply overrides" — the regression that test #3
// catches today is the silent divergence between dropdown pick and pod
// behavior, which would be the user-trust failure.
test("ensureSdkQuery pins model + effort from the first submit_turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { model?: string; effort?: string }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    sdkQuery: unknown;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: { opts: { model?: string; effort?: string } | null } = { opts: null };
  runner.launchSdkQuery = (opts) => {
    captured.opts = opts;
    return { interrupt: () => {} } as unknown;
  };

  runner.ensureSdkQuery({
    id: "cmd-1",
    type: "submit_turn",
    model: "claude-haiku-4-5",
    effort: "low",
  });

  assert.equal(runner.pinnedModel, "claude-haiku-4-5");
  assert.equal(runner.pinnedEffort, "low");
  assert.notEqual(runner.sdkQuery, null);
  assert.ok(captured.opts, "launchSdkQuery should have been called");
  assert.equal(captured.opts.model, "claude-haiku-4-5");
  assert.equal(captured.opts.effort, "low");
});

test("ensureSdkQuery falls back to DEFAULT_MODEL / DEFAULT_EFFORT on empty first turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { model?: string; effort?: string }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: { opts: { model?: string; effort?: string } | null } = { opts: null };
  runner.launchSdkQuery = (opts) => {
    captured.opts = opts;
    return { interrupt: () => {} } as unknown;
  };

  runner.ensureSdkQuery({
    id: "cmd-1",
    type: "submit_turn",
    // No model or effort fields — legacy/pre-feature client.
  });

  // The defaults must stay in lockstep with the constants in runner.ts.
  // If the product moves the default to a different model, both this
  // assertion and the SPA's DEFAULT_RUN_PREFS need to update together.
  assert.equal(runner.pinnedModel, "claude-opus-4-7");
  assert.equal(runner.pinnedEffort, "high");
  assert.ok(captured.opts);
  assert.equal(captured.opts.model, "claude-opus-4-7");
  assert.equal(captured.opts.effort, "high");
});

test("ensureSdkQuery ignores model/effort overrides on subsequent turns", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { model?: string; effort?: string }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    sdkQuery: unknown;
    ensureSdkQuery: (record: unknown) => void;
  };
  let launchCalls = 0;
  runner.launchSdkQuery = (_opts) => {
    launchCalls += 1;
    return { interrupt: () => {} } as unknown;
  };

  runner.ensureSdkQuery({
    id: "cmd-1",
    type: "submit_turn",
    model: "claude-opus-4-7",
    effort: "high",
  });
  // Second turn requests a different model + effort. The runner MUST
  // keep the pinned values because the SDK's Options is sealed; an
  // override here would be a no-op at the pod, and silently appearing
  // to honor it would lie to the user. The metric path catches the
  // divergence (optionsOverrideIgnoredTotal) — we don't assert the
  // metric here because it's a prom-client global, but the no-launch +
  // pinned-values assertions cover the observable behavior.
  runner.ensureSdkQuery({
    id: "cmd-2",
    type: "submit_turn",
    model: "claude-haiku-4-5",
    effort: "low",
  });

  assert.equal(launchCalls, 1, "second turn must not relaunch the SDK query");
  assert.equal(runner.pinnedModel, "claude-opus-4-7");
  assert.equal(runner.pinnedEffort, "high");
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
