// Tests for the #1078 runner-correctness cluster: natural-terminal publish
// hardening (item 1), stop-during-AskUserQuestion (item 2), durable-answer
// recovery across restarts (item 3), redelivery dedup (item 6), the orphan
// already-terminal check (item 6), dispatch retry (item 6), and bounded
// tracking maps (item 6).
import { test } from "node:test";
import assert from "node:assert/strict";

import {
  boundedMapSet,
  boundedSetAdd,
  dispatch,
  Runner,
  type PendingTurn,
} from "./runner.js";
import {
  askUserQuestionHandoffEvents,
  turnEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";

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

type UpsertedEvent = Record<string, unknown>;

function builtTurnEvent(
  type: "turn.started" | "turn.completed",
  turnID = "turn_t1",
): TankConversationEvent {
  return turnEvent({
    sessionID: "63",
    turnID,
    clientNonce: "nonce-t1",
    source: "claude",
    type,
  }) as TankConversationEvent;
}

function stubSink(opts?: {
  failUpserts?: number;
  failAlways?: boolean;
  terminal?: unknown;
}) {
  const upserts: UpsertedEvent[] = [];
  let failRemaining = opts?.failUpserts ?? 0;
  const sink = {
    upsert: async (event: UpsertedEvent) => {
      if (opts?.failAlways || failRemaining > 0) {
        failRemaining--;
        throw new Error("publish failed (stub)");
      }
      upserts.push(event);
    },
    findTurnTerminal: async () => opts?.terminal ?? null,
  };
  return { sink, upserts };
}

function stubCommandBus() {
  const calls = {
    completed: [] as unknown[],
    failed: [] as { record: unknown; err: unknown }[],
    heartbeatsStarted: 0,
    heartbeatsStopped: 0,
  };
  const bus = {
    markCompleted: async (record: unknown) => {
      calls.completed.push(record);
    },
    markFailed: async (record: unknown, err: unknown) => {
      calls.failed.push({ record, err });
    },
    startCommandHeartbeat: () => {
      calls.heartbeatsStarted++;
      return () => {
        calls.heartbeatsStopped++;
      };
    },
    attemptsExceeded: () => false,
  };
  return { bus, calls };
}

function commandRecord(extra: Record<string, unknown> = {}) {
  const naks: number[] = [];
  return {
    record: {
      id: "cmd-1",
      prompt: "do the thing",
      client_nonce: "nonce-t1",
      nak: (ms: number) => {
        naks.push(ms);
      },
      ...extra,
    },
    naks,
  };
}

function pendingTurn(overrides: Partial<PendingTurn> = {}): PendingTurn {
  return {
    turnID: turnIDForClientNonce("nonce-t1"),
    clientNonce: "nonce-t1",
    text: "do the thing",
    started: true,
    interrupted: false,
    terminalEmitted: false,
    ...overrides,
  } as PendingTurn;
}

type RunnerInternals = {
  sink: unknown;
  commandBus: unknown;
  activeTurn: PendingTurn | null;
  pendingTurns: PendingTurn[];
  pendingInputReplies: Map<string, Record<string, unknown>>;
  parkedInputReplies: unknown[];
  pendingInterrupts: unknown[];
  sdkQuery: unknown;
  publishTurnTerminalOrDefer: (
    turn: PendingTurn,
    event: TankConversationEvent,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ) => Promise<boolean>;
  reattachRedeliveredCommand: (
    turn: PendingTurn,
    record: unknown,
  ) => Promise<void>;
  acceptCommandTurn: (record: unknown) => Promise<void>;
  acceptInputReply: (record: unknown) => Promise<void>;
  applyInterruptToTurn: (
    record: unknown,
    turn: PendingTurn,
    reason: string,
  ) => Promise<void>;
  findTurnForKey: (key: string) => PendingTurn | null;
  drainParkedInputRepliesFor: (turn: PendingTurn) => Promise<void>;
  expireBufferedInterrupt: (record: unknown) => Promise<void>;
  drainTurnsForFatalExit: () => Promise<void>;
  userQueue: { push: (m: unknown) => void };
};

function makeRunner(
  sink: unknown,
  bus: unknown,
): RunnerInternals {
  const runner = new Runner(runnerConfig()) as unknown as RunnerInternals;
  runner.sink = sink;
  runner.commandBus = bus;
  return runner;
}

function questionEntryFor(turn: PendingTurn) {
  const handoff = askUserQuestionHandoffEvents({
    sessionID: "63",
    askingTurnID: turn.turnID,
    askingClientNonce: turn.clientNonce,
    source: "claude",
    providerItemID: "item-1",
    providerTimelineID: `${turn.turnID}:item-1`,
    questions: [{ question: "Proceed?", options: [{ label: "Yes" }] }],
  });
  return handoff;
}

test("boundedMapSet evicts oldest beyond the cap", () => {
  const map = new Map<string, number>();
  for (let i = 0; i < 5; i++) boundedMapSet(map, `k${i}`, i, 3);
  assert.equal(map.size, 3);
  assert.deepEqual([...map.keys()], ["k2", "k3", "k4"]);
  // Re-setting an existing key refreshes its position instead of duplicating.
  boundedMapSet(map, "k2", 22, 3);
  assert.deepEqual([...map.keys()], ["k3", "k4", "k2"]);
});

test("boundedSetAdd evicts oldest beyond the cap", () => {
  const set = new Set<string>();
  for (let i = 0; i < 5; i++) boundedSetAdd(set, `v${i}`, 2);
  assert.equal(set.size, 2);
  assert.deepEqual([...set.values()], ["v3", "v4"]);
});

test("dispatch retries a transient durable-publish failure in place", async () => {
  const { sink, upserts } = stubSink({ failUpserts: 1 });
  const ok = await dispatch(sink, builtTurnEvent("turn.started"));
  assert.equal(ok, true);
  assert.equal(upserts.length, 1);
});

test("dispatch with attempts=1 stays fire-once for terminal-retry callers", async () => {
  const { sink, upserts } = stubSink({ failUpserts: 1 });
  const ok = await dispatch(sink, builtTurnEvent("turn.started"), 1);
  assert.equal(ok, false);
  assert.equal(upserts.length, 0);
});

test("publishTurnTerminalOrDefer acks on success and parks + NAKs on exhaustion", async () => {
  // Success arm.
  {
    const { sink, upserts } = stubSink();
    const { bus, calls } = stubCommandBus();
    const runner = makeRunner(sink, bus);
    const { record } = commandRecord();
    const turn = pendingTurn({
      commandRecord: record,
      stopCommandHeartbeat: bus.startCommandHeartbeat(),
    } as Partial<PendingTurn>);
    const ok = await runner.publishTurnTerminalOrDefer(
      turn,
      builtTurnEvent("turn.completed", turn.turnID),
      "turn.completed",
    );
    assert.equal(ok, true);
    assert.equal(turn.terminalEmitted, true);
    assert.equal(calls.completed.length, 1);
    assert.equal(calls.heartbeatsStopped, 1);
    assert.equal(upserts.length, 1);
  }
  // Exhaustion arm: heartbeat stopped, record NAK'd, event parked, command
  // detached so the redelivery owns it — and the prompt is never re-run.
  {
    const { sink } = stubSink({ failAlways: true });
    const { bus, calls } = stubCommandBus();
    const runner = makeRunner(sink, bus);
    const { record, naks } = commandRecord();
    const turn = pendingTurn({
      commandRecord: record,
      stopCommandHeartbeat: bus.startCommandHeartbeat(),
    } as Partial<PendingTurn>);
    const event = builtTurnEvent("turn.completed", turn.turnID);
    const ok = await runner.publishTurnTerminalOrDefer(
      turn,
      event,
      "turn.completed",
    );
    assert.equal(ok, false);
    assert.equal(turn.terminalEmitted, false);
    assert.equal(turn.pendingTerminal, event);
    assert.equal(turn.pendingTerminalType, "turn.completed");
    assert.equal(turn.commandRecord, undefined);
    assert.equal(calls.heartbeatsStopped, 1);
    assert.deepEqual(naks, [5_000]);
    assert.equal(calls.completed.length, 0);
  }
});

test("acceptCommandTurn redelivery republishes a parked terminal instead of re-running the prompt", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  let prompted = 0;
  runner.userQueue = { push: () => prompted++ } as RunnerInternals["userQueue"];

  const parkedEvent = builtTurnEvent(
    "turn.completed",
    turnIDForClientNonce("nonce-t1"),
  );
  const turn = pendingTurn({
    pendingTerminal: parkedEvent,
    pendingTerminalType: "turn.completed",
  } as Partial<PendingTurn>);
  runner.pendingTurns.push(turn);

  const { record } = commandRecord();
  await runner.acceptCommandTurn(record);

  assert.equal(prompted, 0, "the prompt must not be re-fed to the SDK");
  assert.equal(
    upserts.some((e) => e.type === "turn.completed"),
    true,
    "the parked terminal must be republished",
  );
  assert.equal(turn.terminalEmitted, true);
  assert.equal(calls.completed.length, 1);
  assert.equal(runner.pendingTurns.length, 0);
});

test("acceptCommandTurn redelivery reattaches a live in-flight turn (no double execution)", async () => {
  const { sink } = stubSink();
  const { bus } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  let prompted = 0;
  runner.userQueue = { push: () => prompted++ } as RunnerInternals["userQueue"];

  const turn = pendingTurn();
  runner.pendingTurns.push(turn);
  runner.activeTurn = turn;

  const { record } = commandRecord();
  await runner.acceptCommandTurn(record);

  assert.equal(prompted, 0, "the prompt must not be re-fed to the SDK");
  assert.equal(turn.commandRecord, record, "the fresh delivery owns the ack");
  assert.equal(typeof turn.stopCommandHeartbeat, "function");
  assert.equal(runner.pendingTurns.length, 1);
});

test("findTurnForKey resolves question identifiers to the asking turn", () => {
  const { sink } = stubSink();
  const { bus } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  const turn = pendingTurn();
  const handoff = questionEntryFor(turn);
  runner.pendingInputReplies.set("key-1", {
    turn,
    providerItemID: "item-1",
    timelineID: `${turn.turnID}:item-1`,
    questionTurnID: handoff.questionTurnID,
    questionClientNonce: handoff.questionClientNonce,
    resolve: () => {},
  });
  assert.equal(runner.findTurnForKey(handoff.questionTurnID), turn);
  assert.equal(runner.findTurnForKey(handoff.questionClientNonce), turn);
  assert.equal(runner.findTurnForKey("turn_unrelated"), null);
});

test("applyInterruptToTurn dismisses a pending question: callback settles, shell closes, asking turn interrupts", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  runner.sdkQuery = { interrupt: () => {} };

  const turn = pendingTurn();
  const handoff = questionEntryFor(turn);
  let settled: { isError?: boolean } | null = null;
  runner.pendingInputReplies.set("key-1", {
    turn,
    providerItemID: "item-1",
    timelineID: `${turn.turnID}:item-1`,
    questionTurnID: handoff.questionTurnID,
    questionClientNonce: handoff.questionClientNonce,
    resolve: (result: { isError?: boolean }) => {
      settled = result;
    },
  });

  const { record } = commandRecord();
  await runner.applyInterruptToTurn(record, turn, "client_interrupt");

  assert.notEqual(settled, null, "the canUseTool promise must settle");
  assert.equal(settled!.isError, true);
  assert.equal(runner.pendingInputReplies.size, 0);
  const questionTerminal = upserts.find(
    (e) =>
      e.type === "turn.interrupted" && e.turn_id === handoff.questionTurnID,
  );
  assert.notEqual(
    questionTerminal,
    undefined,
    "the question shell needs a durable terminal so the card stops accepting answers",
  );
  const askingTerminal = upserts.find(
    (e) => e.type === "turn.interrupted" && e.turn_id === turn.turnID,
  );
  assert.notEqual(askingTerminal, undefined);
  assert.equal(turn.terminalEmitted, true);
  // interrupt command ack'd exactly once.
  assert.equal(calls.completed.length, 1);
});

test("acceptInputReply fallback-matches by asking turn across a restart re-ask and closes the superseded shell", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);

  const turn = pendingTurn();
  const handoff = questionEntryFor(turn);
  let settled = false;
  // The re-asked pause is keyed by the NEW provider item id.
  runner.pendingInputReplies.set("new-key", {
    turn,
    providerItemID: "item-NEW",
    timelineID: `${turn.turnID}:item-NEW`,
    questionTurnID: handoff.questionTurnID,
    questionClientNonce: handoff.questionClientNonce,
    resolve: () => {
      settled = true;
    },
  });

  // The durable answer targets the ORIGINAL (pre-restart) identifiers.
  const { record } = commandRecord({
    client_nonce: "answer-abc123",
    target_turn_id: turn.turnID,
    target_timeline_id: `${turn.turnID}:item-OLD`,
    target_provider_item_id: "item-OLD",
    answers: { "Proceed?": ["Yes"] },
  });
  await runner.acceptInputReply(record);

  assert.equal(settled, true, "the durable answer must reach the re-asked pause");
  assert.equal(runner.pendingInputReplies.size, 0);
  assert.equal(calls.completed.length, 1);
  assert.equal(runner.parkedInputReplies.length, 0);
  const superseded = upserts.find(
    (e) =>
      e.type === "turn.interrupted" &&
      e.turn_id === handoff.questionTurnID,
  );
  assert.notEqual(
    superseded,
    undefined,
    "the re-asked shell must close as superseded so it cannot sit awaiting forever",
  );
});

test("acceptInputReply parks an unmatched answer under heartbeat and drain delivers it later", async () => {
  const { sink } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);

  const { record } = commandRecord({
    client_nonce: "answer-abc123",
    target_turn_id: turnIDForClientNonce("nonce-t1"),
    target_timeline_id: "tl-old",
    target_provider_item_id: "item-OLD",
    answers: { "Proceed?": ["Yes"] },
  });
  await runner.acceptInputReply(record);

  assert.equal(runner.parkedInputReplies.length, 1, "the answer must park, not fail");
  assert.equal(calls.heartbeatsStarted, 1, "parking keeps the delivery alive under heartbeat");
  assert.equal(calls.failed.length, 0);

  // The redelivered submit_turn replays the turn and the SDK re-asks —
  // the pause registers and the parked answer must drain into it.
  const turn = pendingTurn();
  const handoff = questionEntryFor(turn);
  let settled = false;
  runner.pendingInputReplies.set("new-key", {
    turn,
    providerItemID: "item-NEW",
    timelineID: `${turn.turnID}:item-NEW`,
    questionTurnID: handoff.questionTurnID,
    questionClientNonce: handoff.questionClientNonce,
    resolve: () => {
      settled = true;
    },
  });
  await runner.drainParkedInputRepliesFor(turn);

  assert.equal(settled, true);
  assert.equal(runner.parkedInputReplies.length, 0);
  assert.equal(calls.heartbeatsStopped, 1);
  assert.equal(calls.completed.length, 1);
});

test("expireBufferedInterrupt consults the ledger before writing interrupt_orphaned", async () => {
  const { sink, upserts } = stubSink({
    terminal: { type: "turn.completed" },
  });
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);

  const { record } = commandRecord();
  // The production expiry path never clears this timer (it IS the expiry
  // trigger normally); keep it from holding the test process open.
  const orphanTimer = setTimeout(() => {}, 1_000_000);
  orphanTimer.unref?.();
  runner.pendingInterrupts.push({
    record,
    targetKey: "turn_already-done",
    receivedAtMs: Date.now(),
    stopCommandHeartbeat: bus.startCommandHeartbeat(),
    orphanTimer,
  });
  await runner.expireBufferedInterrupt(record);
  clearTimeout(orphanTimer);

  assert.equal(calls.completed.length, 1, "already-terminal turns ack the interrupt");
  assert.equal(
    upserts.some((e) => e.type === "turn.failed"),
    false,
    "no contradictory second terminal",
  );
});

test("drainTurnsForFatalExit fails command-bearing turns durably before exit", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);

  const { record } = commandRecord();
  const turn = pendingTurn({
    commandRecord: record,
    stopCommandHeartbeat: bus.startCommandHeartbeat(),
  } as Partial<PendingTurn>);
  runner.pendingTurns.push(turn);

  await runner.drainTurnsForFatalExit();

  const failed = upserts.find(
    (e) =>
      e.type === "turn.failed" &&
      (e.payload as Record<string, unknown> | undefined)?.reason ===
        "sdk_loop_dead",
  );
  assert.notEqual(failed, undefined);
  assert.equal(calls.completed.length, 1);
});
