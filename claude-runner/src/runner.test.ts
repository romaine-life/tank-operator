import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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
    finalAnswer: {
      timelineIDs: ["turn_askq-test-1780648459:item:final"],
      providerItemIDs: ["assistant:final"],
    },
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
  assert.ok(handoff.invocation.payload);
  assert.equal(
    handoff.invocation.payload.question_turn_id,
    handoff.questionTurnID,
  );
  assert.equal(
    handoff.invocation.payload.question_timeline_id,
    handoff.questionTimelineID,
  );
  assert.equal(handoff.invocation.payload.question_page, 1);
  assert.deepEqual(handoff.awaitingInput.payload.asking_turn_final_answer, {
    timeline_ids: ["turn_askq-test-1780648459:item:final"],
    provider_item_ids: ["assistant:final"],
  });
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
    natsCommandStream: "TANK_SESSION_COMMANDS",
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

test("permission_denied frames become durable failed tool items", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    handleEvent: (message: unknown) => Promise<void>;
    activeTurn: unknown;
    sink: { upsert: (event: TankConversationEvent) => Promise<void> };
  };
  const events: TankConversationEvent[] = [];
  runner.activeTurn = {
    turnID: "turn-active",
    clientNonce: "turn-active",
    started: true,
    interrupted: false,
    terminalEmitted: false,
  };
  runner.sink = {
    async upsert(event) {
      events.push(event);
    },
  };

  await runner.handleEvent({
    type: "system",
    subtype: "permission_denied",
    tool_name: "mcp__github__create_pull_request",
    tool_use_id: "toolu_pr",
    agent_id: "agent-1",
    decision_reason_type: "rule",
    decision_reason: "not allowed",
    message: "Permission denied",
    uuid: "evt-denied",
  });

  assert.equal(events.length, 1);
  assert.equal(events[0]?.type, "item.failed");
  assert.equal(events[0]?.provider_item_id, "toolu_pr");
  assert.deepEqual(events[0]?.payload?.permission_denied, {
    agent_kind: "subagent",
    decision: "rule",
    decision_reason: "not allowed",
  });
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

// The Tank-owned AskUserQuestion MCP tool publishes durable turn.awaiting_input,
// keeps the turn active, and resolves only when input_reply arrives for the
// same durable question target.
test("Tank AskUserQuestion MCP tool pauses the active turn and resumes from input_reply", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    handleTankAskUserQuestion: (input: unknown) => Promise<{
      isError?: boolean;
      content: Array<{ type: string; text?: string }>;
      structuredContent?: { answers?: Record<string, string> };
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
    finalAnswer: {
      timelineIDs: ["turn-active:item:final"],
      providerItemIDs: ["assistant:final"],
    },
    commandRecord: {},
  };

  const resultPromise = runner.handleTankAskUserQuestion({
    questions: [
      { question: "Which auth method?", options: [{ label: "OAuth" }] },
    ],
  });
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
    "AskUserQuestion is the Tank-visible response, but the provider command stays in flight for MCP reply delivery",
  );
  const payload = (awaiting as {
    payload?: {
      provider_item_id?: string;
      provider_timeline_id?: string;
      asking_turn_final_answer?: unknown;
    };
  }).payload;
  assert.deepEqual(payload?.asking_turn_final_answer, {
    timeline_ids: ["turn-active:item:final"],
    provider_item_ids: ["assistant:final"],
  });

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: payload?.provider_timeline_id,
    target_provider_item_id: payload?.provider_item_id,
    answers: { "Which auth method?": ["OAuth"] },
    annotations: { "Which auth method?": { notes: "matches the IdP" } },
  });
  const result = await resultPromise;
  assert.equal(result.isError, undefined);
  assert.match(result.content[0]?.text ?? "", /User answered/);
  assert.deepEqual(result.structuredContent?.answers, {
    "Which auth method?": "OAuth\n\nmatches the IdP",
  });
  assert.ok(
    completedRecord,
    "input_reply command should be acked after resolving the tool",
  );
});

test("Tank AskUserQuestion MCP tool rejects the retired top-level question shorthand", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    handleTankAskUserQuestion: (input: unknown) => Promise<{
      isError?: boolean;
      content: Array<{ type: string; text?: string }>;
    }>;
    activeTurn: unknown;
    publishTerminalWithRetry: (
      event: TankConversationEvent,
    ) => Promise<boolean>;
    sink: { upsert: (event: TankConversationEvent) => Promise<void> };
  };

  runner.activeTurn = {
    turnID: "turn-active",
    clientNonce: "turn-active",
    terminalEmitted: false,
    commandRecord: {},
  };
  runner.sink = {
    async upsert() {
      assert.fail("invalid AskUserQuestion input must not publish events");
    },
  };
  runner.publishTerminalWithRetry = async () => {
    assert.fail("invalid AskUserQuestion input must not publish a pause");
    return false;
  };

  const result = await runner.handleTankAskUserQuestion({
    question: "Proceed?",
    options: [{ label: "Yes" }],
  });

  assert.equal(result.isError, true);
  assert.match(
    result.content[0]?.text ?? "",
    /requires questions: a non-empty array/,
  );
});

test("Tank AskUserQuestion MCP tool delivers free-form Other text instead of synthetic label", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    handleTankAskUserQuestion: (input: unknown) => Promise<{
      structuredContent?: { answers?: Record<string, string> };
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

  const resultPromise = runner.handleTankAskUserQuestion({
    questions: [
      {
        question: "Proceed?",
        allowFreeForm: true,
        options: [{ label: "Yes" }],
      },
    ],
  });
  await new Promise((resolve) => setImmediate(resolve));
  const pending = (runner as unknown as {
    pendingInputReplies: Map<
      string,
      { providerItemID: string; timelineID: string }
    >;
  }).pendingInputReplies.values().next().value;
  assert.ok(pending, "AskUserQuestion should park a pending input reply");

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: pending.timelineID,
    target_provider_item_id: pending.providerItemID,
    answers: { "Proceed?": ["Other"] },
    annotations: { "Proceed?": { notes: "Use the dedicated test database." } },
  });

  const result = await resultPromise;
  assert.deepEqual(result.structuredContent?.answers, {
    "Proceed?": "Use the dedicated test database.",
  });
});

test("acceptInputReply parks under heartbeat when the provider callback is not recreated yet", async () => {
  // Issue #1078 item 3: the old nak(1s) loop exhausted the control plane's
  // max_deliver budget in ~10s while the redelivered submit_turn replayed
  // the whole turn — the user's durable answer was lost and the SDK
  // re-asked. The answer now parks under a JetStream heartbeat until the
  // re-asked pause registers (drained by pauseTurnForInput).
  const runner = new Runner(runnerConfig()) as unknown as {
    acceptInputReply: (record: unknown) => Promise<void>;
    parkedInputReplies: unknown[];
    commandBus: {
      markCompleted: () => Promise<void>;
      markFailed: () => Promise<void>;
      startCommandHeartbeat: () => () => void;
    };
  };
  let heartbeats = 0;
  let nakked = false;
  runner.commandBus = {
    async markCompleted() {
      assert.fail(
        "early input_reply should not complete without a pending callback",
      );
    },
    async markFailed() {
      assert.fail("early input_reply should park instead of failing");
    },
    startCommandHeartbeat() {
      heartbeats++;
      return () => {};
    },
  };

  await runner.acceptInputReply({
    type: "input_reply",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:toolu_ask",
    target_provider_item_id: "toolu_ask",
    answers: { "Proceed?": ["Yes"] },
    nak() {
      nakked = true;
    },
  });

  assert.equal(nakked, false, "parking must not burn max_deliver budget");
  assert.equal(heartbeats, 1);
  assert.equal(runner.parkedInputReplies.length, 1);
});

// ensureSdkQuery is the load-bearing pinning point for model + effort.
// These tests pin the contract:
//   1. First submit_turn with values pins them into SDK Options.
//   2. First submit_turn with empty values falls back to DEFAULT_MODEL /
//      DEFAULT_EFFORT — the wire shape is additive so empty must keep
//      working for legacy clients.
//   3. A subsequent submit_turn whose model/effort differ does NOT relaunch
//      from ensureSdkQuery (Options is sealed on the running query); it
//      SCHEDULES a re-pin (pendingRepin) that performRebuild applies at the
//      next idle turn boundary by rebuilding query() with provider-session
//      resume. model/effort are sealed within a turn, re-pinnable between
//      turns — the mid-session model switch the SPA dropdown drives.
test("ensureSdkQuery pins model + effort from the first submit_turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: {
      model?: string;
      effort?: string;
      continue?: boolean;
      resume?: string;
      stderr?: (data: string) => void;
      permissionMode?: string;
      allowDangerouslySkipPermissions?: boolean;
      toolAliases?: Record<string, string>;
      mcpServers?: Record<string, unknown>;
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
      continue?: boolean;
      resume?: string;
      stderr?: (data: string) => void;
      permissionMode?: string;
      allowDangerouslySkipPermissions?: boolean;
      toolAliases?: Record<string, string>;
      mcpServers?: Record<string, unknown>;
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
  const opts = captured.opts;
  assert.ok(opts, "launchSdkQuery should have been called");
  assert.equal(opts.model, "claude-haiku-4-5");
  assert.equal(opts.effort, "low");
  assert.equal(opts.continue, true);
  assert.equal(opts.resume, undefined);
  assert.equal(typeof opts.stderr, "function");
  assert.equal(opts.permissionMode, "bypassPermissions");
  assert.equal(opts.allowDangerouslySkipPermissions, true);
  assert.equal(opts.toolAliases?.AskUserQuestion, "mcp__tank__AskUserQuestion");
  assert.ok(opts.mcpServers?.tank, "Tank MCP server should be wired");
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

test("ensureSdkQuery resumes explicit provider session id when command carries one", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: {
      model?: string;
      effort?: string;
      continue?: boolean;
      resume?: string;
    }) => unknown;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: {
    opts: {
      model?: string;
      effort?: string;
      continue?: boolean;
      resume?: string;
    } | null;
  } = { opts: null };
  runner.launchSdkQuery = (opts) => {
    captured.opts = opts;
    return { interrupt: () => {} } as unknown;
  };

  runner.ensureSdkQuery({
    id: "cmd-1",
    type: "submit_turn",
    model: "claude-opus-4-8",
    effort: "max",
    provider_session_id: "db0a8b4b-64cd-4a9a-a592-ad5622075dc8",
  });

  const opts = captured.opts;
  assert.ok(opts);
  assert.equal(opts.resume, "db0a8b4b-64cd-4a9a-a592-ad5622075dc8");
  assert.equal(opts.continue, undefined);
  assert.equal(opts.model, "claude-opus-4-8");
  assert.equal(opts.effort, "max");
});

test("ensureSdkQuery falls back to DEFAULT_MODEL / DEFAULT_EFFORT on empty first turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: {
      model?: string;
      effort?: string;
      continue?: boolean;
      resume?: string;
    }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    ensureSdkQuery: (record: unknown) => void;
  };
  const captured: {
    opts: {
      model?: string;
      effort?: string;
      continue?: boolean;
      resume?: string;
    } | null;
  } = { opts: null };
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
  const opts = captured.opts;
  assert.ok(opts);
  assert.equal(opts.model, "claude-opus-4-8");
  assert.equal(opts.effort, "high");
  assert.equal(opts.continue, true);
  assert.equal(opts.resume, undefined);
});

test("ensureSdkQuery schedules a re-pin on a differing subsequent turn", () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: { model?: string; effort?: string }) => unknown;
    pinnedModel: string | null;
    pinnedEffort: string | null;
    sdkQuery: unknown;
    ensureSdkQuery: (record: unknown) => void;
    pendingRepin: { model: string; effort: string } | null;
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
  // Second turn requests a different model + effort. ensureSdkQuery must NOT
  // relaunch here (Options is sealed on the running query) and keeps the
  // pinned values for now — but it schedules the change as pendingRepin, which
  // performRebuild applies at the next idle boundary by rebuilding the query.
  // This is the mid-session model switch; the old "silently ignore" behavior
  // is gone.
  runner.ensureSdkQuery({
    id: "cmd-2",
    type: "submit_turn",
    model: "claude-haiku-4-5",
    effort: "low",
  });

  assert.equal(
    launchCalls,
    1,
    "ensureSdkQuery must not relaunch; rebuild is deferred to performRebuild",
  );
  assert.equal(
    runner.pinnedModel,
    "claude-opus-4-7",
    "pinned values stay until the re-pin is applied",
  );
  assert.equal(runner.pinnedEffort, "high");
  assert.deepEqual(
    runner.pendingRepin,
    { model: "claude-haiku-4-5", effort: "low" },
    "the differing turn schedules a re-pin",
  );
});

test("performRebuild rebuilds with resume + new model and tears down the old query", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    launchSdkQuery: (opts: {
      model?: string;
      effort?: string;
      resume?: string;
      continue?: boolean;
    }) => unknown;
    ensureSdkQuery: (record: unknown) => void;
    performRebuild: () => Promise<void>;
    reportedProviderSessionID: string;
    reportedContextWindowTokens: number | null;
    pendingRepin: { model: string; effort: string } | null;
    pinnedModel: string | null;
  };
  const launches: {
    model?: string;
    effort?: string;
    resume?: string;
    continue?: boolean;
  }[] = [];
  let interrupts = 0;
  runner.launchSdkQuery = (opts) => {
    launches.push(opts);
    return {
      interrupt: () => {
        interrupts += 1;
      },
    } as unknown;
  };

  // First turn pins Haiku and builds the query.
  runner.ensureSdkQuery({
    id: "cmd-1",
    type: "submit_turn",
    model: "claude-haiku-4-5",
    effort: "low",
  });
  // The runner has since latched the live conversation id + a context window.
  runner.reportedProviderSessionID = "sess-live-123";
  runner.reportedContextWindowTokens = 200000;
  // A differing turn schedules the re-pin; performRebuild applies it.
  runner.ensureSdkQuery({
    id: "cmd-2",
    type: "submit_turn",
    model: "claude-opus-4-8",
    effort: "high",
  });
  await runner.performRebuild();

  assert.equal(launches.length, 2, "performRebuild constructs a second query");
  assert.equal(launches[1].model, "claude-opus-4-8", "rebuild uses the new model");
  assert.equal(launches[1].effort, "high");
  assert.equal(
    launches[1].resume,
    "sess-live-123",
    "rebuild resumes the live conversation id",
  );
  assert.equal(
    launches[1].continue,
    undefined,
    "resume, not continue, when a session id is known",
  );
  assert.equal(interrupts, 1, "the old query is interrupted");
  assert.equal(
    runner.pinnedModel,
    "claude-opus-4-8",
    "pinned model updated after rebuild",
  );
  assert.equal(
    runner.reportedContextWindowTokens,
    null,
    "context-window latch reset for the new model",
  );
  assert.equal(runner.pendingRepin, null, "pending re-pin cleared");
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
  const tmp = mkdtempSync(path.join(tmpdir(), "claude-runner-test-"));
  const tokenPath = path.join(tmp, "token");
  writeFileSync(tokenPath, "runtime-token\n", "utf8");
  const fetchCalls: Array<{ url: string; init?: RequestInit; body: Record<string, unknown> }> = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (url: string | URL | Request, init?: RequestInit) => {
    fetchCalls.push({
      url: String(url),
      init,
      body: JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>,
    });
    return new Response("{}", { status: 200 });
  }) as typeof fetch;
  const { runner, harness } = makeInterruptHarness({
    ...runnerConfig(),
    operatorInternalURL: "http://operator.internal",
    operatorTokenPath: tokenPath,
  });
  const r = runner as unknown as {
    activeTurn: unknown;
    providerRetryStallMs: number;
    handleEvent: (message: unknown) => Promise<void>;
  };
  try {
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
    assert.equal(fetchCalls.length, 0, "a single retry frame must not report rate-limit state");

    // Second api_retry, past the (zeroed) window, forces the durable terminal.
    await r.handleEvent({
      type: "system",
      subtype: "api_retry",
      error: "rate_limit",
      uuid: "retry-2",
    });
    for (let i = 0; i < 5 && fetchCalls.length === 0; i += 1) {
      await new Promise((resolve) => setImmediate(resolve));
    }

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
    assert.equal(fetchCalls.length, 1, "the terminal retry stall reports provider rate-limit state");
    assert.equal(
      fetchCalls[0]!.url,
      "http://operator.internal/api/internal/sessions/63/runtime-config",
    );
    assert.equal(
      fetchCalls[0]!.init?.headers instanceof Headers
        ? fetchCalls[0]!.init.headers.get("Authorization")
        : (fetchCalls[0]!.init?.headers as Record<string, string>)?.Authorization,
      "Bearer runtime-token",
    );
    assert.deepEqual(fetchCalls[0]!.body.provider_rate_limit_info, {
      provider: "claude",
      status: "rejected",
      rateLimitType: "api_retry",
      uuid: "retry-2",
    });
  } finally {
    globalThis.fetch = originalFetch;
    rmSync(tmp, { recursive: true, force: true });
  }
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
