// Tests for the #1078 codex-runner correctness fixes: stop-during-
// AskUserQuestion (item 2), durable-answer recovery across restarts
// (item 3), stop_background_task collateral wake suppression (item 6),
// interrupt metrics/outcome accounting (item 6), and prior-identity
// matching across AskUserQuestion rotations.
import { test } from "node:test";
import assert from "node:assert/strict";

import { interruptTargetMatchesTurn, Runner } from "./runner.js";
import {
  askUserQuestionHandoffEvents,
  shellTaskEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";

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

type UpsertedEvent = Record<string, unknown>;

function stubSink(opts?: { terminal?: unknown }) {
  const upserts: UpsertedEvent[] = [];
  return {
    upserts,
    sink: {
      upsert: async (event: UpsertedEvent) => {
        upserts.push(event);
      },
      findTurnTerminal: async () => opts?.terminal ?? null,
    },
  };
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

type CodexTurn = {
  turnID: string;
  clientNonce: string;
  turnSeq: number;
  priorIdentities?: string[];
  interruptRecords?: unknown[];
  interruptOnStart?: boolean;
};

function acceptedTurn(): CodexTurn {
  return {
    turnID: turnIDForClientNonce("nonce-t1"),
    clientNonce: "nonce-t1",
    turnSeq: 1,
  };
}

type RunnerInternals = {
  sink: unknown;
  commandBus: unknown;
  currentTurn: CodexTurn | null;
  currentAbort: { abort: () => void } | null;
  interruptRequested: boolean;
  pendingInputReplies: Map<string, Record<string, unknown>>;
  parkedInputReplies: unknown[];
  collateralStoppedTasks: Set<string>;
  firedBackgroundTaskWakes: Set<string>;
  codexAdapter: unknown;
  appServerTransport: unknown;
  acceptInterrupt: (record: unknown) => Promise<void>;
  acceptInputReply: (record: unknown) => Promise<void>;
  acceptStopBackgroundTask: (record: unknown) => Promise<void>;
  drainParkedInputRepliesFor: (turn: CodexTurn) => Promise<void>;
  completeStalePendingInterrupts: (turn: CodexTurn) => Promise<void>;
  pendingInterrupts: unknown[];
  turnForQuestionKey: (key: string) => CodexTurn | null;
};

function makeRunner(sink: unknown, bus: unknown): RunnerInternals {
  const runner = new Runner(runnerConfig()) as unknown as RunnerInternals;
  runner.sink = sink;
  runner.commandBus = bus;
  return runner;
}

function questionEntryFor(
  runner: RunnerInternals,
  turn: CodexTurn,
  opts?: { providerItemID?: string; onResolve?: (v: unknown) => void },
) {
  const providerItemID = opts?.providerItemID ?? "item-1";
  const handoff = askUserQuestionHandoffEvents({
    sessionID: "63",
    askingTurnID: turn.turnID,
    askingClientNonce: turn.clientNonce,
    source: "codex",
    providerItemID,
    providerTimelineID: `${turn.turnID}:${providerItemID}`,
    questions: [{ question: "Proceed?", options: [{ label: "Yes" }] }],
  });
  runner.pendingInputReplies.set(`key-${providerItemID}`, {
    turn,
    providerItemID,
    timelineID: `${turn.turnID}:${providerItemID}`,
    questionTurnID: handoff.questionTurnID,
    questionClientNonce: handoff.questionClientNonce,
    resolve: (v: unknown) => opts?.onResolve?.(v),
  });
  return handoff;
}

test("interruptTargetMatchesTurn honors prior identities from rotations", () => {
  const turn = {
    turnID: "turn_after",
    clientNonce: "after",
    priorIdentities: ["turn_before", "before"],
  };
  assert.equal(interruptTargetMatchesTurn("turn_before", turn), true);
  assert.equal(interruptTargetMatchesTurn("before", turn), true);
  assert.equal(interruptTargetMatchesTurn("turn_after", turn), true);
  assert.equal(interruptTargetMatchesTurn("turn_other", turn), false);
});

test("acceptInterrupt targeting the question turn dismisses the pause and aborts the asking turn", async () => {
  const { sink, upserts } = stubSink();
  const { bus } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  const turn = acceptedTurn();
  runner.currentTurn = turn;
  let aborted = 0;
  runner.currentAbort = {
    abort: () => {
      aborted++;
    },
  };
  let settled: unknown = null;
  const handoff = questionEntryFor(runner, turn, {
    onResolve: (v) => {
      settled = v;
    },
  });

  await runner.acceptInterrupt({
    target_turn_id: handoff.questionTurnID,
    client_nonce: handoff.questionTurnID,
  });

  assert.deepEqual(settled, { answers: {} }, "the requestUserInput promise must settle");
  assert.equal(runner.pendingInputReplies.size, 0);
  assert.equal(aborted, 1, "the asking turn must abort");
  assert.equal(runner.interruptRequested, true);
  assert.equal(turn.interruptRecords?.length, 1);
  const questionTerminal = upserts.find(
    (e) =>
      e.type === "turn.interrupted" && e.turn_id === handoff.questionTurnID,
  );
  assert.notEqual(
    questionTerminal,
    undefined,
    "the question shell needs a durable terminal so the card stops accepting answers",
  );
});

test("acceptInputReply fallback-matches by asking turn across a restart re-ask and closes the superseded shell", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  const turn = acceptedTurn();
  let settled: unknown = null;
  const handoff = questionEntryFor(runner, turn, {
    providerItemID: "item-NEW",
    onResolve: (v) => {
      settled = v;
    },
  });

  await runner.acceptInputReply({
    client_nonce: "answer-abc123",
    target_turn_id: turn.turnID,
    target_timeline_id: `${turn.turnID}:item-OLD`,
    target_provider_item_id: "item-OLD",
    answers: { "Proceed?": ["Yes"] },
  });

  assert.notEqual(settled, null, "the durable answer must reach the re-asked pause");
  assert.equal(runner.pendingInputReplies.size, 0);
  assert.equal(calls.completed.length, 1);
  assert.equal(runner.parkedInputReplies.length, 0);
  const superseded = upserts.find(
    (e) =>
      e.type === "turn.interrupted" && e.turn_id === handoff.questionTurnID,
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

  await runner.acceptInputReply({
    client_nonce: "answer-abc123",
    target_turn_id: turnIDForClientNonce("nonce-t1"),
    target_timeline_id: "tl-old",
    target_provider_item_id: "item-OLD",
    answers: { "Proceed?": ["Yes"] },
  });

  assert.equal(runner.parkedInputReplies.length, 1, "the answer must park, not fail");
  assert.equal(calls.heartbeatsStarted, 1);
  assert.equal(calls.failed.length, 0);

  const turn = acceptedTurn();
  let settled: unknown = null;
  questionEntryFor(runner, turn, {
    providerItemID: "item-NEW",
    onResolve: (v) => {
      settled = v;
    },
  });
  await runner.drainParkedInputRepliesFor(turn);

  assert.notEqual(settled, null);
  assert.equal(runner.parkedInputReplies.length, 0);
  assert.equal(calls.heartbeatsStopped, 1);
  assert.equal(calls.completed.length, 1);
});

test("completeStalePendingInterrupts still acks every stale record", async () => {
  const { sink } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);
  const turn = acceptedTurn();
  runner.pendingInterrupts.push({
    target_turn_id: turn.turnID,
    client_nonce: turn.turnID,
  });
  await runner.completeStalePendingInterrupts(turn);
  assert.equal(calls.completed.length, 1);
  assert.equal(runner.pendingInterrupts.length, 0);
});

test("acceptStopBackgroundTask resolves target + collateral honestly and suppresses their wakes", async () => {
  const { sink, upserts } = stubSink();
  const { bus, calls } = stubCommandBus();
  const runner = makeRunner(sink, bus);

  const live = [
    { taskID: "task-target", processID: 11, command: "sleep 100" },
    { taskID: "task-bystander", processID: 12, command: "sleep 200" },
  ];
  const resolved: string[] = [];
  runner.codexAdapter = {
    pendingBackgroundTasks: () => live.filter((t) => !resolved.includes(t.taskID)),
    completeBackgroundShellByExit: (
      taskID: string,
      opts: { status?: string; completionSource?: string } = {},
    ) => {
      if (resolved.includes(taskID)) return [];
      resolved.push(taskID);
      return [
        shellTaskEvent({
          sessionID: "63",
          turnID: turnIDForClientNonce("nonce-t1"),
          source: "codex",
          type: "shell_task.exited",
          taskID,
          status: opts.status ?? "completed",
          providerItemID: taskID,
          payload: {
            status: opts.status ?? "completed",
            completion_source: opts.completionSource ?? "process_exit_observed",
          },
        }),
      ];
    },
  };
  let cleans = 0;
  runner.appServerTransport = {
    cleanBackgroundTerminals: async () => {
      cleans++;
    },
  };

  await runner.acceptStopBackgroundTask({
    target_task_id: "task-target",
    target_turn_id: turnIDForClientNonce("nonce-t1"),
    command_id: "cmd-stop-1",
  });

  assert.equal(cleans, 1);
  assert.equal(calls.completed.length, 1, "the stop command must ack");
  const exits = upserts.filter((e) => e.type === "shell_task.exited");
  assert.equal(exits.length, 2, "target AND collateral get durable exits");
  for (const exit of exits) {
    assert.equal(
      (exit.payload as Record<string, unknown>).status,
      "stopped",
      "forced exits must not claim completion",
    );
  }
  assert.equal(
    runner.firedBackgroundTaskWakes.size,
    0,
    "no wake registration for deliberately stopped tasks",
  );
  assert.equal(
    runner.collateralStoppedTasks.size,
    0,
    "suppression entries are consumed by the exit publications",
  );
});
