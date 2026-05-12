import assert from "node:assert/strict";
import { test } from "node:test";

import {
  initialConversationState,
  reduceConversationEvents,
} from "./conversationReducer.ts";
import type { TankConversationEvent } from "./tankConversation.ts";

function ev(
  event_id: string,
  type: TankConversationEvent["type"],
  fields: Partial<TankConversationEvent> = {},
): TankConversationEvent {
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

test("Tool lifecycle replays to a completed tool item", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started"),
    ev("2", "item.started", {
      actor: "tool",
      source: "claude",
      item_id: "toolu-read",
      payload: { kind: "tool", title: "Read", text: "{\"file_path\":\"README.md\"}" },
    }),
    ev("3", "item.completed", {
      actor: "tool",
      source: "claude",
      item_id: "toolu-read",
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
      item_id: "approval-1",
      actor: "tool",
      payload: { kind: "approval", title: "Run tests" },
    }),
    ev("3", "tool.approval_resolved", {
      turn_id: "turn-approval",
      item_id: "approval-1",
      actor: "tool",
      payload: { decision: "allow" },
    }),
  ]);

  assert.equal(state.needsInput, false);
  assert.equal(state.runStatus, "streaming");
  assert.equal(state.items[0]?.kind, "approval");
});

test("Replay and live delivery converge through event id dedupe", () => {
  const events = [
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "hello" },
    }),
    ev("2", "turn.started"),
    ev("3", "item.completed", {
      actor: "assistant",
      item_id: "msg-1",
      payload: { kind: "message", text: "world" },
    }),
    ev("4", "turn.completed"),
  ];
  const replayOnly = reduceConversationEvents(events);
  const replayThenLive = reduceConversationEvents(events, reduceConversationEvents(events));

  assert.deepEqual(replayThenLive, replayOnly);
  assert.notDeepEqual(replayOnly, initialConversationState);
});
