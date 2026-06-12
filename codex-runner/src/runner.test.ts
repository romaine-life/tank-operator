import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  codexQuestionsToTankShape,
  dispatch,
  interruptTargetMatchesTurn,
  Runner,
  takePendingInterruptForTurn,
  threadOptionsForCommand,
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
    natsCommandStream: "TANK_SESSION_COMMANDS",
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

// codexQuestionsToTankShape is the codex-side parity for the AskUserQuestion
// pause: the app-server question shape is normalized to the Tank-canonical
// questions that ride durable turn.awaiting_input. The user's answer returns to
// the paused app-server request through input_reply.
test("codexQuestionsToTankShape maps codex app-server questions into the Tank shape", () => {
  assert.deepEqual(
    codexQuestionsToTankShape([
      {
        id: "q1",
        question: "Which auth method?",
        header: "Auth",
        isOther: true,
        isSecret: false,
        options: [
          { label: "OAuth", description: "Use OAuth" },
          { label: "API key" },
        ],
      },
    ] as never),
    [
      {
        question: "Which auth method?",
        header: "Auth",
        multiSelect: false,
        options: [
          { label: "OAuth", description: "Use OAuth" },
          { label: "API key" },
        ],
        allowFreeForm: true,
        secret: false,
      },
    ],
  );
});

test("acceptInputReply parks under heartbeat when the app-server request is not recreated yet", async () => {
  // Issue #1078 item 3: the old nak(1s) loop burned the control plane's
  // max_deliver budget in ~10s while the redelivered submit_turn replayed
  // the whole turn — the user's durable answer was lost forever. The
  // answer now parks under a JetStream heartbeat until the re-asked pause
  // registers (drained by pauseTurnForInput).
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
      assert.fail("early input_reply should not complete without a pending request");
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
    target_timeline_id: "turn-active:item:req_ask",
    target_provider_item_id: "req_ask",
    answers: { "Proceed?": ["Yes"] },
    nak() {
      nakked = true;
    },
  });

  assert.equal(nakked, false, "parking must not burn max_deliver budget");
  assert.equal(heartbeats, 1);
  assert.equal(runner.parkedInputReplies.length, 1);
});

test("codexQuestionsToTankShape tolerates pure free-form (null options) and drops malformed entries", () => {
  assert.deepEqual(
    codexQuestionsToTankShape([
      { id: "q", question: "Say something", isOther: true, options: null },
      { id: "bad", question: "", options: [] },
    ] as never),
    [
      {
        question: "Say something",
        multiSelect: false,
        options: [],
        allowFreeForm: true,
        secret: false,
      },
    ],
  );
});

test("acceptInputReply delivers free-form Other text to codex app-server", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    acceptInputReply: (record: unknown) => Promise<void>;
    rotateTurnForInputReply: (turn: unknown, record: unknown) => Promise<void>;
    pendingInputReplies: Map<string, { resolve: (value: unknown) => void }>;
    commandBus: { markCompleted: (record: unknown) => Promise<void>; markFailed: () => Promise<void> };
  };
  let resolved: unknown;
  let completedRecord: unknown;
  runner.pendingInputReplies = new Map([
    [
      "turn-active\x1fturn-active:item:req_ask\x1freq_ask",
      {
        resolve(value: unknown) {
          resolved = value;
        },
      },
    ],
  ]);
  runner.commandBus = {
    async markCompleted(record) {
      completedRecord = record;
    },
    async markFailed() {
      assert.fail("input_reply should resolve the pending request");
    },
  };
  runner.rotateTurnForInputReply = async (_turn, record) => {
    assert.equal((record as { client_nonce?: string }).client_nonce, "answer-continuation");
  };

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:req_ask",
    target_provider_item_id: "req_ask",
    answers: { "Proceed?": ["Other"] },
    annotations: { "Proceed?": { notes: "Use the dedicated test database." } },
  });

  assert.deepEqual(resolved, {
    answers: { "Proceed?": { answers: ["Use the dedicated test database."] } },
  });
  assert.ok(completedRecord, "input_reply command should be acked after resolving the request");
});

test("acceptInputReply keeps selected codex labels and appends free-form context", async () => {
  const runner = new Runner(runnerConfig()) as unknown as {
    acceptInputReply: (record: unknown) => Promise<void>;
    rotateTurnForInputReply: (turn: unknown, record: unknown) => Promise<void>;
    pendingInputReplies: Map<string, { resolve: (value: unknown) => void }>;
    commandBus: { markCompleted: () => Promise<void>; markFailed: () => Promise<void> };
  };
  let resolved: unknown;
  runner.pendingInputReplies = new Map([
    [
      "turn-active\x1fturn-active:item:req_ask\x1freq_ask",
      {
        resolve(value: unknown) {
          resolved = value;
        },
      },
    ],
  ]);
  runner.commandBus = {
    async markCompleted() {},
    async markFailed() {
      assert.fail("input_reply should resolve the pending request");
    },
  };
  runner.rotateTurnForInputReply = async (_turn, record) => {
    assert.equal((record as { client_nonce?: string }).client_nonce, "answer-continuation");
  };

  await runner.acceptInputReply({
    type: "input_reply",
    client_nonce: "answer-continuation",
    target_turn_id: "turn-active",
    target_timeline_id: "turn-active:item:req_ask",
    target_provider_item_id: "req_ask",
    answers: { "Proceed?": ["Apply the additive table now"] },
    annotations: { "Proceed?": { notes: "Use the real migration path." } },
  });

  assert.deepEqual(resolved, {
    answers: { "Proceed?": { answers: ["Apply the additive table now", "Use the real migration path."] } },
  });
});

test("threadOptionsForCommand forwards first-turn Codex model and effort", () => {
  const opts = threadOptionsForCommand(runnerConfig(), {
    model: "gpt-5.5",
    effort: "xhigh",
  } as unknown as Parameters<typeof threadOptionsForCommand>[1]);

  assert.equal(opts.workingDirectory, "/workspace");
  assert.equal(opts.model, "gpt-5.5");
  assert.equal(opts.modelReasoningEffort, "xhigh");
  assert.equal(opts.sandboxMode, "danger-full-access");
  assert.equal(opts.approvalPolicy, "never");
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

// ───────────────────────────────────────────────────────────────────────────
// romaine-life/tank-operator#532 — four-outcome contract for accepted interrupts
// on the codex-runner. Sibling of claude-runner's contract tests; same shape
// but exercising the codex-runner's acceptInterrupt → orphanInterrupts /
// pendingInterrupts paths.
// ───────────────────────────────────────────────────────────────────────────

function makeCodexInterruptHarness(): {
  runner: Runner;
  events: TankConversationEvent[];
  bus: string[];
  setSinkFailureCount: (n: number) => void;
} {
  const events: TankConversationEvent[] = [];
  const bus: string[] = [];
  let sinkFailuresLeft = 0;
  const runner = new Runner(runnerConfig());
  const internals = runner as unknown as {
    sink: {
      upsert: (e: TankConversationEvent) => Promise<void>;
      findTurnTerminal?: (turnID: string) => Promise<TankConversationEvent | null>;
    };
    commandBus: {
      markCompleted: (r?: unknown) => Promise<void>;
      markFailed: (r?: unknown, err?: unknown) => Promise<void>;
      startCommandHeartbeat: (r?: unknown) => () => void;
    };
  };
  internals.sink = {
    async upsert(event: TankConversationEvent) {
      if (sinkFailuresLeft > 0) {
        sinkFailuresLeft -= 1;
        throw new Error("simulated dispatch failure");
      }
      events.push(event);
    },
    async findTurnTerminal() {
      return null;
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
      return () => {};
    },
  };
  return {
    runner,
    events,
    bus,
    setSinkFailureCount(n: number) {
      sinkFailuresLeft = n;
    },
  };
}

test("acceptInterrupt during in-flight codex turn: aborts immediately, queues record on currentTurn", async () => {
  const { runner, bus, events } = makeCodexInterruptHarness();
  const r = runner as unknown as {
    currentTurn: { turnID: string; clientNonce: string; interruptRecords?: unknown[] } | null;
    currentAbort: { abort: () => void } | null;
    interruptRequested: boolean;
    acceptInterrupt: (record: unknown) => Promise<void>;
  };
  let aborted = false;
  r.currentAbort = { abort: () => { aborted = true; } };
  r.currentTurn = { turnID: "turn_abc-123", clientNonce: "abc-123" };

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "abc-123",
    client_nonce: "abc-123",
  });

  assert.equal(aborted, true, "AbortController must fire");
  assert.equal(r.interruptRequested, true, "interruptRequested flag must be set");
  assert.equal(r.currentTurn?.interruptRecords?.length, 1, "record queued on currentTurn for terminal-time ack");
  // The terminal publish + ack happens in the run-loop catch branch, not
  // in acceptInterrupt. So no immediate ack/event here.
  assert.equal(events.length, 0);
  assert.equal(bus.length, 0);
});

test("acceptInterrupt with no matching turn: orphan-buffered with timer + heartbeat", async () => {
  const { runner } = makeCodexInterruptHarness();
  const r = runner as unknown as {
    currentTurn: unknown;
    pendingCommandTurnTargets: Set<string>;
    orphanInterrupts: Array<{ record: unknown; targetKey: string; orphanTimer: ReturnType<typeof setTimeout> }>;
    acceptInterrupt: (record: unknown) => Promise<void>;
  };
  r.currentTurn = null;
  r.pendingCommandTurnTargets.clear();

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "abc-123",
    client_nonce: "abc-123",
  });

  assert.equal(r.orphanInterrupts.length, 1, "interrupt with no matching turn must be orphan-buffered, not silently ack'd");
  assert.equal(r.orphanInterrupts[0]!.targetKey, "abc-123");
  // Clear the timer so the test doesn't leak a setTimeout.
  clearTimeout(r.orphanInterrupts[0]!.orphanTimer);
});

test("trackCommandTurnTarget drains matching orphan interrupts into pendingInterrupts", async () => {
  const { runner } = makeCodexInterruptHarness();
  const r = runner as unknown as {
    currentTurn: unknown;
    pendingCommandTurnTargets: Set<string>;
    orphanInterrupts: Array<{ record: unknown; targetKey: string; orphanTimer: ReturnType<typeof setTimeout> }>;
    pendingInterrupts: unknown[];
    acceptInterrupt: (record: unknown) => Promise<void>;
    trackCommandTurnTarget: (clientNonce: string) => void;
  };
  r.currentTurn = null;

  // Stop click arrives first (target_turn_id = bare uuid form, which
  // is what the backend's /interrupt handler sends).
  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "abc-123",
    client_nonce: "abc-123",
  });
  assert.equal(r.orphanInterrupts.length, 1, "orphan-buffered (no matching submit yet)");

  // Then the submit_turn lands and the data-plane consumer tracks it.
  r.trackCommandTurnTarget("abc-123");

  assert.equal(r.orphanInterrupts.length, 0, "orphan buffer drained");
  assert.equal(r.pendingInterrupts.length, 1, "moved into pendingInterrupts for dequeue-side application");
});

test("acceptInterrupt with missing target: fails command explicitly instead of silently acking", async () => {
  const { runner, bus, events } = makeCodexInterruptHarness();
  const r = runner as unknown as { acceptInterrupt: (record: unknown) => Promise<void> };

  await r.acceptInterrupt({
    type: "interrupt_turn",
    id: "cmd-1",
    target_turn_id: "",
    client_nonce: "",
  });

  assert.deepEqual(bus, ["fail"], "missing target must produce a visible failure, not a silent ack");
  assert.equal(events.length, 0);
});
