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

test("Tool lifecycle replays to a completed tool item", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started"),
    ev("2", "item.started", {
      actor: "tool",
      source: "claude",
      timeline_id: "toolu-read",
      payload: { kind: "tool", title: "Read", text: "{\"file_path\":\"README.md\"}" },
    }),
    ev("3", "item.completed", {
      actor: "tool",
      source: "claude",
      timeline_id: "toolu-read",
      payload: { kind: "tool", title: "Read", text: "README contents" },
    }),
    ev("4", "turn.completed"),
  ]);

  assert.equal(state.runStatus, "ready");
  assert.equal(state.activeItemId, null);
  assert.deepEqual(state.items.map((item) => [item.id, item.status, item.title]), [
    ["toolu-read", "completed", "Read"],
  ]);
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
