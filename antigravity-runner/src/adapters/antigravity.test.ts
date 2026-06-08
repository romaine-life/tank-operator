import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../../runner-shared/conversation.js";
import { stampTankEvent } from "../../../runner-shared/conversation-builders.js";
import {
  AntigravityTranscriptAdapter,
  type AgyStep,
  type AntigravityTurn,
} from "./antigravity.js";

function loadFixture(name: string): AgyStep[] {
  // Read from the source tree (tsc does not copy .jsonl into dist/).
  const url = new URL(`../../src/adapters/fixtures/${name}`, import.meta.url);
  const text = readFileSync(fileURLToPath(url), "utf8");
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map((line) => JSON.parse(line) as AgyStep);
}

const TURN: AntigravityTurn = {
  turnID: "turn_test123",
  clientNonce: "turn_test123",
};

// Drive the whole transcript through the adapter the way the runner tails it:
// one step at a time, then the turn terminal. Every emitted event is stamped
// (the publish path) and must pass the Tank conversation contract validator.
function runTranscript(steps: AgyStep[]): TankConversationEvent[] {
  const adapter = new AntigravityTranscriptAdapter("42");
  const events: TankConversationEvent[] = [];
  for (const step of steps) {
    for (const event of adapter.stepEvents(TURN, step)) {
      events.push(stampTankEvent(event) as TankConversationEvent);
    }
  }
  events.push(
    stampTankEvent(
      adapter.completeTurn(TURN, { input_tokens: 10, output_tokens: 20 }),
    ) as TankConversationEvent,
  );
  return events;
}

test("every emitted event satisfies the Tank conversation contract", () => {
  const events = runTranscript(loadFixture("banana-turn.jsonl"));
  for (const event of events) {
    assert.ok(
      isTankConversationEvent(event),
      `invalid Tank event: ${JSON.stringify(event)}`,
    );
    assert.equal(event.source, "antigravity");
    assert.equal(event.session_id, "42");
    assert.equal(event.turn_id, "turn_test123");
  }
});

test("maps the agentic turn to the expected structured items", () => {
  const events = runTranscript(loadFixture("banana-turn.jsonl"));
  const types = events.map((e) => e.type);

  // One turn.started, two tool items (write + read) each started+completed,
  // one assistant message, one turn.completed. User input + system history
  // are dropped (Tank owns the user message).
  assert.equal(types.filter((t) => t === "turn.started").length, 1);
  assert.equal(types.filter((t) => t === "turn.completed").length, 1);
  assert.equal(types[0], "turn.started");
  assert.equal(types.at(-1), "turn.completed");

  const started = events.filter((e) => e.type === "item.started");
  const completed = events.filter((e) => e.type === "item.completed");
  const toolStarts = started.filter(
    (e) => (e.payload as { kind?: string }).kind === "tool",
  );
  const toolDone = completed.filter(
    (e) => (e.payload as { kind?: string }).kind === "tool",
  );
  const messages = completed.filter(
    (e) => (e.payload as { kind?: string }).kind === "message",
  );
  assert.equal(toolStarts.length, 2);
  assert.equal(toolDone.length, 2);
  assert.equal(messages.length, 1);

  // The first tool is the write; its title comes from toolSummary and its
  // tool_input drops the agy UI-hint keys but keeps the real arguments.
  const write = toolStarts[0].payload as {
    tool_name: string;
    title: string;
    tool_input?: Record<string, unknown>;
  };
  assert.equal(write.tool_name, "write_to_file");
  assert.equal(write.title, "Create note.txt");
  assert.ok(write.tool_input);
  assert.equal(write.tool_input.TargetFile, "/workspace/note.txt");
  assert.equal(write.tool_input.CodeContent, "BANANA");
  assert.equal(write.tool_input.toolSummary, undefined);
  assert.equal(write.tool_input.toolAction, undefined);
});

test("tool started/completed share a timeline id; final answer is the last prose", () => {
  const events = runTranscript(loadFixture("banana-turn.jsonl"));
  const started = events.filter((e) => e.type === "item.started");
  const completed = events.filter((e) => e.type === "item.completed");

  // Each tool's item.started and item.completed must address the same
  // timeline unit so the GUI renders one tool row that resolves in place.
  const startedTimelines = started.map((e) => e.timeline_id).sort();
  const toolCompletedTimelines = completed
    .filter((e) => (e.payload as { kind?: string }).kind === "tool")
    .map((e) => e.timeline_id)
    .sort();
  assert.deepEqual(toolCompletedTimelines, startedTimelines);

  // The final answer points at the single assistant message timeline.
  const message = completed.find(
    (e) => (e.payload as { kind?: string }).kind === "message",
  )!;
  const completedTurn = events.find((e) => e.type === "turn.completed")!;
  const finalAnswer = (
    completedTurn.payload as {
      final_answer?: { timeline_ids: string[] };
    }
  ).final_answer;
  assert.ok(finalAnswer);
  assert.deepEqual(finalAnswer.timeline_ids, [message.timeline_id]);

  // turn.completed carries the loadCodeAssist usage observation.
  assert.ok((completedTurn.payload as { usage?: unknown }).usage);
});

test("final answer state requires done non-empty assistant prose", () => {
  const adapter = new AntigravityTranscriptAdapter("42");
  const active = adapter.stepEvents(TURN, {
    step_index: 1,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "IN_PROGRESS",
    content: "Partial...",
  });
  assert.deepEqual(
    active.map((event) => event.type),
    ["turn.started"],
  );
  assert.equal(adapter.hasFinalAnswer(TURN), false);

  const empty = adapter.stepEvents(TURN, {
    step_index: 2,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    content: "",
  });
  assert.deepEqual(empty, []);
  assert.equal(adapter.hasFinalAnswer(TURN), false);

  const done = adapter.stepEvents(TURN, {
    step_index: 1,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    content: "Done.",
  });
  assert.equal(done.length, 1);
  assert.equal(done[0]!.type, "item.completed");
  assert.equal(adapter.hasFinalAnswer(TURN), true);
});

test("in-progress tool call does not consume the later done transition", () => {
  const adapter = new AntigravityTranscriptAdapter("42");
  const active = adapter.stepEvents(TURN, {
    step_index: 10,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "IN_PROGRESS",
    tool_calls: [
      {
        name: "run_command",
        args: { CommandLine: "pwd", toolSummary: "Run pwd" },
      },
    ],
  });
  assert.deepEqual(
    active.map((event) => event.type),
    ["turn.started"],
  );

  const done = adapter.stepEvents(TURN, {
    step_index: 10,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [
      {
        name: "run_command",
        args: { CommandLine: "pwd", toolSummary: "Run pwd" },
      },
    ],
  });
  assert.equal(done.length, 1);
  assert.equal(done[0]!.type, "item.started");
  assert.equal((done[0]!.payload as { title?: string }).title, "Run pwd");
});

test("in-progress tool result does not close the pending tool", () => {
  const adapter = new AntigravityTranscriptAdapter("42");
  adapter.stepEvents(TURN, {
    step_index: 1,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [
      {
        name: "run_command",
        args: { CommandLine: "pwd", toolSummary: "Run pwd" },
      },
    ],
  });

  const activeResult = adapter.stepEvents(TURN, {
    step_index: 2,
    source: "MODEL",
    type: "RUN_COMMAND",
    status: "IN_PROGRESS",
    content: "partial output",
  });
  assert.deepEqual(activeResult, []);

  const doneResult = adapter.stepEvents(TURN, {
    step_index: 2,
    source: "MODEL",
    type: "RUN_COMMAND",
    status: "DONE",
    content: "final output",
  });
  assert.equal(doneResult.length, 1);
  assert.equal(doneResult[0]!.type, "item.completed");
  assert.equal(
    (doneResult[0]!.payload as { text?: string }).text,
    "final output",
  );
});

test("re-feeding a step is idempotent (tailing a growing file)", () => {
  const adapter = new AntigravityTranscriptAdapter("42");
  const step: AgyStep = {
    step_index: 7,
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    content: "done",
  };
  const first = adapter.stepEvents(TURN, step);
  const second = adapter.stepEvents(TURN, step);
  assert.ok(first.length > 0);
  assert.equal(second.length, 0);
});

test("SYSTEM ERROR_MESSAGE closes the failed tool instead of poisoning FIFO", () => {
  const events = runTranscript(loadFixture("system-error-tool-turn.jsonl"));
  const failed = events.find((e) => e.type === "item.failed");
  assert.ok(failed, "invalid provider tool call should emit item.failed");
  assert.equal((failed.payload as { title?: string }).title, "Checking out test slot");
  assert.deepEqual((failed.payload as { outcome?: unknown }).outcome, {
    kind: "execution_failed",
    reason: "provider_item_error",
  });
  assert.match(
    String((failed.payload as { error?: string }).error ?? ""),
    /checkout_test_slot is not enabled/,
  );

  const toolCompleted = events.filter(
    (e) =>
      e.type === "item.completed" &&
      (e.payload as { kind?: string }).kind === "tool",
  );
  assert.equal(toolCompleted.length, 1);
  const env = toolCompleted[0]!;
  assert.equal((env.payload as { title?: string }).title, "Running env");
  assert.match(String((env.payload as { text?: string }).text ?? ""), /PWD=\/workspace/);

  const failedStart = events.find(
    (e) =>
      e.type === "item.started" &&
      e.provider_item_id === failed.provider_item_id,
  );
  assert.equal(failedStart?.timeline_id, failed.timeline_id);
});

test("conversation id scopes duplicate Antigravity step indexes", () => {
  const events = runTranscript([
    {
      step_index: 1,
      conversation_id: "root",
      source: "MODEL",
      type: "PLANNER_RESPONSE",
      status: "DONE",
      tool_calls: [
        {
          name: "run_command",
          args: { toolSummary: "Root command", CommandLine: "pwd" },
        },
      ],
    },
    {
      step_index: 1,
      conversation_id: "subagent",
      source: "MODEL",
      type: "PLANNER_RESPONSE",
      status: "DONE",
      tool_calls: [
        {
          name: "run_command",
          args: { toolSummary: "Subagent command", CommandLine: "pwd" },
        },
      ],
    },
    {
      step_index: 2,
      conversation_id: "root",
      source: "MODEL",
      type: "RUN_COMMAND",
      status: "DONE",
      content: "root output",
    },
    {
      step_index: 2,
      conversation_id: "subagent",
      source: "MODEL",
      type: "RUN_COMMAND",
      status: "DONE",
      content: "subagent output",
    },
  ]);

  const starts = events.filter((e) => e.type === "item.started");
  const completed = events.filter(
    (e) =>
      e.type === "item.completed" &&
      (e.payload as { kind?: string }).kind === "tool",
  );
  assert.equal(starts.length, 2);
  assert.equal(completed.length, 2);
  assert.notEqual(starts[0]!.provider_item_id, starts[1]!.provider_item_id);
  assert.equal((completed[0]!.payload as { title?: string }).title, "Root command");
  assert.equal(
    (completed[1]!.payload as { title?: string }).title,
    "Subagent command",
  );
});

test("adapter reports unclosed pending tools at turn terminal", () => {
  const observed: Array<{ kind: string; count: number | undefined }> = [];
  const adapter = new AntigravityTranscriptAdapter("42", {
    recordCorrelation: (kind, count) => observed.push({ kind, count }),
  });
  adapter.stepEvents(TURN, {
    step_index: 1,
    conversation_id: "root",
    source: "MODEL",
    type: "PLANNER_RESPONSE",
    status: "DONE",
    tool_calls: [{ name: "run_command", args: { toolSummary: "Never closes" } }],
  });
  adapter.completeTurn(TURN);
  assert.deepEqual(observed, [
    { kind: "unclosed_tool_at_terminal", count: 1 },
  ]);
});
