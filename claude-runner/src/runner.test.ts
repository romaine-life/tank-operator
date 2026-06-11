import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  claudeRateLimitEventIsTerminal,
  claudeRateLimitInfo,
  claudeRestartClosureEvent,
  classifyProviderFailure,
  dispatch,
  logUnhandledSdkMessage,
  pickContextWindowFromModelUsage,
  Runner,
} from "./runner.js";
import {
  isDurableTankConversationEvent,
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../runner-shared/conversation.js";
import {
  askUserQuestionHandoffEvents,
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
  assert.equal(classifyProviderFailure("ECONNRESET socket hang up"), "other");
});

test("pickContextWindowFromModelUsage reads contextWindow from a populated entry", () => {
  // Shape of SDKResultMessage.modelUsage: Record<string, ModelUsage>, where
  // each ModelUsage carries `contextWindow`. Single entry → its window.
  assert.equal(
    pickContextWindowFromModelUsage({
      "claude-opus-4-8": {
        contextWindow: 200000,
      } as { contextWindow?: number },
    }),
    200000,
  );
});

test("pickContextWindowFromModelUsage returns the max across multiple entries", () => {
  // A turn can touch more than one model (e.g. a subagent on Haiku); take the
  // largest positive window so the composer fraction reflects the main model.
  assert.equal(
    pickContextWindowFromModelUsage({
      "claude-haiku": { contextWindow: 100000 },
      "claude-opus-4-8": { contextWindow: 200000 },
      "claude-sonnet": { contextWindow: 150000 },
    }),
    200000,
  );
});

test("pickContextWindowFromModelUsage floors a fractional window", () => {
  assert.equal(
    pickContextWindowFromModelUsage({
      "claude-opus-4-8": { contextWindow: 199999.9 },
    }),
    199999,
  );
});

test("pickContextWindowFromModelUsage returns null for missing/empty/non-positive windows", () => {
  // null (not 0) is the contract so the caller skips the report rather than
  // reporting a zero window. The runner must not even reach the report when
  // every entry's window is missing/zero/negative/non-finite.
  assert.equal(pickContextWindowFromModelUsage(undefined), null);
  assert.equal(pickContextWindowFromModelUsage({}), null);
  assert.equal(pickContextWindowFromModelUsage({ m: {} }), null);
  assert.equal(
    pickContextWindowFromModelUsage({ m: { contextWindow: 0 } }),
    null,
  );
  assert.equal(
    pickContextWindowFromModelUsage({ m: { contextWindow: -5 } }),
    null,
  );
  assert.equal(
    pickContextWindowFromModelUsage({ m: { contextWindow: Number.NaN } }),
    null,
  );
  assert.equal(
    pickContextWindowFromModelUsage({ m: { contextWindow: Infinity } }),
    null,
  );
  // A mix where only some entries are unusable still yields the best positive.
  assert.equal(
    pickContextWindowFromModelUsage({
      bad: { contextWindow: 0 },
      good: { contextWindow: 200000 },
    }),
    200000,
  );
});

test("claudeRateLimitInfo keeps the SDK rate-limit fields admins need", () => {
  assert.deepEqual(
    claudeRateLimitInfo({
      type: "rate_limit_event",
      uuid: "rate-limit-1",
      session_id: "sdk-session-1",
      rate_limit_info: {
        status: "rejected",
        rateLimitType: "five_hour",
        resetsAt: 1790000000000,
        utilization: 1.02,
        isUsingOverage: false,
        nested: { ignored: true },
      },
    }),
    {
      provider: "claude",
      status: "rejected",
      rateLimitType: "five_hour",
      resetsAt: 1790000000000,
      utilization: 1.02,
      isUsingOverage: false,
      uuid: "rate-limit-1",
      session_id: "sdk-session-1",
    },
  );
});

test("AskUserQuestion handoff emits a route-safe question turn id", () => {
  const handoff = askUserQuestionHandoffEvents({
    sessionID: "63",
    askingTurnID: "turn_askq-test-1780648459",
    askingClientNonce: "askq-test-1780648459",
    source: "claude",
    providerItemID: "toolu_01CmiCoRsbHCRZwTAT8P2pNZ",
    providerTimelineID:
      "turn_askq-test-1780648459:item:toolu_01CmiCoRsbHCRZwTAT8P2pNZ",
    questions: [
      { question: "Which cat coat color do you like best?" },
      { question: "Which cat behavior is your favorite?" },
    ],
  });

  assert.match(handoff.questionTurnID, /^[A-Za-z0-9._-]{1,80}$/);
  assert.equal(handoff.questionTurnID.includes(":"), false);
  assert.equal(handoff.questionSubmitted.turn_id, handoff.questionTurnID);
  assert.ok(handoff.awaitingInput.payload);
  assert.equal(
    handoff.awaitingInput.payload.question_turn_id,
    handoff.questionTurnID,
  );
});

test("claudeRateLimitEventIsTerminal follows primary quota status, not overage status", () => {
  assert.equal(
    claudeRateLimitEventIsTerminal({
      type: "rate_limit_event",
      uuid: "rate-limit-allowed-overage-rejected",
      rate_limit_info: {
        status: "allowed",
        rateLimitType: "five_hour",
        resetsAt: 1780659000,
        overageStatus: "rejected",
        overageDisabledReason: "org_level_disabled",
        isUsingOverage: false,
      },
    }),
    false,
    "session 618 shape: primary quota allowed means this is informational",
  );
  assert.equal(
    claudeRateLimitEventIsTerminal({
      type: "rate_limit_event",
      uuid: "rate-limit-rejected",
      rate_limit_info: {
        status: "rejected",
        rateLimitType: "five_hour",
        resetsAt: 1790000000000,
      },
    }),
    true,
    "rejected primary quota still terminates the active turn",
  );
  assert.equal(
    claudeRateLimitEventIsTerminal({
      type: "rate_limit_event",
      uuid: "rate-limit-retry",
      retry_after_ms: 60000,
    }),
    true,
    "unstructured retry/error frames remain terminal so the command queue cannot strand",
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

// Background-task wake: a run_in_background task finishing while the session is
// idle must register a durable backend wake so the agent is re-invoked (the
// runner registers; the orchestrator owns the fire decision). The runner only
// adds the task id to firedBackgroundTaskWakes after every gate passes, so the
// Set is a faithful proxy for "registration was attempted". operatorInternalURL
// is empty in runnerConfig(), so registerBackgroundTaskWake returns false
// without any network call.
test("maybeRegisterBackgroundTaskWake registers an idle natural terminal exactly once", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    maybeRegisterBackgroundTaskWake: (e: unknown, observedEventID: string) => Promise<void>;
    activeTurn: unknown;
    firedBackgroundTaskWakes: Set<string>;
  };
  runner.activeTurn = null;
  const terminal = {
    type: "system",
    subtype: "task_notification",
    task_id: "task-idle",
    status: "completed",
    uuid: "u-1",
  };

  await runner.maybeRegisterBackgroundTaskWake(terminal, "evt-1");
  assert.equal(
    runner.firedBackgroundTaskWakes.has("task-idle\u001fevt-1"),
    true,
  );

  // A repeated terminal frame of the SAME observation must not re-register.
  await runner.maybeRegisterBackgroundTaskWake(terminal, "evt-1");
  assert.equal(runner.firedBackgroundTaskWakes.size, 1);

  // A NEW observation of the same task (the real completion after a
  // premature fire) is a fresh registration — the backend decides whether it
  // re-arms the next wake generation. Task-id-only dedupe was the once-only
  // wake burn.
  await runner.maybeRegisterBackgroundTaskWake(terminal, "evt-2");
  assert.equal(runner.firedBackgroundTaskWakes.size, 2);
});

test("maybeRegisterBackgroundTaskWake skips when a turn is active", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    maybeRegisterBackgroundTaskWake: (e: unknown, observedEventID: string) => Promise<void>;
    activeTurn: unknown;
    firedBackgroundTaskWakes: Set<string>;
  };
  runner.activeTurn = {
    turnID: "turn-active",
    clientNonce: "turn-active",
    terminalEmitted: false,
  };
  await runner.maybeRegisterBackgroundTaskWake(
    {
      type: "system",
      subtype: "task_notification",
      task_id: "task-bound",
      status: "completed",
    },
    "evt-bound",
  );
  // The active turn receives the bound shell_task.exited in-turn; no wake needed.
  assert.equal(runner.firedBackgroundTaskWakes.size, 0);
});

test("maybeRegisterBackgroundTaskWake registers when active turn is already terminal", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    maybeRegisterBackgroundTaskWake: (e: unknown, observedEventID: string) => Promise<void>;
    activeTurn: unknown;
    firedBackgroundTaskWakes: Set<string>;
  };
  runner.activeTurn = {
    turnID: "turn-terminal",
    clientNonce: "turn-terminal",
    terminalEmitted: true,
  };
  await runner.maybeRegisterBackgroundTaskWake(
    {
      type: "system",
      subtype: "task_notification",
      task_id: "task-terminal",
      status: "completed",
    },
    "evt-terminal",
  );
  assert.equal(
    runner.firedBackgroundTaskWakes.has("task-terminal\u001fevt-terminal"),
    true,
  );
});

test("maybeRegisterBackgroundTaskWake ignores user stops and lifecycle starts", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    maybeRegisterBackgroundTaskWake: (e: unknown, observedEventID: string) => Promise<void>;
    activeTurn: unknown;
    firedBackgroundTaskWakes: Set<string>;
  };
  runner.activeTurn = null;
  await runner.maybeRegisterBackgroundTaskWake({
    type: "system",
    subtype: "task_notification",
    task_id: "task-cancel",
    status: "cancelled",
  }, "");
  await runner.maybeRegisterBackgroundTaskWake({
    type: "system",
    subtype: "task_started",
    task_id: "task-start",
    status: "running",
  }, "");
  assert.equal(runner.firedBackgroundTaskWakes.size, 0);
});

const fixturesPath = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../schemas/tank-conversation-event.fixtures.json",
);
const fixtures: { events: { name: string; event: TankConversationEvent }[] } =
  JSON.parse(readFileSync(fixturesPath, "utf8"));

test("dispatch publishes a built Tank event and stamps order metadata", async () => {
  const order: Order = [];
  let sinkEvent: TankConversationEvent | undefined;
  const sink = {
    async upsert(
      event: TankConversationEvent & { uuid: string; order_key: string },
    ) {
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
    () =>
      dispatch(sink, { type: "assistant" } as unknown as TankConversationEvent),
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
    () =>
      stampTankEvent({
        type: "user_message.created",
      } as unknown as TankConversationEvent),
    /event_id is required/,
  );
});

test("durable Tank fixtures pass the shared filter and dispatch end-to-end", async () => {
  for (const { name, event } of fixtures.events) {
    const order: Order = [];
    const sink = makeSink(order);
    const stamped = stampTankEvent(event);
    if (!isDurableTankConversationEvent(stamped)) {
      assert.fail(
        `${name}: stamped fixture should satisfy isDurableTankConversationEvent`,
      );
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
  for (const event of [
    stampTankEvent(userMessage),
    stampTankEvent(turnSubmitted),
  ]) {
    assert.equal(isTankConversationEvent(event), true);
  }
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
      startCommandConsumer: (
        h: (r: unknown) => Promise<void>,
        s?: AbortSignal,
      ) => Promise<() => Promise<void>>;
      startControlConsumer: (
        h: (r: unknown) => Promise<void>,
        s?: AbortSignal,
      ) => Promise<() => Promise<void>>;
      markCompleted: () => Promise<void>;
    };
    startCommandConsumer: (signal: AbortSignal) => () => void;
    startControlConsumer: (signal: AbortSignal) => () => void;
    acceptInterrupt: (record: unknown) => Promise<void>;
    acceptCommandTurn: (record: unknown) => Promise<void>;
  };

  type RecordHandler = (record: unknown) => Promise<void>;
  const handlers: {
    data: RecordHandler | null;
    control: RecordHandler | null;
  } = {
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

  const ctl = new AbortController();
  runner.startCommandConsumer(ctl.signal);
  runner.startControlConsumer(ctl.signal);
  // Yield so the .then() callbacks that capture the consumer handlers
  // get a chance to run before we read them.
  await new Promise((resolve) => setImmediate(resolve));

  const dataFn = handlers.data;
  const controlFn = handlers.control;
  if (!dataFn)
    throw new Error("startCommandConsumer should register a data handler");
  if (!controlFn)
    throw new Error(
      "startControlConsumer should register a separate control handler",
    );
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

test("acceptCommandTurn emits turn.claimed before provider output", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    sink: { findTurnTerminal: () => Promise<null> };
    ensureSdkQuery: () => void;
    acceptCommandTurn: (record: unknown) => Promise<void>;
  };
  r.sink.findTurnTerminal = async () => null;
  r.ensureSdkQuery = () => undefined;

  await r.acceptCommandTurn({
    type: "submit_turn",
    id: "submit-1",
    client_nonce: "client-claimed",
    prompt: "work on this",
    created_at: new Date(Date.now() - 250).toISOString(),
  });

  assert.equal(
    harness.events.length,
    1,
    "claimed is the only pre-provider durable event",
  );
  assert.equal(harness.events[0]!.type, "turn.claimed");
  assert.equal(harness.events[0]!.client_nonce, "client-claimed");
  assert.deepEqual(
    harness.bus,
    [],
    "submit command is not acked until a terminal event",
  );
});

// canUseTool is the AskUserQuestion pause point. When the agent invokes
// AskUserQuestion the runner publishes durable turn.awaiting_input, keeps the
// turn active, and resolves only when input_reply arrives for the same
// turn/tool item.
test("canUseTool pauses the active turn and resumes from input_reply", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    canUseTool: (
      toolName: string,
      input: unknown,
      ctx: { toolUseID?: string },
    ) => Promise<{
      behavior: string;
      updatedInput?: { answers?: Record<string, string> };
    }>;
    acceptInputReply: (record: unknown) => Promise<void>;
    activeTurn: unknown;
    publishTerminalWithRetry: (
      event: TankConversationEvent,
    ) => Promise<boolean>;
    markCommandTerminal: (turn: unknown, outcome: string) => Promise<void>;
    rotateTurnForInputReply: (turn: unknown, record: unknown) => Promise<void>;
    commandBus: {
      markCompleted: (record: unknown) => Promise<void>;
      markFailed: (record: unknown) => Promise<void>;
    };
    sink: { upsert: (event: TankConversationEvent) => Promise<void> };
  };

  const dispatched: TankConversationEvent[] = [];
  const published: TankConversationEvent[] = [];
  const outcomes: string[] = [];
  let completedRecord: unknown;
  runner.sink = {
    async upsert(event) {
      dispatched.push(event);
    },
  };
  runner.publishTerminalWithRetry = async (event) => {
    published.push(event);
    return true;
  };
  runner.markCommandTerminal = async (_turn, outcome) => {
    outcomes.push(outcome);
  };
  runner.commandBus = {
    async markCompleted(record) {
      completedRecord = record;
    },
    async markFailed() {
      assert.fail("input_reply should resolve the pending AskUserQuestion");
    },
  };
  runner.rotateTurnForInputReply = async (_turn, record) => {
    assert.equal(
      (record as { client_nonce?: string }).client_nonce,
      "answer-continuation",
    );
  };
  runner.activeTurn = {
    turnID: "turn-active",
    clientNonce: "turn-active",
    terminalEmitted: false,
    commandRecord: {},
  };

  const resultPromise = runner.canUseTool(
    "AskUserQuestion",
    {
      questions: [
        { question: "Which auth method?", options: [{ label: "OAuth" }] },
      ],
    },
    { toolUseID: "toolu_ask" },
  );
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(
    dispatched.map((e) => e.type),
    [
      "turn.awaiting_input.invocation",
      "assistant_message.created",
      "turn.submitted",
    ],
  );
  const questionMessage = dispatched.find(
    (e) => e.type === "assistant_message.created",
  );
  assert.equal(questionMessage?.turn_id, "turn-active");
  assert.equal(questionMessage?.actor, "assistant");
  // A durable turn.awaiting_input pause carries the question turn forward.
  const awaiting = published.find(
    (e) => (e as { type?: string }).type === "turn.awaiting_input",
  );
  assert.ok(awaiting, "expected turn.awaiting_input to be published");
  assert.notEqual((awaiting as { turn_id?: string }).turn_id, "turn-active");
  assert.deepEqual(
    outcomes,
    [],
    "AskUserQuestion is the Tank-visible response, but the provider command stays in flight for callback recovery",
  );

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:toolu_ask",
    target_provider_item_id: "toolu_ask",
    answers: { "Which auth method?": ["OAuth"] },
    annotations: { "Which auth method?": { notes: "matches the IdP" } },
  });
  const result = await resultPromise;
  assert.equal(result.behavior, "allow");
  assert.deepEqual(result.updatedInput?.answers, {
    "Which auth method?": "OAuth\n\nmatches the IdP",
  });
  assert.ok(
    completedRecord,
    "input_reply command should be acked after resolving the tool",
  );
});

test("canUseTool delivers free-form Other text to Claude instead of synthetic label", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    canUseTool: (
      toolName: string,
      input: unknown,
      ctx: { toolUseID?: string },
    ) => Promise<{
      behavior: string;
      updatedInput?: { answers?: Record<string, string> };
    }>;
    acceptInputReply: (record: unknown) => Promise<void>;
    activeTurn: unknown;
    publishTerminalWithRetry: (
      event: TankConversationEvent,
    ) => Promise<boolean>;
    markCommandTerminal: (turn: unknown, outcome: string) => Promise<void>;
    rotateTurnForInputReply: (turn: unknown, record: unknown) => Promise<void>;
    commandBus: {
      markCompleted: (record: unknown) => Promise<void>;
      markFailed: (record: unknown) => Promise<void>;
    };
    sink: { upsert: (event: TankConversationEvent) => Promise<void> };
  };

  runner.sink = {
    async upsert() {},
  };
  runner.publishTerminalWithRetry = async () => true;
  runner.markCommandTerminal = async () => {};
  runner.commandBus = {
    async markCompleted() {},
    async markFailed() {
      assert.fail("input_reply should resolve the pending AskUserQuestion");
    },
  };
  runner.rotateTurnForInputReply = async (_turn, record) => {
    assert.equal(
      (record as { client_nonce?: string }).client_nonce,
      "answer-continuation",
    );
  };
  runner.activeTurn = {
    turnID: "turn-active",
    clientNonce: "turn-active",
    terminalEmitted: false,
    commandRecord: {},
  };

  const resultPromise = runner.canUseTool(
    "AskUserQuestion",
    {
      questions: [
        {
          question: "Proceed?",
          allowFreeForm: true,
          options: [{ label: "Yes" }],
        },
      ],
    },
    { toolUseID: "toolu_ask" },
  );
  await new Promise((resolve) => setImmediate(resolve));

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:toolu_ask",
    target_provider_item_id: "toolu_ask",
    answers: { "Proceed?": ["Other"] },
    annotations: { "Proceed?": { notes: "Use the dedicated test database." } },
  });

  const result = await resultPromise;
  assert.equal(result.behavior, "allow");
  assert.deepEqual(result.updatedInput?.answers, {
    "Proceed?": "Use the dedicated test database.",
  });
});

test("acceptInputReply redelivers when the provider callback is not recreated yet", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    acceptInputReply: (record: unknown) => Promise<void>;
    commandBus: {
      markCompleted: () => Promise<void>;
      markFailed: () => Promise<void>;
    };
  };
  let nakDelay: number | undefined;
  runner.commandBus = {
    async markCompleted() {
      assert.fail(
        "early input_reply should not complete without a pending callback",
      );
    },
    async markFailed() {
      assert.fail("early input_reply should redeliver instead of failing");
    },
  };

  await runner.acceptInputReply({
    type: "input_reply",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:toolu_ask",
    target_provider_item_id: "toolu_ask",
    answers: { "Proceed?": ["Yes"] },
    nak(delayMs: number) {
      nakDelay = delayMs;
    },
  });

  assert.equal(nakDelay, 1000);
});

test("canUseTool auto-allows non-AskUserQuestion tools unchanged", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    canUseTool: (
      toolName: string,
      input: unknown,
      ctx: { toolUseID?: string },
    ) => Promise<{ behavior: string; updatedInput?: unknown }>;
  };
  const input = { command: "ls -la" };
  const result = await runner.canUseTool("Bash", input, {
    toolUseID: "toolu_bash",
  });
  assert.equal(result.behavior, "allow");
  assert.equal(result.updatedInput, input);
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
    launchSdkQuery: (opts: {
      model?: string;
      effort?: string;
      stderr?: (data: string) => void;
    }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    sdkQuery: unknown;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: {
    opts: {
      model?: string;
      effort?: string;
      stderr?: (data: string) => void;
    } | null;
  } = { opts: null };
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
  assert.equal(typeof captured.opts.stderr, "function");
});

test("ensureSdkQuery stderr callback logs redacted Claude SDK stderr", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { stderr?: (data: string) => void }) => unknown;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: { opts: { stderr?: (data: string) => void } | null } = {
    opts: null,
  };
  runner.launchSdkQuery = (opts) => {
    captured.opts = opts;
    return { interrupt: () => {} } as unknown;
  };
  const warnings: string[] = [];
  const originalWarn = console.warn;
  console.warn = (...args: unknown[]) => {
    warnings.push(args.map(String).join(" "));
  };
  try {
    runner.ensureSdkQuery({ id: "cmd-1", type: "submit_turn" });
    captured.opts?.stderr?.(
      "API Error: 529 Overloaded with sk-ant-secret\nAuthorization: Bearer very.secret.token\n",
    );
  } finally {
    console.warn = originalWarn;
  }

  assert.equal(warnings.length, 2);
  assert.match(warnings[0] ?? "", /claude_sdk_stderr/);
  assert.match(warnings[0] ?? "", /API Error: 529 Overloaded/);
  assert.doesNotMatch(warnings.join("\n"), /sk-ant-secret|very\.secret\.token/);
});

test("ensureSdkQuery falls back to DEFAULT_MODEL / DEFAULT_EFFORT on empty first turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { model?: string; effort?: string }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: { opts: { model?: string; effort?: string } | null } = {
    opts: null,
  };
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
    commandBus: {
      markCompleted: () => Promise<void>;
      markFailed: () => Promise<void>;
    };
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
    sink: {
      upsert: (e: TankConversationEvent) => Promise<void>;
      findTurnTerminal?: () => Promise<null>;
    };
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
  assert.equal(
    harness.sdkBackgroundTasks,
    1,
    "foreground Bash/subagent backgrounding must be attempted",
  );
  assert.equal(harness.sdkInterrupts, 1, "SDK interrupt must be called");
  assert.equal(
    harness.events.length,
    1,
    "exactly one durable terminal must be published",
  );
  assert.equal(harness.events[0]!.type, "turn.interrupted");
  assert.equal(
    (harness.events[0] as { turn_id: string }).turn_id,
    "turn_abc-123",
  );
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
  (r.sink as { findTurnTerminal: () => Promise<null> }).findTurnTerminal =
    async () => null;
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
  assert.equal(
    r.pendingInterrupts.length,
    1,
    "interrupt must be buffered (no matching turn yet)",
  );
  assert.equal(
    harness.events.length,
    0,
    "no durable terminal yet — waiting for the matching submit_turn",
  );
  assert.deepEqual(
    harness.bus,
    [],
    "no ack/fail yet — the JetStream record is parked",
  );

  // Now the matching submit_turn lands.
  await r.acceptCommandTurn({
    type: "submit_turn",
    id: "cmd-2",
    prompt: "hello",
    client_nonce: "abc-123",
    target_turn_id: "abc-123",
  });

  assert.equal(
    r.pendingInterrupts.length,
    0,
    "buffer must drain on matching submit_turn",
  );
  assert.equal(
    harness.sdkInterrupts,
    0,
    "SDK must not be interrupted — it was never fed the prompt",
  );
  assert.equal(
    sdkFed.length,
    0,
    "SDK userQueue must not receive the aborted-before-start turn",
  );
  const terminals = harness.events.filter(
    (event) => event.type === "turn.interrupted",
  );
  assert.equal(
    terminals.length,
    1,
    "synthetic turn.interrupted must be published",
  );
  assert.equal(
    (terminals[0] as { payload?: { reason?: string } }).payload?.reason,
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

  assert.equal(
    harness.sdkInterrupts,
    1,
    "SDK interrupt must still be called regardless of publish outcome",
  );
  assert.equal(
    harness.events.length,
    1,
    "exactly one durable terminal must eventually land",
  );
  assert.equal(
    harness.events[0]!.type,
    "turn.failed",
    "fallback shape is turn.failed",
  );
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string } }).payload?.reason,
    "publish_interrupt_failed",
    "fallback reason must name the cause so the post-mortem isn't a mystery",
  );
  assert.deepEqual(
    harness.bus,
    ["fail"],
    "command bus must be marked failed when the happy terminal didn't land",
  );
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
  assert.equal(
    harness.events.length,
    0,
    "no new terminal — the natural terminal already exists",
  );
  assert.deepEqual(
    harness.bus,
    ["ack"],
    "interrupt command still acked (it was delivered correctly)",
  );
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
    rate_limit_info: {
      status: "rejected",
      rateLimitType: "five_hour",
      resetsAt: 1790000000000,
    },
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(
    harness.events.length,
    1,
    "rate limit must emit exactly one durable terminal",
  );
  assert.equal(harness.events[0]!.type, "turn.failed");
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string; error?: string } })
      .payload?.reason,
    "provider_rate_limit",
  );
  assert.match(
    (harness.events[0] as { payload?: { error?: string } }).payload?.error ??
      "",
    /retry_after_ms=60000/,
  );
  assert.match(
    (harness.events[0] as { payload?: { error?: string } }).payload?.error ??
      "",
    /status=rejected; rateLimitType=five_hour; resetsAt=1790000000000/,
  );
  assert.deepEqual(
    harness.bus,
    ["ack"],
    "submit command must ack after the durable rate-limit terminal",
  );
});

test("Claude rate_limit_event with allowed primary quota does not fail the active turn", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    handleEvent: (message: unknown) => Promise<void>;
  };
  const activeTurn = {
    turnID: "turn_allowed-overage-rejected",
    clientNonce: "allowed-overage-rejected",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
    commandRecord: { id: "submit-1", type: "submit_turn" },
    stopCommandHeartbeat: () => harness.sdkControlCalls.push("stop-heartbeat"),
  };
  r.activeTurn = activeTurn;

  await r.handleEvent({
    type: "rate_limit_event",
    uuid: "rate-limit-allowed-overage-rejected",
    rate_limit_info: {
      status: "allowed",
      rateLimitType: "five_hour",
      resetsAt: 1780659000,
      overageStatus: "rejected",
      overageDisabledReason: "org_level_disabled",
      isUsingOverage: false,
    },
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(harness.events.length, 0, "allowed primary quota is not a durable turn terminal");
  assert.deepEqual(harness.bus, [], "submit command must stay in flight for the provider turn to continue");
  assert.equal(r.activeTurn, activeTurn, "the active turn must remain owned by the runner");
});

test("Claude api_retry rate_limit stall fails the turn durably once the no-progress window elapses", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    providerRetryStallMs: number;
    handleEvent: (message: unknown) => Promise<void>;
  };
  // Collapse the no-progress window so the second frame trips it
  // deterministically without sleeping for the production default.
  r.providerRetryStallMs = 0;
  r.activeTurn = {
    turnID: "turn_retry-stalled",
    clientNonce: "retry-stalled",
    text: "hello",
    // started:false — the 638 pathology: claimed but the SDK never produced
    // a first frame, only api_retry.
    started: false,
    interrupted: false,
    terminalEmitted: false,
    commandRecord: { id: "submit-1", type: "submit_turn" },
    stopCommandHeartbeat: () => harness.sdkControlCalls.push("stop-heartbeat"),
  };

  // First api_retry arms the stall window; no terminal yet.
  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "rate_limit",
    uuid: "retry-1",
  });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(
    harness.events.length,
    0,
    "a single retry frame must not fail the turn",
  );
  assert.deepEqual(
    harness.bus,
    [],
    "command stays in flight while the SDK is still retrying",
  );

  // Second api_retry, past the (zeroed) window, forces the durable terminal.
  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "rate_limit",
    uuid: "retry-2",
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(
    harness.events.length,
    1,
    "a sustained rate-limit retry stall must emit exactly one durable terminal",
  );
  assert.equal(harness.events[0]!.type, "turn.failed");
  assert.equal(
    (harness.events[0] as { payload?: { reason?: string } }).payload?.reason,
    "provider_rate_limit",
  );
  assert.match(
    (harness.events[0] as { payload?: { error?: string } }).payload?.error ??
      "",
    /retry stall/,
  );
  assert.deepEqual(
    harness.bus,
    ["ack"],
    "the submit command must ack after the durable stall terminal",
  );
  assert.equal(
    r.activeTurn,
    null,
    "the stalled turn is released after the terminal",
  );
});

test("Claude api_retry rate_limit resets after real turn progress so a later isolated retry does not fail the turn", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    providerRetryStall: unknown;
    providerRetryStallMs: number;
    resetProviderRetryStall: () => void;
    handleEvent: (message: unknown) => Promise<void>;
  };
  r.providerRetryStallMs = 0;
  const activeTurn = {
    turnID: "turn_recovers",
    clientNonce: "recovers",
    text: "hello",
    started: true,
    interrupted: false,
    terminalEmitted: false,
    commandRecord: { id: "submit-1", type: "submit_turn" },
    stopCommandHeartbeat: () => harness.sdkControlCalls.push("stop-heartbeat"),
  };
  r.activeTurn = activeTurn;

  // A retry arms the window...
  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "rate_limit",
    uuid: "retry-1",
  });
  assert.notEqual(r.providerRetryStall, null, "the stall window is armed");
  // ...then the turn makes real progress, which must clear it.
  r.resetProviderRetryStall();
  assert.equal(r.providerRetryStall, null, "progress clears the stall window");

  // A later isolated retry only re-arms (count resets to 1); it must not fail.
  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "rate_limit",
    uuid: "retry-2",
  });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(
    harness.events.length,
    0,
    "an isolated retry after progress must re-arm, not immediately fail",
  );
  assert.equal(r.activeTurn, activeTurn, "the active turn stays owned");
});

test("Claude api_retry overloaded is observed but never forces a turn terminal", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    activeTurn: unknown;
    providerRetryStallMs: number;
    handleEvent: (message: unknown) => Promise<void>;
  };
  r.providerRetryStallMs = 0;
  const activeTurn = {
    turnID: "turn_overloaded",
    clientNonce: "overloaded",
    text: "hello",
    started: false,
    interrupted: false,
    terminalEmitted: false,
    commandRecord: { id: "submit-1", type: "submit_turn" },
    stopCommandHeartbeat: () => harness.sdkControlCalls.push("stop-heartbeat"),
  };
  r.activeTurn = activeTurn;

  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "overloaded",
    uuid: "retry-1",
  });
  await r.handleEvent({
    type: "system",
    subtype: "api_retry",
    error: "overloaded",
    uuid: "retry-2",
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(
    harness.events.length,
    0,
    "overloaded retries are transient; the SDK recovers and we must not fail the turn",
  );
  assert.deepEqual(harness.bus, []);
  assert.equal(r.activeTurn, activeTurn, "the active turn stays owned by the runner");
});

test("acceptInterrupt with missing target: fails command explicitly instead of silently acking", async () => {
  const { runner, harness } = makeInterruptHarness();
  const r = runner as unknown as {
    acceptInterrupt: (record: unknown) => Promise<void>;
  };

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "",
    client_nonce: "",
  });

  assert.deepEqual(
    harness.bus,
    ["fail"],
    "missing target must produce a visible failure, not a silent ack",
  );
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
  assert.equal(
    result.event,
    tiny,
    "should return the same reference when no work is needed",
  );
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
  const result = truncateEventIfOversized(event, {
    maxBytes: 50_000,
    stringThreshold: 1024,
  });
  assert.equal(result.truncated, true);
  assert.equal(
    result.payloadDropped,
    undefined,
    "envelope should be preserved when string truncation suffices",
  );
  assert.ok(
    result.finalBytes <= 50_000,
    `result must fit under budget; got ${result.finalBytes}`,
  );
  // Original event is not mutated; clone-modify is required for shared
  // event objects.
  assert.equal((event.payload as { output: string }).output.length, 200_000);
  // Truncated string carries the original size + a sha256_16 prefix.
  const truncated = (result.event.payload as { output: string }).output;
  assert.ok(
    truncated.startsWith("[truncated: 200000 bytes"),
    `unexpected marker: ${truncated.slice(0, 80)}`,
  );
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
  const event = {
    event_id: "e1",
    type: "item.completed",
    turn_id: "t1",
    payload,
  };
  const result = truncateEventIfOversized(event, {
    maxBytes: 5_000,
    stringThreshold: 100_000,
  });
  assert.equal(result.truncated, true);
  assert.equal(
    result.payloadDropped,
    true,
    "must fall back to payload-dropped when strings alone can't fit",
  );
  assert.ok(result.finalBytes <= 5_000);
  assert.equal(
    (result.event.payload as unknown as { __payload_dropped: boolean })
      .__payload_dropped,
    true,
  );
  // Envelope fields stay intact so the durable ledger still records
  // "an event of this type existed for this turn."
  assert.equal(result.event.type, "item.completed");
  assert.equal(result.event.event_id, "e1");
  assert.equal(result.event.turn_id, "t1");
});

test("claudeRestartClosureEvent closes an orphaned task honestly and deterministically", () => {
  const cfg = runnerConfig();
  const task = {
    taskID: "task-orphan",
    turnID: "turn-origin",
    startedEventID: "turn-origin:shell_task.started:abc",
  };
  const first = claudeRestartClosureEvent(cfg, task);
  assert.equal(first.type, "shell_task.exited");
  assert.equal(first.turn_id, "turn-origin");
  // Honest closure: the restart severed the SDK task registry, so completion
  // was never observed and must not be claimed.
  assert.equal((first.payload as { status?: string }).status, "unknown");
  assert.equal(
    (first.payload as { completion_source?: string }).completion_source,
    "runner_restart",
  );
  // Deterministic observation identity: a second restart re-derives the SAME
  // event id, so the wake registration dedupes instead of stacking
  // generations.
  const second = claudeRestartClosureEvent(cfg, task);
  assert.equal(first.event_id, second.event_id);
  assert.ok(String(first.event_id ?? "").length > 0);
});
