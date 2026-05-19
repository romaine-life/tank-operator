import assert from "node:assert/strict";
import { test } from "node:test";

import { reduceConversationEvents } from "./conversationReducer.ts";
import { projectConversationState } from "./conversationProjection.ts";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";

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

test("turn.interrupt_requested renders a 'Stop requested' meta chip at its order_key", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
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
    ]),
  );

  assert.equal(projection.runStatus, "stopping");
  assert.equal(projection.stopping, true);
  const meta = projection.entries.find((entry) => entry.kind === "meta");
  assert.ok(meta, "Stop requested chip should appear in projection entries");
  if (meta?.kind === "meta") {
    assert.equal(meta.meta.title, "Stop requested");
    assert.equal(meta.meta.severity, "info");
    assert.equal(meta.turnId, "turn-1");
    assert.equal(meta.orderKey, "0004");
  }
});

test("projects canonical user and assistant events into chat messages", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "hello" },
    }),
    ev("2", "turn.started", { source: "claude" }),
    ev("3", "item.completed", {
      actor: "assistant",
      source: "claude",
      timeline_id: "assistant-1",
      payload: { kind: "message", text: "world" },
    }),
    ev("4", "turn.completed", { source: "claude" }),
  ]);

  const projection = projectConversationState(state);

  assert.deepEqual(
    projection.entries.map((entry) =>
      entry.kind === "message" ? [entry.role, entry.text] : [entry.kind],
    ),
    [
      ["user", "hello"],
      ["assistant", "world"],
    ],
  );
  assert.equal(projection.runStatus, "ready");
});

test("keys assistant messages by Tank timeline id, not provider item id", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        turn_id: "turn-1",
        timeline_id: "turn-1:user",
        client_nonce: "run-1",
        payload: { text: "first prompt" },
      }),
      ev("2", "item.completed", {
        actor: "assistant",
        source: "codex",
        turn_id: "turn-1",
        timeline_id: "turn-1:item:item_0",
        provider_item_id: "item_0",
        payload: { kind: "message", text: "first answer" },
      }),
      ev("3", "user_message.created", {
        actor: "user",
        turn_id: "turn-2",
        timeline_id: "turn-2:user",
        client_nonce: "run-2",
        payload: { text: "second prompt" },
      }),
      ev("4", "item.completed", {
        actor: "assistant",
        source: "codex",
        turn_id: "turn-2",
        timeline_id: "turn-2:item:item_0",
        provider_item_id: "item_0",
        payload: { kind: "message", text: "second answer" },
      }),
    ]),
  );

  assert.deepEqual(
    projection.entries.map((entry) =>
      entry.kind === "message" ? [entry.role, entry.text] : [entry.kind],
    ),
    [
      ["user", "first prompt"],
      ["assistant", "first answer"],
      ["user", "second prompt"],
      ["assistant", "second answer"],
    ],
  );
});

test("projects canonical tool lifecycle and active tool state", () => {
  const running = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", {
        source: "claude",
        created_at: "2026-05-12T00:00:00.000Z",
      }),
      ev("2", "item.started", {
        actor: "tool",
        source: "claude",
        timeline_id: "toolu-read",
        created_at: "2026-05-12T00:00:10.000Z",
        payload: {
          kind: "tool",
          title: "Read",
          input: { file_path: "README.md" },
        },
      }),
    ]),
  );

  assert.equal(running.activeToolName, "Read");
  assert.equal(running.entries[0]?.kind, "tool");
  if (running.entries[0]?.kind === "tool") {
    assert.equal(running.entries[0].toolStatus, "started");
    assert.match(running.entries[0].toolInput ?? "", /README\.md/);
    assert.equal(running.entries[0].startedAt, "2026-05-12T00:00:10.000Z");
    assert.equal(running.entries[0].completedAt, undefined);
  }

  const completed = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", { source: "claude" }),
      ev("2", "item.started", {
        actor: "tool",
        source: "claude",
        timeline_id: "toolu-read",
        created_at: "2026-05-12T00:00:10.000Z",
        payload: { kind: "tool", title: "Read" },
      }),
      ev("3", "item.completed", {
        actor: "tool",
        source: "claude",
        timeline_id: "toolu-read",
        created_at: "2026-05-12T00:00:15.000Z",
        payload: { kind: "tool_result", output: "README contents" },
      }),
    ]),
  );

  assert.equal(completed.activeToolName, null);
  assert.equal(completed.entries[0]?.kind, "tool");
  if (completed.entries[0]?.kind === "tool") {
    assert.equal(completed.entries[0].toolName, "Read");
    assert.equal(completed.entries[0].toolStatus, "completed");
    assert.equal(completed.entries[0].toolOutput, "README contents");
    assert.equal(completed.entries[0].time, "2026-05-12T00:00:10.000Z");
    assert.equal(completed.entries[0].startedAt, "2026-05-12T00:00:10.000Z");
    assert.equal(completed.entries[0].completedAt, "2026-05-12T00:00:15.000Z");
  }
});

test("projects active client nonce for resumed running turn", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        turn_id: "turn-resumed",
        timeline_id: "turn-resumed:user",
        client_nonce: "client-resumed-1",
        payload: { text: "keep working" },
      }),
      ev("2", "turn.started", {
        source: "claude",
        turn_id: "turn-resumed",
        client_nonce: "client-resumed-1",
      }),
    ]),
  );

  assert.equal(projection.runStatus, "streaming");
  assert.equal(projection.activeTurnId, "turn-resumed");
  assert.equal(projection.activeClientNonce, "client-resumed-1");
});

test("canonical duplicate delivery converges before projection", () => {
  const events = [
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "run-1",
      payload: { text: "once" },
    }),
    ev("2", "turn.interrupted", {
      source: "codex",
      payload: { reason: "client_interrupt" },
    }),
  ];

  const projection = projectConversationState(
    reduceConversationEvents([...events, ...events]),
  );

  assert.equal(projection.entries.length, 1);
  assert.equal(projection.stopped, true);
  assert.equal(projection.failed, false);
});

test("projects durable skill invocation display metadata", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        timeline_id: "turn-1:user",
        client_nonce: "client-1",
        payload: {
          text: "/test\n\nplease verify",
          display: {
            kind: "skill_invocation",
            skill_name: "test",
            supplemental_text: "please verify",
          },
        },
      }),
    ]),
  );

  assert.equal(projection.entries.length, 1);
  assert.equal(projection.entries[0]?.kind, "message");
  if (projection.entries[0]?.kind === "message") {
    assert.deepEqual(projection.entries[0].display, {
      kind: "skill_invocation",
      skill_name: "test",
      supplemental_text: "please verify",
    });
  }
});

test("projects durable AskUserQuestion reply targets", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", { source: "claude", turn_id: "turn-active" }),
      ev("2", "tool.approval_requested", {
        actor: "tool",
        source: "claude",
        turn_id: "turn-active",
        timeline_id: "turn-active:item:toolu_ask",
        provider_item_id: "toolu_ask",
        payload: {
          kind: "needs_input",
          name: "AskUserQuestion",
          input: { question: "Proceed?" },
        },
      }),
    ]),
  );

  assert.equal(projection.entries.length, 1);
  assert.equal(projection.entries[0]?.kind, "tool");
  if (projection.entries[0]?.kind === "tool") {
    assert.equal(projection.entries[0].turnId, "turn-active");
    assert.equal(projection.entries[0].providerItemId, "toolu_ask");
    assert.equal(projection.entries[0].id, "turn-active:item:toolu_ask");
  }
});

test("surfaces durable AskUserQuestion answers on the projected tool entry", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", { source: "claude", turn_id: "turn-active" }),
      ev("2", "tool.approval_requested", {
        actor: "tool",
        source: "claude",
        turn_id: "turn-active",
        timeline_id: "turn-active:item:toolu_ask",
        provider_item_id: "toolu_ask",
        payload: {
          kind: "needs_input",
          name: "AskUserQuestion",
          input: {
            questions: [
              {
                question: "Which features do you want to enable?",
                header: "Features",
                multiSelect: true,
                options: [
                  { label: "Search", description: "Full-text" },
                  { label: "Tags", description: "Faceted nav" },
                  { label: "Notes", description: "Inline notes" },
                ],
              },
            ],
          },
        },
      }),
      ev("3", "tool.approval_resolved", {
        actor: "tool",
        source: "claude",
        turn_id: "turn-active",
        timeline_id: "turn-active:item:toolu_ask",
        provider_item_id: "toolu_ask",
        payload: {
          kind: "needs_input",
          resolved: true,
          is_error: false,
          answers: {
            "Which features do you want to enable?": ["Search", "Tags"],
          },
          annotations: {
            "Which features do you want to enable?": {
              notes: "Drop notes for now, we'll revisit",
            },
          },
        },
      }),
    ]),
  );

  assert.equal(projection.entries.length, 1);
  assert.equal(projection.entries[0]?.kind, "tool");
  if (projection.entries[0]?.kind === "tool") {
    assert.deepEqual(projection.entries[0].askUserAnswers, {
      "Which features do you want to enable?": {
        labels: ["Search", "Tags"],
        notes: "Drop notes for now, we'll revisit",
      },
    });
  }
});
