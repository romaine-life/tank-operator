import { test } from "node:test";
import assert from "node:assert/strict";

import {
  canonicalEventsForClaudeMessage,
  claudeTerminalBackgroundTask,
  type ClaudeTurnContext,
} from "./claude.js";
import type { Config } from "../config.js";
import {
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../../runner-shared/conversation.js";
import { stampTankEvent } from "../../../runner-shared/conversation-builders.js";

// Adapter output doesn't carry order_key/sequence until dispatch stamps
// it; running the event through stampTankEvent here mirrors what the
// runner does before publishing to the bus, so the assertion validates
// the full post-stamp envelope the persister sees.
function assertTankEventFixture(event: TankConversationEvent, label = event.type) {
  const stamped = stampTankEvent(event);
  assert.equal(isTankConversationEvent(stamped), true, `${label} should satisfy the Tank envelope`);
}

// claudeTerminalBackgroundTask drives the idle background-task wake. It must
// fire only for NATURAL terminals (the task finished on its own) and stay
// silent for user-initiated stops, lifecycle starts, and id-less frames — the
// natural/user split is the load-bearing assumption about SDK status strings.
test("claudeTerminalBackgroundTask wakes on natural terminals only", () => {
  const note = (over: Record<string, unknown>) =>
    ({ type: "system", subtype: "task_notification", task_id: "task-1", ...over }) as never;

  for (const status of ["completed", "failed", "exited"]) {
    const out = claudeTerminalBackgroundTask(note({ status }));
    assert.ok(out, `${status} should be a wake`);
    assert.equal(out?.taskID, "task-1");
    assert.equal(out?.status, status);
  }

  for (const status of ["stopped", "cancelled", "canceled"]) {
    assert.equal(
      claudeTerminalBackgroundTask(note({ status })),
      null,
      `${status} is a user-initiated stop and must not wake the session`,
    );
  }

  // lifecycle start, missing task id, and non-lifecycle messages never wake.
  assert.equal(
    claudeTerminalBackgroundTask({
      type: "system",
      subtype: "task_started",
      task_id: "task-x",
      status: "running",
    } as never),
    null,
  );
  assert.equal(claudeTerminalBackgroundTask(note({ status: "completed", task_id: "" })), null);
  assert.equal(claudeTerminalBackgroundTask({ type: "assistant" } as never), null);
});

test("claudeTerminalBackgroundTask carries human-meaningful terminal fields", () => {
  const out = claudeTerminalBackgroundTask({
    type: "system",
    subtype: "task_notification",
    task_id: "task-2",
    status: "failed",
    description: "Wait for mcp-glimmung CI",
    summary: "2 checks failed",
    last_tool_name: "Bash",
    error: "boom",
  } as never);
  assert.equal(out?.description, "Wait for mcp-glimmung CI");
  assert.equal(out?.summary, "2 checks failed");
  assert.equal(out?.lastToolName, "Bash");
  assert.equal(out?.error, "boom");
});

function turn(fields: Partial<ClaudeTurnContext> = {}): ClaudeTurnContext {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    interrupted: false,
    terminalEmitted: false,
    ...fields,
  };
}

function cfg(): Config {
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

test("adapter maps Claude compact_boundary to a context.compacted notice", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "system",
      subtype: "compact_boundary",
      uuid: "claude-compact-1",
      compact_metadata: { trigger: "auto", pre_tokens: 154321 },
    },
  );

  assert.deepEqual(events.map((event) => event.type), ["context.compacted"]);
  const event = events[0];
  assert.ok(event);
  assert.equal(event.actor, "runner");
  assert.equal(event.source, "claude");
  assert.equal(event.turn_id, "turn-run-123");
  assert.equal(event.payload?.trigger, "auto");
  assert.equal(event.payload?.pre_tokens, 154321);
  assertTankEventFixture(event);
});

test("adapter defaults a malformed compact_boundary to an auto notice without tokens", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    { type: "system", subtype: "compact_boundary", uuid: "claude-compact-2" },
  );

  assert.equal(events.length, 1);
  assert.equal(events[0]?.type, "context.compacted");
  assert.equal(events[0]?.payload?.trigger, "auto");
  assert.equal(events[0]?.payload?.pre_tokens, undefined);
  for (const event of events) assertTankEventFixture(event);
});

test("adapter maps Claude assistant text and tool_use blocks to Tank items", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-1",
      message: {
        content: [
          { type: "text", text: "I will inspect the workspace." },
          {
            type: "tool_use",
            id: "toolu_read",
            name: "Read",
            input: { file_path: "README.md" },
          },
        ],
      },
    },  );

  assert.deepEqual(events.map((event) => event.type), [
    "item.completed",
    "item.started",
  ]);
  for (const event of events) assertTankEventFixture(event);
  assert.equal(events[0]?.actor, "assistant");
  assert.equal(events[0]?.timeline_id, "turn-run-123:item:assistant:claude-msg-1:text:0");
  assert.equal(events[0]?.provider_item_id, "assistant:claude-msg-1:text:0");
  assert.equal(events[0]?.payload?.kind, "message");
  assert.equal(events[0]?.payload?.text, "I will inspect the workspace.");
  assert.equal(events[1]?.actor, "tool");
  assert.equal(events[1]?.timeline_id, "turn-run-123:item:toolu_read");
  assert.equal(events[1]?.provider_item_id, "toolu_read");
  assert.equal(events[1]?.payload?.title, "Read");
});

test("adapter gives each text block in one Claude message a unique canonical id", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-multi-text",
      message: {
        content: [
          { type: "text", text: "First paragraph." },
          { type: "text", text: "Second paragraph." },
        ],
      },
    },  );

  assert.deepEqual(events.map((event) => event.payload?.text), [
    "First paragraph.",
    "Second paragraph.",
  ]);
  assert.equal(new Set(events.map((event) => event.timeline_id)).size, 2);
  assert.equal(new Set(events.map((event) => event.event_id)).size, 2);
  assert.equal(events[0]?.timeline_id, "turn-run-123:item:assistant:claude-msg-multi-text:text:0");
  assert.equal(events[1]?.timeline_id, "turn-run-123:item:assistant:claude-msg-multi-text:text:1");
  for (const event of events) assertTankEventFixture(event);
});

test("adapter carries explicit Claude final-answer ids on successful result", () => {
  const ctx = turn();
  const assistant = canonicalEventsForClaudeMessage(
    cfg(),
    ctx,
    {
      type: "assistant",
      uuid: "claude-msg-final",
      message: {
        content: [
          { type: "text", text: "First final paragraph." },
          { type: "text", text: "Second final paragraph." },
        ],
      },
    },  );
  assert.equal(assistant.length, 2);

  const result = canonicalEventsForClaudeMessage(
    cfg(),
    ctx,
    {
      type: "result",
      subtype: "success",
      uuid: "claude-result-success",
    },  );

  assert.equal(result.length, 1);
  assertTankEventFixture(result[0]!);
  assert.equal(result[0]?.type, "turn.completed");
  assert.deepEqual(result[0]?.payload?.final_answer, {
    timeline_ids: [
      "turn-run-123:item:assistant:claude-msg-final:text:0",
      "turn-run-123:item:assistant:claude-msg-final:text:1",
    ],
    provider_item_ids: [
      "assistant:claude-msg-final:text:0",
      "assistant:claude-msg-final:text:1",
    ],
  });
});

test("adapter does not mark Claude assistant text with tool_use as final", () => {
  const ctx = turn();
  canonicalEventsForClaudeMessage(
    cfg(),
    ctx,
    {
      type: "assistant",
      uuid: "claude-msg-tool",
      message: {
        content: [
          { type: "text", text: "I will inspect the file." },
          { type: "tool_use", id: "toolu_read", name: "Read", input: { file_path: "README.md" } },
        ],
      },
    },  );
  const result = canonicalEventsForClaudeMessage(
    cfg(),
    ctx,
    {
      type: "result",
      subtype: "success",
      uuid: "claude-result-no-final",
    },  );

  assert.equal(result[0]?.payload?.final_answer, undefined);
});

test("adapter emits no item events for Claude AskUserQuestion (the runner ends the turn awaiting input)", () => {
  // AskUserQuestion is handled by the runner's canUseTool, which ends the
  // asking turn with a durable turn.awaiting_input terminal carrying the
  // Tank-canonical questions. The adapter must NOT emit a dangling
  // item.started / approval event on that settled turn — there is no in-turn
  // tool item and no tool.approval_* event anymore.
  const events = canonicalEventsForClaudeMessage(cfg(), turn(), {
    type: "assistant",
    uuid: "claude-msg-ask",
    message: {
      content: [
        {
          type: "tool_use",
          id: "toolu_ask",
          name: "AskUserQuestion",
          input: { question: "Proceed?" },
        },
      ],
    },
  });

  assert.deepEqual(events, []);
});

test("adapter still emits item.started for a non-AskUserQuestion tool_use", () => {
  const events = canonicalEventsForClaudeMessage(cfg(), turn(), {
    type: "assistant",
    uuid: "claude-msg-bash",
    message: {
      content: [
        {
          type: "tool_use",
          id: "toolu_bash",
          name: "Bash",
          input: { command: "ls" },
        },
      ],
    },
  });

  assert.deepEqual(events.map((event) => event.type), ["item.started"]);
  for (const event of events) assertTankEventFixture(event);
});

test("adapter maps Claude tool_result is_error to completed result_failed outcome", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "user",
      uuid: "claude-tool-result-error",
      message: {
        content: [
          {
            type: "tool_result",
            tool_use_id: "toolu_read",
            content: "exit 1",
            is_error: true,
          },
        ],
      },
    },  );

  assert.equal(events.length, 1);
  assertTankEventFixture(events[0]!);
  assert.equal(events[0]?.type, "item.completed");
  assert.deepEqual(events[0]?.payload?.outcome, {
    kind: "result_failed",
    reason: "claude_tool_result_is_error",
  });
});

test("adapter maps Claude result failures and interrupts to terminal turn events", () => {
  const failed = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "result",
      subtype: "error",
      result: "provider failed",
      uuid: "claude-result-failed",
    },  );
  assert.equal(failed.length, 1);
  assertTankEventFixture(failed[0]!);
  assert.equal(failed[0]?.type, "turn.failed");
  assert.equal(failed[0]?.payload?.reason, "provider_failure");
  assert.equal(failed[0]?.payload?.error, "provider failed");

  const interrupted = canonicalEventsForClaudeMessage(
    cfg(),
    turn({ interrupted: true }),
    {
      type: "result",
      subtype: "success",
      result: "stopped",
      uuid: "claude-result-interrupted",
    },  );
  assert.equal(interrupted.length, 1);
  assertTankEventFixture(interrupted[0]!);
  assert.equal(interrupted[0]?.type, "turn.interrupted");
  assert.equal(interrupted[0]?.payload?.reason, "client_interrupt");
});

test("adapter maps Claude background task lifecycle to shell task events", () => {
  const started = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "system",
      subtype: "task_started",
      task_id: "task-abc",
      tool_use_id: "toolu_monitor",
      status: "running",
      summary: "Watching logs",
      uuid: "task-event-1",
    },  );

  assert.equal(started.length, 1);
  assertTankEventFixture(started[0]!);
  assert.equal(started[0]?.type, "shell_task.started");
  assert.equal(started[0]?.actor, "tool");
  assert.equal(started[0]?.timeline_id, "turn-run-123:shell_task:task-abc");
  assert.equal(started[0]?.task_id, "task-abc");
  assert.equal(started[0]?.provider_item_id, "toolu_monitor");
  assert.deepEqual(started[0]?.payload, {
    kind: "shell_task",
    task_id: "task-abc",
    status: "running",
    provider_subtype: "task_started",
    summary: "Watching logs",
    tool_use_id: "toolu_monitor",
  });

  const exited = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "system",
      subtype: "task_notification",
      task_id: "task-abc",
      status: "failed",
      summary: "Monitor exited nonzero",
      error: "exit 1",
      uuid: "task-event-2",
    },  );

  assert.equal(exited.length, 1);
  assertTankEventFixture(exited[0]!);
  assert.equal(exited[0]?.type, "shell_task.exited");
  assert.equal(exited[0]?.payload?.status, "failed");
  assert.equal(exited[0]?.payload?.error, "exit 1");
});

test("adapter emits a per-message context-occupancy snapshot tagged claude.message", () => {
  // Claude reports usage only on the cumulative terminal, whose input_tokens
  // is the tiny uncached sliver. Each assistant message's own usage is the
  // size of that model call's prompt; the adapter forwards it as a durable
  // turn.usage snapshot so the context gauge has a per-call signal.
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-usage",
      message: {
        content: [{ type: "text", text: "done." }],
        usage: {
          input_tokens: 4,
          cache_read_input_tokens: 157_652,
          cache_creation_input_tokens: 161_334,
          output_tokens: 5_016,
        },
      },
    },
  );

  const usageEvents = events.filter((event) => event.type === "turn.usage");
  assert.equal(usageEvents.length, 1);
  const usageEvent = usageEvents[0]!;
  assertTankEventFixture(usageEvent);
  assert.equal(usageEvent.turn_id, "turn-run-123");
  assert.equal(usageEvent.actor, "runner");
  assert.equal((usageEvent.payload?.usage as Record<string, unknown>)?.cache_read_input_tokens, 157_652);
  assert.deepEqual(usageEvent.payload?.usage_observation, {
    usage_source: "claude.message",
    terminal_had_usage: false,
  });
});

test("adapter emits no usage snapshot when a Claude assistant message carries no usage", () => {
  const events = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "assistant",
      uuid: "claude-msg-nousage",
      message: { content: [{ type: "text", text: "hi" }] },
    },
  );
  assert.equal(events.some((event) => event.type === "turn.usage"), false);
});

test("adapter tags the cumulative Claude result terminal as claude.result", () => {
  // result.usage is cumulative across the turn; it drives cost, not the
  // context gauge. The claude.result tag tells the reader to ignore it for
  // occupancy (so the cumulative cache-read sum is not mistaken for the
  // live prompt size).
  const result = canonicalEventsForClaudeMessage(
    cfg(),
    turn(),
    {
      type: "result",
      subtype: "success",
      uuid: "claude-result-usage",
      usage: {
        input_tokens: 266,
        cache_read_input_tokens: 3_219_249,
        cache_creation_input_tokens: 21_332,
        output_tokens: 19_380,
      },
    },
  );
  assert.equal(result.length, 1);
  assertTankEventFixture(result[0]!);
  assert.equal(result[0]?.type, "turn.completed");
  assert.equal((result[0]?.payload?.usage as Record<string, unknown>)?.cache_read_input_tokens, 3_219_249);
  assert.deepEqual(result[0]?.payload?.usage_observation, {
    usage_source: "claude.result",
    terminal_had_usage: true,
  });
});
