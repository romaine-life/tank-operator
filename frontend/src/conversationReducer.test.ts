import assert from "node:assert/strict";
import { test } from "node:test";

import {
  initialConversationState,
  reduceConversationEvents,
} from "./conversationReducer.ts";
import { isTankConversationEvent, type TankConversationEvent } from "../../runner-shared/conversation.js";

function ev(
  event_id: string,
  type: TankConversationEvent["type"],
  fields: Partial<TankConversationEvent> = {},
): TankConversationEvent {
  const defaults: Partial<TankConversationEvent> = {};
  if (type === "user_message.created") {
    defaults.actor = "user";
    defaults.timeline_id = "turn-1:user";
    defaults.client_nonce = "client-1";
  }
  if (type === "turn.submitted") {
    defaults.client_nonce = "client-1";
    defaults.payload = { status: "submitted" };
  }
  if (type === "session.status") {
    defaults.actor = "system";
    defaults.timeline_id = "session:63:status:ready";
    defaults.payload = { status: "ready", text: "Session is ready." };
  }
  return {
    event_id,
    order_key: event_id.padStart(4, "0"),
    session_id: "63",
    turn_id: "turn-1",
    actor: "runner",
    source: "tank",
    type,
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    ...defaults,
    ...fields,
  };
}

test("context.compacted is a valid Tank envelope", () => {
  const event = ev("c", "context.compacted", { source: "claude", payload: { trigger: "manual" } });
  assert.equal(isTankConversationEvent(event), true);
});

test("context.compacted is an informational no-op for run state", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.submitted"),
    ev("2", "turn.started"),
    ev("3", "context.compacted", { source: "claude", payload: { trigger: "auto", pre_tokens: 158000 } }),
  ]);
  assert.equal(state.runStatus, "streaming");
  assert.equal(state.activeTurnId, "turn-1");
  assert.equal(state.needsInput, false);
  assert.equal(state.failed, false);
  assert.equal(state.seenEventIds.includes("3"), true);
});

test("Codex interrupt is stopped state, not provider error", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      source: "tank",
      client_nonce: "run-1",
      payload: { text: "stop safely" },
    }),
    ev("2", "turn.submitted", { source: "codex" }),
    ev("3", "turn.started", { source: "codex" }),
    ev("4", "turn.interrupted", {
      source: "codex",
      payload: { reason: "client_interrupt" },
    }),
  ]);

  assert.equal(state.runStatus, "stopped");
  assert.equal(state.failed, false);
  assert.equal(state.messages.length, 1);
});

test("session status events replay as durable system messages", () => {
  const state = reduceConversationEvents([
    ev("session:63:status:loading", "session.status", {
      actor: "system",
      timeline_id: "session:63:status:loading",
      created_at: "2026-05-20T10:00:00.000Z",
      payload: { status: "loading", text: "Session is loading." },
    }),
    ev("session:63:status:ready", "session.status", {
      actor: "system",
      timeline_id: "session:63:status:ready",
      created_at: "2026-05-20T10:00:08.000Z",
      payload: { status: "ready", text: "Session is ready." },
    }),
  ]);

  assert.deepEqual(state.messages.map((message) => message.role), ["system", "system"]);
  assert.deepEqual(state.messages.map((message) => message.text), [
    "Session is loading.",
    "Session is ready.",
  ]);
  assert.equal(state.runStatus, "ready");
});

test("Normal turn reaches ready with one user message and assistant item", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-normal",
      payload: { text: "summarize" },
    }),
    ev("2", "turn.submitted", { client_nonce: "run-normal" }),
    ev("3", "turn.started", { source: "claude" }),
    ev("4", "item.completed", {
      actor: "assistant",
      source: "claude",
      timeline_id: "msg-1",
      payload: { kind: "message", text: "summary" },
    }),
    ev("5", "turn.completed", { source: "claude" }),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.text, "summarize");
  assert.equal(state.items.length, 1);
  assert.equal(state.items[0]?.actor, "assistant");
  assert.equal(state.items[0]?.text, "summary");
  assert.equal(state.activeTurnId, null);
  assert.equal(state.turnTerminals["turn-1"]?.status, "completed");
  assert.equal(state.turnTerminals["turn-1"]?.sourceEventId, "5");
});

test("turn.usage records latest usage without closing the active turn", () => {
  const firstUsage = { input_tokens: 100, output_tokens: 25, total_tokens: 125 };
  const latestUsage = { input_tokens: 120, output_tokens: 30, total_tokens: 150 };
  const usageObservation = {
    usage_source: "thread.tokenUsage.updated",
    provider_turn_id: "provider-turn-1",
    update_count: 2,
  };
  const state = reduceConversationEvents([
    ev("1", "turn.submitted", { source: "codex" }),
    ev("2", "turn.started", { source: "codex" }),
    ev("3", "turn.usage", {
      source: "codex",
      payload: {
        usage: firstUsage,
        usage_observation: {
          usage_source: "thread.tokenUsage.updated",
          provider_turn_id: "provider-turn-1",
          update_count: 1,
        },
      },
    }),
    ev("4", "turn.usage", {
      source: "codex",
      payload: {
        usage: latestUsage,
        usage_observation: usageObservation,
      },
    }),
  ]);

  assert.equal(state.runStatus, "streaming");
  assert.equal(state.activeTurnId, "turn-1");
  assert.deepEqual(state.lastUsage, latestUsage);
  assert.equal(state.turnUsages["turn-1"]?.orderKey, "0003");
  assert.equal(state.turnUsages["turn-1"]?.endOrderKey, "0004");
  assert.deepEqual(state.turnUsages["turn-1"]?.usage, latestUsage);
  assert.deepEqual(state.turnUsages["turn-1"]?.usageObservation, usageObservation);
});

test("origin_session_id on user message flows onto ConversationMessage", () => {
  // Cross-session handoff path: a sibling tank-operator session
  // (id=42) posted this turn via mcp-tank-operator. The orchestrator
  // stamps the originating id onto the event envelope so the renderer
  // can pick the parent session's avatar for the user bubble instead
  // of the human owner's Gravatar.
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "handoff-1",
      payload: { text: "fix the avatar bug" },
      origin_session_id: "42",
    } as Partial<TankConversationEvent>),
  ]);

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.originSessionId, "42");
});

test("user message without origin_session_id leaves originSessionId undefined", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "human-1",
      payload: { text: "I typed this myself" },
    }),
  ]);

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.originSessionId, undefined);
});

test("author_kind on user message flows onto ConversationMessage", () => {
  // Bot-token (purpose=bot) authored turn: the orchestrator stamps
  // author_kind="system" so the renderer shows the session's system
  // identity instead of the human owner's avatar.
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "bot-1",
      payload: { text: "posted via bot token" },
      author_kind: "system",
    } as Partial<TankConversationEvent>),
  ]);

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.authorKind, "system");
});

test("user message without author_kind leaves authorKind undefined", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "human-2",
      payload: { text: "I typed this myself" },
    }),
  ]);

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.authorKind, undefined);
});

test("Tool lifecycle replays to a completed tool item", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started"),
    ev("2", "item.started", {
      actor: "tool",
      source: "claude",
      timeline_id: "toolu-read",
      created_at: "2026-05-12T00:00:10.000Z",
      payload: { kind: "tool", title: "Read", text: "{\"file_path\":\"README.md\"}" },
    }),
    ev("3", "item.completed", {
      actor: "tool",
      source: "claude",
      timeline_id: "toolu-read",
      created_at: "2026-05-12T00:00:15.000Z",
      payload: { kind: "tool", title: "Read", text: "README contents" },
    }),
    ev("4", "turn.completed"),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.activeItemId, null);
  assert.deepEqual(state.items.map((item) => [item.id, item.status, item.title]), [
    ["toolu-read", "completed", "Read"],
  ]);
  assert.equal(state.items[0]?.startedAt, "2026-05-12T00:00:10.000Z");
  assert.equal(state.items[0]?.completedAt, "2026-05-12T00:00:15.000Z");
});

test("Background shell task lifecycle replays independent of active tool state", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "shell_task.started", {
      actor: "tool",
      source: "claude",
      timeline_id: "turn-1:shell_task:task-abc",
      task_id: "task-abc",
      provider_item_id: "toolu-monitor",
      created_at: "2026-05-12T00:00:10.000Z",
      payload: {
        kind: "shell_task",
        task_id: "task-abc",
        status: "running",
        tool_use_id: "toolu-monitor",
        summary: "Watching logs",
        command: "tail -f app.log",
        cwd: "/workspace/app",
        process_id: "proc-abc",
        output: "booting\n",
      },
    }),
    ev("3", "turn.completed", { source: "claude" }),
    ev("4", "shell_task.exited", {
      actor: "tool",
      source: "claude",
      timeline_id: "turn-1:shell_task:task-abc",
      task_id: "task-abc",
      provider_item_id: "toolu-monitor",
      created_at: "2026-05-12T00:00:20.000Z",
      payload: {
        kind: "shell_task",
        task_id: "task-abc",
        status: "completed",
        summary: "Log watch finished",
        output: "booting\nready\n",
        exit_code: 0,
        duration_ms: 10_000,
      },
    }),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.items.length, 0);
  assert.equal(state.backgroundTasks.length, 1);
  assert.equal(state.backgroundTasks[0]?.status, "completed");
  assert.equal(state.backgroundTasks[0]?.taskId, "task-abc");
  assert.equal(state.backgroundTasks[0]?.toolUseId, "toolu-monitor");
  assert.equal(state.backgroundTasks[0]?.summary, "Log watch finished");
  assert.equal(state.backgroundTasks[0]?.command, "tail -f app.log");
  assert.equal(state.backgroundTasks[0]?.cwd, "/workspace/app");
  assert.equal(state.backgroundTasks[0]?.processId, "proc-abc");
  assert.equal(state.backgroundTasks[0]?.output, "booting\nready\n");
  assert.equal(state.backgroundTasks[0]?.exitCode, 0);
  assert.equal(state.backgroundTasks[0]?.durationMs, 10_000);
  assert.equal(state.backgroundTasks[0]?.startedAt, "2026-05-12T00:00:10.000Z");
  assert.equal(state.backgroundTasks[0]?.completedAt, "2026-05-12T00:00:20.000Z");
});

test("Codex userMessage provider echo items are ignored on frontend replay", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      source: "tank",
      client_nonce: "run-codex",
      payload: { text: "hello" },
    }),
    ev("2", "item.started", {
      actor: "tool",
      source: "codex",
      timeline_id: "turn-1:item:item-user-echo",
      provider_item_id: "item-user-echo",
      payload: {
        kind: "userMessage",
        title: "userMessage",
        text: "hello",
        raw_item: {
          id: "item-user-echo",
          type: "userMessage",
          text: "hello",
        },
      },
    }),
    ev("3", "item.completed", {
      actor: "tool",
      source: "codex",
      timeline_id: "turn-1:item:item-user-echo",
      provider_item_id: "item-user-echo",
      payload: {
        kind: "userMessage",
        title: "userMessage",
        text: "hello",
        outcome: { kind: "ok" },
        raw_item: {
          id: "item-user-echo",
          type: "userMessage",
          text: "hello",
        },
      },
    }),
    ev("4", "item.completed", {
      actor: "assistant",
      source: "codex",
      timeline_id: "turn-1:item:item-agent-message",
      provider_item_id: "item-agent-message",
      payload: {
        kind: "agent_message",
        title: "agent_message",
        text: "hi",
        outcome: { kind: "ok" },
      },
    }),
  ]);

  assert.equal(state.messages.length, 1);
  assert.deepEqual(
    state.items.map((item) => [item.id, item.kind, item.text]),
    [["turn-1:item:item-agent-message", "agent_message", "hi"]],
  );
});

test("Late item.started does not regress a completed tool back to running", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started"),
    ev("3", "item.completed", {
      actor: "tool",
      source: "codex",
      timeline_id: "tool-call",
      provider_item_id: "item_1",
      payload: {
        kind: "command_execution",
        title: "/bin/sh -lc \"printf 'success\\n'\"",
        raw_item: {
          id: "item_1",
          type: "command_execution",
          status: "completed",
          command: "/bin/sh -lc \"printf 'success\\n'\"",
          exit_code: 0,
          aggregated_output: "success\n",
        },
      },
    }),
    ev("2", "item.started", {
      actor: "tool",
      source: "codex",
      timeline_id: "tool-call",
      provider_item_id: "item_1",
      payload: {
        kind: "command_execution",
        title: "/bin/sh -lc \"printf 'success\\n'\"",
        raw_item: {
          id: "item_1",
          type: "command_execution",
          status: "in_progress",
          command: "/bin/sh -lc \"printf 'success\\n'\"",
          exit_code: null,
          aggregated_output: "",
        },
      },
    }),
  ]);

  assert.equal(state.items.length, 1);
  assert.equal(state.items[0]?.status, "completed");
  assert.equal(state.activeItemId, null);
  assert.deepEqual(state.items[0]?.payload?.raw_item, {
    id: "item_1",
    type: "command_execution",
    status: "completed",
    command: "/bin/sh -lc \"printf 'success\\n'\"",
    exit_code: 0,
    aggregated_output: "success\n",
  });
});

test("Late item.started does not regress a failed result back to running", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started"),
    ev("3", "item.completed", {
      actor: "tool",
      source: "codex",
      timeline_id: "tool-call",
      provider_item_id: "item_1",
      payload: {
        kind: "command_execution",
        title: "/bin/sh -lc false",
        raw_item: {
          id: "item_1",
          type: "command_execution",
          status: "failed",
          command: "/bin/sh -lc false",
          exit_code: 1,
          aggregated_output: "",
        },
      },
    }),
    ev("2", "item.started", {
      actor: "tool",
      source: "codex",
      timeline_id: "tool-call",
      provider_item_id: "item_1",
      payload: {
        kind: "command_execution",
        title: "/bin/sh -lc false",
        raw_item: {
          id: "item_1",
          type: "command_execution",
          status: "in_progress",
          command: "/bin/sh -lc false",
          exit_code: null,
          aggregated_output: "",
        },
      },
    }),
  ]);

  assert.equal(state.items.length, 1);
  assert.equal(state.items[0]?.status, "failed");
  assert.equal(state.activeItemId, null);
});

test("Duplicate user submissions with the same client nonce do not duplicate bubbles", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "same prompt" },
    }),
    ev("2", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "same prompt" },
    }),
  ]);

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0]?.text, "same prompt");
});

test("Approval pause is explicit needs-input state and resumes streaming", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { turn_id: "turn-approval" }),
    ev("2", "tool.approval_requested", {
      turn_id: "turn-approval",
      timeline_id: "approval-1",
      actor: "tool",
      payload: { kind: "approval", title: "Run tests" },
    }),
    ev("3", "tool.approval_resolved", {
      turn_id: "turn-approval",
      timeline_id: "approval-1",
      actor: "tool",
      payload: { decision: "allow" },
    }),
  ]);

  assert.equal(state.needsInput, false);
  assert.equal(state.runStatus, "streaming");
  assert.equal(state.items[0]?.kind, "approval");
});

test("Provider error becomes terminal error state without needs-input", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "tool.approval_requested", {
      timeline_id: "approval-1",
      actor: "tool",
      payload: { kind: "approval", title: "Run command" },
    }),
    ev("3", "turn.failed", {
      source: "claude",
      payload: { reason: "provider_failure", error: "quota exceeded" },
    }),
  ]);

  assert.equal(state.runStatus, "error");
  assert.equal(state.failed, true);
  assert.equal(state.needsInput, false);
  assert.equal(state.activeTurnId, null);
  assert.equal(state.turnTerminals["turn-1"]?.status, "failed");
  assert.equal(state.turnTerminals["turn-1"]?.sourceEventId, "3");
});

test("turn.interrupt_requested transitions streaming → stopping", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "long task" },
    }),
    ev("2", "turn.submitted", { client_nonce: "run-1" }),
    ev("3", "turn.started", { source: "claude" }),
    ev("4", "turn.interrupt_requested", {
      actor: "system",
      source: "tank",
    }),
  ]);

  assert.equal(state.runStatus, "stopping");
  assert.equal(state.activeTurnId, "turn-1");
  assert.equal(state.interruptRequests.length, 1);
  assert.equal(state.interruptRequests[0]?.turnId, "turn-1");
});

test("turn.interrupt_requested → turn.interrupted resolves to stopped", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("3", "turn.interrupted", { source: "claude", payload: { reason: "client_interrupt" } }),
  ]);

  assert.equal(state.runStatus, "stopped");
  assert.equal(state.activeTurnId, null);
  assert.equal(state.interruptRequests.length, 1);
  assert.equal(state.turnTerminals["turn-1"]?.status, "interrupted");
  assert.equal(state.turnTerminals["turn-1"]?.sourceEventId, "3");
});

test("turn.interrupt_requested losing race to turn.completed resolves to ready", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("3", "turn.completed", { source: "claude" }),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.activeTurnId, null);
  // Chip stays as transcript evidence even though stop "lost the race."
  assert.equal(state.interruptRequests.length, 1);
  assert.equal(state.turnTerminals["turn-1"]?.status, "completed");
  assert.equal(state.turnTerminals["turn-1"]?.sourceEventId, "3");
});

test("turn.interrupt_requested followed by turn.command_failed resolves to error", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("3", "turn.command_failed", {
      actor: "system",
      source: "tank",
      payload: { reason: "publish_interrupt_failed" },
    }),
  ]);

  assert.equal(state.runStatus, "error");
  assert.equal(state.failed, true);
  assert.equal(state.interruptRequests.length, 1);
  assert.equal(state.turnTerminals["turn-1"]?.status, "failed");
  assert.equal(state.turnTerminals["turn-1"]?.sourceEventId, "3");
});

test("Late turn.interrupt_requested after terminal state does NOT downgrade run status", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.completed", { source: "claude" }),
    ev("3", "turn.interrupt_requested", { actor: "system", source: "tank" }),
  ]);

  // Stop request lands after the turn already cleanly finished. Chip is
  // recorded for transcript transparency, but runStatus stays ready.
  assert.equal(state.runStatus, "ready");
  assert.equal(state.interruptRequests.length, 1);
});

// item.failed marks a single tool call as errored; the agent typically
// keeps running. Previously this flipped runStatus to "error" and set
// failed=true, leaving the in-pane status indicator pinned red for an
// otherwise healthy session. Session-level error is owned by turn.failed /
// turn.command_failed (durable turn-terminal events). The per-item error
// badge in the transcript still renders off the item's "failed" status.
test("item.failed mid-turn does NOT flip runStatus or set failed", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-tool-error",
      payload: { text: "do thing" },
    }),
    ev("2", "turn.submitted", { client_nonce: "run-tool-error" }),
    ev("3", "turn.started", { source: "claude" }),
    ev("4", "item.started", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-1",
      payload: { kind: "tool", name: "Bash", input: { command: "false" } },
    }),
    ev("5", "item.failed", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-1",
      payload: { kind: "tool_result", is_error: true, output: "exit 1" },
    }),
  ]);

  assert.equal(state.runStatus, "streaming");
  assert.equal(state.failed, false);
  assert.equal(state.lastError, null);
  assert.equal(state.activeTurnId, "turn-1");
  // The per-item failure must still show in the transcript so the
  // user sees the orange error badge under the tool call.
  const failedItem = state.items.find((item) => item.id === "tool-1");
  assert.equal(failedItem?.status, "failed");
});

test("completed item with result_failed outcome marks the item failed without failing the session", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "codex" }),
    ev("2", "item.completed", {
      actor: "tool",
      source: "codex",
      timeline_id: "tool-warn",
      payload: {
        kind: "command_execution",
        title: "npm test",
        outcome: { kind: "result_failed", reason: "exit_code", code: 1 },
      },
    }),
  ]);

  assert.equal(state.runStatus, "streaming");
  assert.equal(state.failed, false);
  assert.equal(state.items.find((item) => item.id === "tool-warn")?.status, "failed");
});

test("completed command item with legacy nonzero raw exit code marks the item failed", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "codex" }),
    ev("2", "item.completed", {
      actor: "tool",
      source: "codex",
      timeline_id: "legacy-exit",
      payload: {
        kind: "command_execution",
        title: "/bin/sh -lc 'exit 1'",
        raw_item: { exit_code: 1 },
      },
    }),
  ]);

  assert.equal(state.items.find((item) => item.id === "legacy-exit")?.status, "failed");
});

test("tool.approval_resolved preserves failed item status from result_failed outcome", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "tool.approval_requested", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-question",
      payload: { kind: "needs_input" },
    }),
    ev("3", "tool.approval_resolved", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-question",
      payload: {
        kind: "needs_input",
        resolved: true,
        outcome: { kind: "result_failed", reason: "claude_tool_result_is_error" },
      },
    }),
  ]);

  assert.equal(state.items.find((item) => item.id === "tool-question")?.status, "failed");
});

test("turn.completed after a mid-turn item.failed resolves to ready, not error", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "item.failed", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-x",
      payload: { kind: "tool_result", is_error: true },
    }),
    ev("3", "item.completed", {
      actor: "assistant",
      source: "claude",
      timeline_id: "msg-recover",
      payload: { kind: "message", text: "I'll try a different approach." },
    }),
    ev("4", "turn.completed", { source: "claude" }),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.failed, false);
  assert.equal(state.lastError, null);
});

test("Duplicate turn.interrupt_requested events dedupe by event_id", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("dup", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("dup", "turn.interrupt_requested", { actor: "system", source: "tank" }),
  ]);

  assert.equal(state.runStatus, "stopping");
  assert.equal(state.interruptRequests.length, 1);
});

test("Timeline replay and SSE delivery converge through event id dedupe", () => {
  const events = [
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "hello" },
    }),
    ev("2", "turn.started"),
    ev("3", "item.completed", {
      actor: "assistant",
      timeline_id: "msg-1",
      payload: { kind: "message", text: "world" },
    }),
    ev("4", "turn.completed"),
  ];
  const replayOnly = reduceConversationEvents(events);
  const replayThenLive = reduceConversationEvents(events, reduceConversationEvents(events));

  assert.deepEqual(replayThenLive, replayOnly);
  assert.notDeepEqual(replayOnly, initialConversationState);
});

test("contract guard rejects malformed per-type events", () => {
  assert.equal(isTankConversationEvent(ev("10", "user_message.created", {
    actor: "user",
    timeline_id: "turn-1:user",
    client_nonce: "client-1",
    payload: { text: "hello", display: { kind: "plain" } },
  })), true);

  assert.equal(isTankConversationEvent(ev("session:63:status:loading", "session.status", {
    actor: "system",
    timeline_id: "session:63:status:loading",
    payload: { status: "loading", text: "Session is loading." },
  })), true);

  assert.equal(isTankConversationEvent(ev("turn-1:usage:1", "turn.usage", {
    source: "codex",
    payload: { usage: { input_tokens: 1, output_tokens: 1 } },
  })), true);

  assert.equal(isTankConversationEvent({
    event_id: "bad-user",
    order_key: "bad-user",
    session_id: "63",
    turn_id: "turn-1",
    actor: "user",
    source: "tank",
    type: "user_message.created",
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    payload: { text: "hello" },
  }), false);

  assert.equal(isTankConversationEvent({
    event_id: "bad-session-status",
    order_key: "bad-session-status",
    session_id: "63",
    timeline_id: "session:63:status:loading",
    actor: "system",
    source: "tank",
    type: "session.status",
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    payload: { status: "loading" },
  }), false);

  assert.equal(isTankConversationEvent({
    event_id: "bad-usage",
    order_key: "bad-usage",
    session_id: "63",
    turn_id: "turn-1",
    actor: "runner",
    source: "codex",
    type: "turn.usage",
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    payload: {},
  }), false);

  assert.equal(isTankConversationEvent({
    event_id: "bad-item",
    order_key: "bad-item",
    session_id: "63",
    turn_id: "turn-1",
    actor: "assistant",
    source: "claude",
    type: "item.completed",
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    payload: { kind: "message" },
  }), false);
});
