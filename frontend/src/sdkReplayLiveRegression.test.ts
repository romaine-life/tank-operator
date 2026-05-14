import assert from "node:assert/strict";
import { test } from "node:test";

import { reduceConversationEvents } from "./conversationReducer.ts";
import { projectConversationState } from "./conversationProjection.ts";
import type { TankConversationEvent } from "./tankConversation.ts";

function ev(
  index: number,
  type: TankConversationEvent["type"],
  fields: Partial<TankConversationEvent> = {},
): TankConversationEvent {
  const orderKey = `order-${String(index).padStart(3, "0")}`;
  const defaults: Partial<TankConversationEvent> = {};
  if (type === "user_message.created") {
    defaults.actor = "user";
    defaults.timeline_id = "turn-414:user";
    defaults.client_nonce = "client-run-414";
  }
  if (type === "turn.submitted") {
    defaults.client_nonce = "client-run-414";
    defaults.payload = { status: "submitted" };
  }
  return {
    event_id: `evt-${String(index).padStart(3, "0")}`,
    order_key: orderKey,
    session_id: "sdk-session-414",
    conversation_id: "sdk-session-414",
    turn_id: "turn-414",
    actor: "runner",
    source: "tank",
    type,
    created_at: "2026-05-12T00:00:00.000Z",
    visibility: "durable",
    ...defaults,
    ...fields,
  };
}

function replayAfterLastOrderKey(
  events: readonly TankConversationEvent[],
  lastOrderKey: string | null,
): TankConversationEvent[] {
  assert.ok(lastOrderKey, "resume cursor should be non-empty");
  return events.filter((event) => (event.order_key ?? "") > lastOrderKey);
}

test("SDK replay and duplicate live delivery converge after reconnect", () => {
  const canonicalTurn = [
    ev(1, "user_message.created", {
      actor: "user",
      client_nonce: "client-run-414",
      payload: { text: "Investigate issue #414" },
    }),
    ev(2, "turn.submitted", {
      client_nonce: "client-run-414",
    }),
    ev(3, "turn.started", {
      source: "claude",
    }),
    ev(4, "item.started", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-github-issue",
      payload: {
        kind: "mcp_tool_call",
        server: "github",
        tool: "get_issue",
        input: { number: 414 },
      },
    }),
    ev(5, "item.completed", {
      actor: "tool",
      source: "claude",
      timeline_id: "tool-github-issue",
      payload: {
        kind: "mcp_tool_call",
        server: "github",
        tool: "get_issue",
        output: "Add replay/live resume regression test",
      },
    }),
    ev(6, "item.completed", {
      actor: "assistant",
      source: "claude",
      timeline_id: "assistant-final",
      payload: {
        kind: "message",
        text: "Replay and live delivery now converge.",
      },
    }),
    ev(7, "turn.completed", {
      source: "claude",
      payload: { usage: { input_tokens: 10, output_tokens: 8 } },
    }),
  ] satisfies TankConversationEvent[];

  const liveBeforeDisconnect = canonicalTurn.slice(0, 4);
  const liveState = reduceConversationEvents(liveBeforeDisconnect);
  const liveProjection = projectConversationState(liveState);
  const reconnectCursor = liveProjection.lastOrderKey;

  assert.equal(reconnectCursor, "order-004");
  assert.equal(liveProjection.runStatus, "streaming");
  assert.equal(liveProjection.activeToolName, "github.get_issue");

  const replayedFromTimeline = replayAfterLastOrderKey(canonicalTurn, reconnectCursor);
  assert.deepEqual(
    replayedFromTimeline.map((event) => event.event_id),
    ["evt-005", "evt-006", "evt-007"],
  );

  const stateAfterReplay = reduceConversationEvents(
    [...replayedFromTimeline, ...replayedFromTimeline],
    liveState,
  );
  const duplicateLiveFramesAfterReconnect = [canonicalTurn[4], canonicalTurn[5], canonicalTurn[6]];
  const finalState = reduceConversationEvents(duplicateLiveFramesAfterReconnect, stateAfterReplay);
  const expectedState = reduceConversationEvents(canonicalTurn);

  assert.deepEqual(finalState, expectedState);

  const finalProjection = projectConversationState(finalState);
  const messageEntries = finalProjection.entries.filter((entry) => entry.kind === "message");
  const toolEntries = finalProjection.entries.filter((entry) => entry.kind === "tool");

  assert.deepEqual(
    messageEntries.map((entry) => (entry.kind === "message" ? [entry.role, entry.text] : [])),
    [
      ["user", "Investigate issue #414"],
      ["assistant", "Replay and live delivery now converge."],
    ],
  );
  assert.equal(toolEntries.length, 1);
  if (toolEntries[0]?.kind === "tool") {
    assert.equal(toolEntries[0].toolName, "github.get_issue");
    assert.equal(toolEntries[0].toolStatus, "completed");
    assert.match(toolEntries[0].toolInput ?? "", /"number": 414/);
    assert.equal(toolEntries[0].toolOutput, "Add replay/live resume regression test");
  }
  assert.equal(finalProjection.runStatus, "ready");
  assert.equal(finalProjection.activeTurnId, null);
  assert.equal(finalProjection.activeToolName, null);
  assert.equal(finalProjection.failed, false);
  assert.equal(finalProjection.lastOrderKey, "order-007");
  assert.equal(new Set(finalProjection.entries.map((entry) => entry.id)).size, 3);
});

test("SDK interrupted replay stays terminal when replay and live frames duplicate", () => {
  const interruptedTurn = [
    ev(1, "user_message.created", {
      actor: "user",
      client_nonce: "client-run-stop",
      payload: { text: "stop" },
    }),
    ev(2, "turn.started", { source: "codex" }),
    ev(3, "turn.interrupted", {
      source: "codex",
      payload: { reason: "client_interrupt" },
    }),
  ] satisfies TankConversationEvent[];

  const liveState = reduceConversationEvents(interruptedTurn.slice(0, 2));
  const reconnectCursor = projectConversationState(liveState).lastOrderKey;
  const replayedFromTimeline = replayAfterLastOrderKey(interruptedTurn, reconnectCursor);
  const finalProjection = projectConversationState(
    reduceConversationEvents(
      [...replayedFromTimeline, ...replayedFromTimeline, interruptedTurn[2]],
      liveState,
    ),
  );

  assert.equal(reconnectCursor, "order-002");
  assert.equal(finalProjection.stopped, true);
  assert.equal(finalProjection.failed, false);
  assert.equal(finalProjection.runStatus, "stopped");
  assert.equal(finalProjection.entries.length, 1);
  assert.equal(finalProjection.entries[0]?.kind, "message");
  if (finalProjection.entries[0]?.kind === "message") {
    assert.equal(finalProjection.entries[0].role, "user");
    assert.equal(finalProjection.entries[0].text, "stop");
  }
});
