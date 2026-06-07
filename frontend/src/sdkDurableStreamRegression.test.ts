import { test, expect } from "vitest";

import { reduceConversationEvents } from "./conversationReducer.ts";
import { projectConversationState } from "./conversationProjection.ts";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";

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

function streamAfterLastOrderKey(
  events: readonly TankConversationEvent[],
  lastOrderKey: string | null,
): TankConversationEvent[] {
  expect(lastOrderKey, "resume cursor should be non-empty").toBeTruthy();
  return events.filter((event) => (event.order_key ?? "") > lastOrderKey);
}

test("durable stream resumes from the last order key without duplicating transcript units", () => {
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
        output: "Add durable stream resume regression test",
      },
    }),
    ev(6, "item.completed", {
      actor: "assistant",
      source: "claude",
      timeline_id: "assistant-final",
      payload: {
        kind: "message",
        text: "Durable stream delivery now converges.",
      },
    }),
    ev(7, "turn.completed", {
      source: "claude",
      payload: { usage: { input_tokens: 10, output_tokens: 8 } },
    }),
  ] satisfies TankConversationEvent[];

  const stateBeforeDisconnect = reduceConversationEvents(canonicalTurn.slice(0, 4));
  const projectionBeforeDisconnect = projectConversationState(stateBeforeDisconnect);
  const reconnectCursor = projectionBeforeDisconnect.lastOrderKey;

  expect(reconnectCursor).toBe("order-004");
  expect(projectionBeforeDisconnect.runStatus).toBe("streaming");
  expect(projectionBeforeDisconnect.activeToolName).toBe("github.get_issue");

  const streamedAfterReconnect = streamAfterLastOrderKey(canonicalTurn, reconnectCursor);
  expect(streamedAfterReconnect.map((event) => event.event_id)).toEqual(["evt-005", "evt-006", "evt-007"]);

  const finalState = reduceConversationEvents(streamedAfterReconnect, stateBeforeDisconnect);
  const expectedState = reduceConversationEvents(canonicalTurn);
  expect(finalState).toEqual(expectedState);

  const finalProjection = projectConversationState(finalState);
  const messageEntries = finalProjection.entries.filter((entry) => entry.kind === "message");
  const toolEntries = finalProjection.entries.filter((entry) => entry.kind === "tool");

  expect(messageEntries.map((entry) => (entry.kind === "message" ? [entry.role, entry.text] : []))).toEqual([
          ["user", "Investigate issue #414"],
          ["assistant", "Durable stream delivery now converges."],
        ]);
  expect(toolEntries.length).toBe(1);
  if (toolEntries[0]?.kind === "tool") {
    expect(toolEntries[0].toolName).toBe("github.get_issue");
    expect(toolEntries[0].toolStatus).toBe("completed");
    expect(toolEntries[0].toolInput ?? "").toMatch(/"number": 414/);
    expect(toolEntries[0].toolOutput).toBe("Add durable stream resume regression test");
  }
  expect(finalProjection.runStatus).toBe("ready");
  expect(finalProjection.activeTurnId).toBe(null);
  expect(finalProjection.activeToolName).toBe(null);
  expect(finalProjection.failed).toBe(false);
  expect(finalProjection.lastOrderKey).toBe("order-007");
  expect(new Set(finalProjection.entries.map((entry) => entry.id)).size).toBe(3);
});

test("stop-then-reconnect: durable replay reconstructs the stopping projection", () => {
  // A user pressed Stop mid-turn, the durable turn.interrupt_requested
  // landed, then the browser disconnected before the SSE caught up. On
  // reconnect, the timeline replay alone (no live SSE yet) must produce
  // the same projection state the live path would have — including the
  // "stopping" run status and the Stop requested chip.
  const stoppingTurn = [
    ev(1, "user_message.created", {
      actor: "user",
      client_nonce: "client-run-414",
      payload: { text: "Long task" },
    }),
    ev(2, "turn.submitted", { client_nonce: "client-run-414" }),
    ev(3, "turn.started", { source: "claude" }),
    ev(4, "turn.interrupt_requested", {
      actor: "system",
      source: "tank",
    }),
  ] satisfies TankConversationEvent[];

  const livePath = reduceConversationEvents(stoppingTurn);
  const replayPath = reduceConversationEvents(stoppingTurn);

  expect(replayPath).toEqual(livePath);
  expect(replayPath.runStatus).toBe("stopping");
  expect(replayPath.activeTurnId).toBe("turn-414");
  expect(replayPath.interruptRequests.length).toBe(1);

  const projection = projectConversationState(replayPath);
  const metaEntries = projection.entries.filter((entry) => entry.kind === "meta");
  expect(metaEntries.length).toBe(1);
  if (metaEntries[0]?.kind === "meta") {
    expect(metaEntries[0].meta.title).toBe("Stop requested");
  }
  expect(projection.stopping).toBe(true);
});
