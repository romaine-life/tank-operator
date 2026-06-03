import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  classifyProviderFailure,
  dispatch,
  inputReplyAnnotations,
  inputReplyAnswers,
  inputReplyTargetProviderItemID,
  joinAnswersForSDK,
  logUnhandledSdkMessage,
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
import { truncateEventIfOversized } from "../../runner-shared/sessionBus.js";

test("classifyProviderFailure pins the extended-thinking resume 400 (session 340)", () => {
  // Verbatim shape of the API error that killed session 340 on resume.
  const msg =
    "API Error: 400 messages.1.content.9: `thinking` or `redacted_thinking` " +
    "blocks in the latest assistant message cannot be modified. These blocks " +
    "must remain as they were in the original response.";
  assert.equal(classifyProviderFailure(msg), "thinking_block_modified");
});

test("classifyProviderFailure maps the other known provider failure shapes", () => {
  assert.equal(
    classifyProviderFailure("API Error: 529 Overloaded"),
    "overloaded",
  );
  assert.equal(
    classifyProviderFailure("API Error: 429 rate limit exceeded"),
    "rate_limit",
  );
  assert.equal(
    classifyProviderFailure("prompt is too long: 250000 tokens > 200000"),
    "context_length",
  );
  assert.equal(
    classifyProviderFailure("API Error: 401 authentication_error"),
    "auth",
  );
  assert.equal(
    classifyProviderFailure("ECONNRESET socket hang up"),
    "other",
  );
});

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

// logUnhandledSdkMessage is the diagnostic surface for "what is the SDK
// telling us that the adapter throws away?" Background task lifecycle
// messages used to land here, but they are now first-class shell_task.*
// Tank events. The test pins two contracts at once:
//   - canonical types the adapter already converts ("assistant", "user",
//     "result", system task lifecycle, and the partial-typing "stream_event")
//     MUST stay silent
//     so we don't double-log them and don't flood kubectl logs with the
//     per-token partial stream.
//   - every other type MUST log one JSON line carrying the small set of
//     identifying fields a debugger needs (subtype, task_id, tool_use_id,
//     status, summary) without bringing the full payload along.
function captureConsoleLog<T>(fn: () => T): { result: T; lines: string[] } {
  const lines: string[] = [];
  const original = console.log;
  console.log = (msg: unknown) => {
    lines.push(String(msg));
  };
  try {
    const result = fn();
    return { result, lines };
  } finally {
    console.log = original;
  }
}

test("logUnhandledSdkMessage is silent for task lifecycle messages handled by the adapter", () => {
  const { lines } = captureConsoleLog(() =>
    logUnhandledSdkMessage({
      type: "system",
      subtype: "task_notification",
      task_id: "task-abc",
      tool_use_id: "toolu_xyz",
      status: "failed",
      summary: "Monitor stream ended without condition match",
      uuid: "u-1",
      session_id: "s-1",
    } as never),
  );
  assert.equal(lines.length, 0);
});

test("logUnhandledSdkMessage is silent for types the adapter already handles", () => {
  const { lines } = captureConsoleLog(() => {
    for (const type of ["assistant", "user", "result", "stream_event"]) {
      logUnhandledSdkMessage({ type } as never);
    }
  });
  assert.equal(
    lines.length,
    0,
    "adapter-handled types must not produce a duplicate diagnostic log",
  );
});

test("logUnhandledSdkMessage logs unknown types even with no identifying fields", () => {
  // A future SDK version may add types we haven't enumerated. The contract
  // is "anything our adapter ignores becomes a single log line so it is
  // discoverable" — not "log only the types we already know about." This
  // pins the open-ended behavior so a forward-incompatible SDK message
  // doesn't go silent.
  const { lines } = captureConsoleLog(() =>
    logUnhandledSdkMessage({ type: "future_unknown_kind" } as never),
  );
  assert.equal(lines.length, 1);
  const parsed = JSON.parse(lines[0]);
  assert.equal(parsed.msg, "sdk_message_unhandled");
  assert.equal(parsed.type, "future_unknown_kind");
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

test("joinAnswersForSDK includes free-form notes in provider-visible answer text", () => {
  assert.deepEqual(
    joinAnswersForSDK(
      {
        "Question type": ["Personality (Recommended)"],
        "Pure free-form": ["Other"],
      },
      {
        "Question type": { notes: "ask about chat box behavior" },
        "Pure free-form": { notes: "use this as the answer" },
      },
    ),
    {
      "Question type": "Personality (Recommended)\n\nAdditional context: ask about chat box behavior",
      "Pure free-form": "use this as the answer",
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

// dispatchInputReplyIndependentlyOfSubmit pins the exact same architectural
// guarantee for input_reply that the previous test pins for interrupts. The
// failure mode is also identical: an input_reply only ever exists *while*
// a submit_turn is parked in canUseTool (the AskUserQuestion gate) — and
// that exact submit_turn is the message holding the data-plane consumer's
// single max_ack_pending=1 slot. If input_reply were routed to the data
// plane, JetStream would queue it behind the parked submit_turn and never
// deliver it; the runner would wait forever for an input_reply that's
// stuck behind the submit_turn that's waiting for the input_reply. A
// classic dispatch deadlock. The fix runs input_reply on the control
// consumer the same way interrupt_turn does. This test pins that:
// input_reply on the control handler triggers acceptInputReply with
// acceptCommandTurn never running.
test("dispatchInputReplyIndependentlyOfSubmit: control handler dispatches input_reply without waiting for the data plane", async () => {
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
  await new Promise((resolve) => setImmediate(resolve));

  const dataFn = handlers.data;
  const controlFn = handlers.control;
  if (!dataFn) throw new Error("startCommandConsumer should register a data handler");
  if (!controlFn) throw new Error("startControlConsumer should register a separate control handler");

  await controlFn({
    type: "input_reply",
    id: "control-input-1",
    target_turn_id: "turn-active",
    answers: { Q: ["A"] },
  });

  assert.deepEqual(
    calls,
    ["acceptInputReply"],
    "input_reply must reach acceptInputReply on the control consumer; routing it to the data plane is the deadlock the split prevents",
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
  assert.equal(runner.pinnedModel, "claude-opus-4-8");
  assert.equal(runner.pinnedEffort, "high");
  assert.ok(captured.opts);
  assert.equal(captured.opts.model, "claude-opus-4-8");
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

// ───────────────────────────────────────────────────────────────────────────
// romaine-life/tank-operator#532 — four-outcome contract for accepted interrupts.
//
// Every interrupt_turn command the runner accepts MUST resolve to exactly
// one terminal-outcome increment on tank_runner_interrupt_outcome_total
// within bounded time. The pre-#532 shape had two silent strandings:
//
//   1. Race: interrupt arrives before submit_turn is dispatched. The old
//      interruptActiveTurn returned "not_found" silently and the stop
//      click was lost — the SDK never got an interrupt, no durable
//      terminal ever landed, the UI hung in "stopping".
//   2. Publish failure: turn.interrupted dispatch failed. The old shape
//      gated sdkQuery.interrupt() on the durable publish succeeding, so
//      a transient publish failure let the model keep generating tool
//      calls until natural completion. Same shape: no durable terminal,
//      UI hangs.
//
// These tests pin the buffer-and-apply contract that closes both silent
// paths. Each test asserts the visible terminal events emitted plus the
// command bus ack/fail outcomes; they intentionally don't assert on the
// interruptOutcomeTotal counter directly (it's a process-global Counter
// from prom-client, which doesn't play well with parallel tests).
// ───────────────────────────────────────────────────────────────────────────

interface InterruptTestHarness {
  events: TankConversationEvent[];
  bus: string[];
  sdkInterrupts: number;
  sdkBackgroundTasks: number;
  sdkControlCalls: string[];
  heartbeats: number;
  setSinkFailureCount: (n: number) => void;
}

function makeInterruptHarness(runnerCfg = runnerConfig()): {
  runner: Runner;
  harness: InterruptTestHarness;
} {
  const events: TankConversationEvent[] = [];
  const bus: string[] = [];
  let sdkInterrupts = 0;
  let sdkBackgroundTasks = 0;
  const sdkControlCalls: string[] = [];
  let heartbeats = 0;
  let sinkFailuresLeft = 0;
  const harness: InterruptTestHarness = {
    events,
    bus,
    get sdkInterrupts() {
      return sdkInterrupts;
    },
    get sdkBackgroundTasks() {
      return sdkBackgroundTasks;
    },
    sdkControlCalls,
    get heartbeats() {
      return heartbeats;
    },
    setSinkFailureCount(n: number) {
      sinkFailuresLeft = n;
    },
  } as InterruptTestHarness;
  const runner = new Runner(runnerCfg);
  const internals = runner as unknown as {
    sink: { upsert: (e: TankConversationEvent) => Promise<void>; findTurnTerminal?: () => Promise<null> };
    commandBus: {
      markCompleted: (r?: unknown) => Promise<void>;
      markFailed: (r?: unknown, err?: unknown) => Promise<void>;
      startCommandHeartbeat: (r?: unknown) => () => void;
      attemptsExceeded: (r?: unknown) => boolean;
    };
    sdkQuery: { interrupt: () => Promise<void> } | null;
  };
  internals.sink = {
    async upsert(event: TankConversationEvent) {
      if (sinkFailuresLeft > 0) {
        sinkFailuresLeft -= 1;
        throw new Error("simulated dispatch failure");
      }
      events.push(event);
    },
  };
  internals.commandBus = {
    async markCompleted() {
      bus.push("ack");
    },
    async markFailed() {
      bus.push("fail");
    },
    startCommandHeartbeat() {
      heartbeats += 1;
      return () => {
        heartbeats -= 1;
      };
    },
    attemptsExceeded() {
      return false;
    },
  };
  internals.sdkQuery = {
    async backgroundTasks() {
      sdkBackgroundTasks += 1;
      sdkControlCalls.push("backgroundTasks");
      return true;
    },
    async interrupt() {
      sdkInterrupts += 1;
      sdkControlCalls.push("interrupt");
    },
  } as never;
  return { runner, harness };
}

test("acceptInterrupt during in-flight turn: foreground tasks are backgrounded before SDK interrupt, terminal publishes immediately", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    acceptInterrupt: (record: unknown) => Promise<void>;
  };
  const turn = {
    turnID: "turn_abc-123",
    clientNonce: "abc-123",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
  };
  r.activeTurn = turn;

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "abc-123",
    client_nonce: "abc-123",
  });

  assert.deepEqual(
    harness.sdkControlCalls,
    ["backgroundTasks", "interrupt"],
    "Stop should ask Claude to background foreground shell work before interrupting the active turn",
  );
  assert.equal(harness.sdkBackgroundTasks, 1, "foreground Bash/subagent backgrounding must be attempted");
  assert.equal(harness.sdkInterrupts, 1, "SDK interrupt must be called");
  assert.equal(harness.events.length, 1, "exactly one durable terminal must be published");
  assert.equal(harness.events[0]!.type, "turn.interrupted");
  assert.equal((harness.events[0] as { turn_id: string }).turn_id, "turn_abc-123");
  assert.deepEqual(harness.bus, ["ack"], "interrupt command must be acked");
});

test("acceptInterrupt with no matching turn: buffered, applied when submit_turn arrives, never feeds SDK", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    pendingInterrupts: unknown[];
    acceptInterrupt: (record: unknown) => Promise<void>;
    acceptCommandTurn: (record: unknown) => Promise<void>;
    ensureSdkQuery: (record: unknown) => void;
    finalizeCommandIfAlreadyTerminal: () => Promise<boolean>;
    sink: { findTurnTerminal: () => Promise<null> };
    userQueue: { push: (m: unknown) => void };
  };
  r.activeTurn = null;
  r.ensureSdkQuery = () => {};
  (r.sink as { findTurnTerminal: () => Promise<null> }).findTurnTerminal = async () => null;
  // Spy on userQueue.push to confirm the SDK is never fed.
  const sdkFed: unknown[] = [];
  r.userQueue = {
    push(message: unknown) {
      sdkFed.push(message);
    },
  };

  // Stop click arrives first.
  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "abc-123",
    client_nonce: "abc-123",
  });
  assert.equal(r.pendingInterrupts.length, 1, "interrupt must be buffered (no matching turn yet)");
  assert.equal(harness.events.length, 0, "no durable terminal yet — waiting for the matching submit_turn");
  assert.deepEqual(harness.bus, [], "no ack/fail yet — the JetStream record is parked");

  // Now the matching submit_turn lands.
  await r.acceptCommandTurn({
    type: "submit_turn",
    id: "cmd-2",
    prompt: "hello",
    client_nonce: "abc-123",
    target_turn_id: "abc-123",
  });

  assert.equal(r.pendingInterrupts.length, 0, "buffer must drain on matching submit_turn");
  assert.equal(harness.sdkInterrupts, 0, "SDK must not be interrupted — it was never fed the prompt");
  assert.equal(sdkFed.length, 0, "SDK userQueue must not receive the aborted-before-start turn");
  assert.equal(harness.events.length, 1, "synthetic turn.interrupted must be published");
  assert.equal(harness.events[0]!.type, "turn.interrupted");
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string } }).payload?.reason,
    "client_interrupt_before_start",
    "reason must distinguish the pre-SDK path from the during-turn path",
  );
});

test("acceptInterrupt with publish failure: durable terminal still lands via fallback turn.failed", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    acceptInterrupt: (record: unknown) => Promise<void>;
  };
  const turn = {
    turnID: "turn_xyz-9",
    clientNonce: "xyz-9",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
  };
  r.activeTurn = turn;
  // Fail every turn.interrupted publish attempt (3 retries) plus the
  // fallback turn.failed publish attempts — until exactly the LAST
  // fallback succeeds. This isolates the contract: even when the
  // happy-path terminal can't land, the runner MUST emit *some*
  // durable terminal so the UI's "stopping" state resolves.
  harness.setSinkFailureCount(3 + 2); // 3 retries on interrupted, then 2 of 3 on fallback

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "xyz-9",
    client_nonce: "xyz-9",
  });

  assert.equal(harness.sdkInterrupts, 1, "SDK interrupt must still be called regardless of publish outcome");
  assert.equal(harness.events.length, 1, "exactly one durable terminal must eventually land");
  assert.equal(harness.events[0]!.type, "turn.failed", "fallback shape is turn.failed");
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string } }).payload?.reason,
    "publish_interrupt_failed",
    "fallback reason must name the cause so the post-mortem isn't a mystery",
  );
  assert.deepEqual(harness.bus, ["fail"], "command bus must be marked failed when the happy terminal didn't land");
});

test("acceptInterrupt when turn already terminal: ack without re-emitting a terminal", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    acceptInterrupt: (record: unknown) => Promise<void>;
  };
  r.activeTurn = {
    turnID: "turn_done-1",
    clientNonce: "done-1",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: true, // turn raced to completion before the interrupt landed
  };

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "done-1",
    client_nonce: "done-1",
  });

  assert.equal(harness.sdkInterrupts, 0, "no SDK call — nothing to interrupt");
  assert.equal(harness.events.length, 0, "no new terminal — the natural terminal already exists");
  assert.deepEqual(harness.bus, ["ack"], "interrupt command still acked (it was delivered correctly)");
});

test("Claude rate_limit_event fails active turn durably instead of stranding the queue", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    handleEvent: (message: unknown) => Promise<void>;
  };
  r.activeTurn = {
    turnID: "turn_rate-limited",
    clientNonce: "rate-limited",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
    commandRecord: { id: "submit-1", type: "submit_turn" },
    stopCommandHeartbeat: () => harness.sdkControlCalls.push("stop-heartbeat"),
  };

  await r.handleEvent({
    type: "rate_limit_event",
    uuid: "rate-limit-1",
    retry_after_ms: 60000,
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(harness.events.length, 1, "rate limit must emit exactly one durable terminal");
  assert.equal(harness.events[0]!.type, "turn.failed");
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string; error?: string } }).payload?.reason,
    "provider_rate_limit",
  );
  assert.match(
    (harness.events[0] as { payload?: { error?: string } }).payload?.error ?? "",
    /retry_after_ms=60000/,
  );
  assert.deepEqual(harness.bus, ["ack"], "submit command must ack after the durable rate-limit terminal");
});

test("acceptInterrupt with missing target: fails command explicitly instead of silently acking", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as { acceptInterrupt: (record: unknown) => Promise<void> };

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "",
    client_nonce: "",
  });

  assert.deepEqual(harness.bus, ["fail"], "missing target must produce a visible failure, not a silent ack");
  assert.equal(harness.events.length, 0);
});

// ───────────────────────────────────────────────────────────────────────────
// romaine-life/tank-operator#532 Stage 3 — oversized-event truncation contract.
// truncateEventIfOversized must keep Tank conversation events under the
// transport budget so NATS's `payload max_payload size exceeded`
// throw doesn't silently hole the durable ledger (the seven publish
// failures observed on session 19 across the pod's lifetime were
// exactly this shape).
// ───────────────────────────────────────────────────────────────────────────

test("truncateEventIfOversized passes through events under the budget unchanged", () => {
  const tiny = {
    event_id: "turn_t1:turn.interrupted:client_interrupt",
    type: "turn.interrupted",
    turn_id: "turn_t1",
    payload: { reason: "client_interrupt" },
  };
  const result = truncateEventIfOversized(tiny);
  assert.equal(result.truncated, false);
  assert.equal(result.event, tiny, "should return the same reference when no work is needed");
  assert.equal(result.fields.length, 0);
});

test("truncateEventIfOversized replaces oversized string fields with a typed marker", () => {
  const huge = "x".repeat(200_000); // 200 KiB single string
  const event = {
    event_id: "turn_t1:item.completed:abc",
    type: "item.completed",
    turn_id: "turn_t1",
    payload: {
      kind: "tool_result",
      output: huge,
    },
  };
  const result = truncateEventIfOversized(event, { maxBytes: 50_000, stringThreshold: 1024 });
  assert.equal(result.truncated, true);
  assert.equal(result.payloadDropped, undefined, "envelope should be preserved when string truncation suffices");
  assert.ok(result.finalBytes <= 50_000, `result must fit under budget; got ${result.finalBytes}`);
  // Original event is not mutated; clone-modify is required for shared
  // event objects.
  assert.equal((event.payload as { output: string }).output.length, 200_000);
  // Truncated string carries the original size + a sha256_16 prefix.
  const truncated = (result.event.payload as { output: string }).output;
  assert.ok(truncated.startsWith("[truncated: 200000 bytes"), `unexpected marker: ${truncated.slice(0, 80)}`);
  assert.ok(/sha256_16=[0-9a-f]{16}/.test(truncated));
  // Schema of the field stays a string — downstream renderers reading
  // payload.output don't need to type-check.
  assert.equal(typeof truncated, "string");
});

test("truncateEventIfOversized drops payload entirely when string truncation cannot fit the budget", () => {
  // A payload made of MANY small strings — none individually large
  // enough to be the obvious truncation target — that collectively
  // exceed the budget. The aggressive-pass loop should lower the
  // threshold, fail to find enough headroom, and replace the whole
  // payload with the dropped marker.
  const payload: Record<string, string> = {};
  for (let i = 0; i < 100; i++) {
    payload[`field_${i}`] = "y".repeat(2_000);
  }
  const event = { event_id: "e1", type: "item.completed", turn_id: "t1", payload };
  const result = truncateEventIfOversized(event, { maxBytes: 5_000, stringThreshold: 100_000 });
  assert.equal(result.truncated, true);
  assert.equal(result.payloadDropped, true, "must fall back to payload-dropped when strings alone can't fit");
  assert.ok(result.finalBytes <= 5_000);
  assert.equal((result.event.payload as unknown as { __payload_dropped: boolean }).__payload_dropped, true);
  // Envelope fields stay intact so the durable ledger still records
  // "an event of this type existed for this turn."
  assert.equal(result.event.type, "item.completed");
  assert.equal(result.event.event_id, "e1");
  assert.equal(result.event.turn_id, "t1");
});
