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
    assert.equal(meta.meta.detail, "Terminating the active turn.");
    assert.equal(meta.meta.severity, "info");
    assert.equal(meta.turnId, "turn-1");
    assert.equal(meta.orderKey, "0004");
  }
});

test("session.status projects as a system transcript message", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
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
    ]),
  );

  assert.deepEqual(projection.entries.map((entry) => entry.kind), ["message", "message"]);
  assert.deepEqual(
    projection.entries.map((entry) => entry.kind === "message" ? entry.role : ""),
    ["system", "system"],
  );
  assert.deepEqual(
    projection.entries.map((entry) => entry.kind === "message" ? entry.text : ""),
    ["Session is loading.", "Session is ready."],
  );
});

test("background shell task projects as its own transcript artifact", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "item.started", {
        actor: "tool",
        source: "claude",
        timeline_id: "tool-before",
        payload: { kind: "tool", name: "Read" },
      }),
      ev("2", "shell_task.started", {
        actor: "tool",
        source: "claude",
        timeline_id: "turn-1:shell_task:task-abc",
        task_id: "task-abc",
        provider_item_id: "toolu-monitor",
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
      ev("3", "item.started", {
        actor: "tool",
        source: "claude",
        timeline_id: "tool-after",
        payload: { kind: "tool", name: "Grep" },
      }),
    ]),
  );

  assert.deepEqual(
    projection.entries.map((entry) => entry.kind),
    ["tool", "background_task", "tool"],
  );
  const task = projection.entries.find((entry) => entry.kind === "background_task");
  assert.ok(task);
  if (task?.kind === "background_task") {
    assert.equal(task.taskId, "task-abc");
    assert.equal(task.taskStatus, "running");
    assert.equal(task.taskSummary, "Watching logs");
    assert.equal(task.taskToolUseId, "toolu-monitor");
    assert.equal(task.taskCommand, "tail -f app.log");
    assert.equal(task.taskCwd, "/workspace/app");
    assert.equal(task.taskProcessId, "proc-abc");
    assert.equal(task.taskOutput, "booting\n");
  }
  assert.equal(projection.backgroundTasks.length, 1);
});

test("background shell task projection hides the matching foreground command item", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "item.started", {
        actor: "tool",
        source: "codex",
        timeline_id: "turn-1:item:item-unified-exec",
        provider_item_id: "item-unified-exec",
        payload: {
          kind: "command_execution",
          title: "npm run dev",
          command: "npm run dev",
        },
      }),
      ev("2", "shell_task.started", {
        actor: "tool",
        source: "codex",
        timeline_id: "turn-1:shell_task:proc-123",
        task_id: "proc-123",
        provider_item_id: "item-unified-exec",
        payload: {
          kind: "shell_task",
          task_id: "proc-123",
          status: "running",
          command: "npm run dev",
          process_id: "proc-123",
          output: "Listening on 5173",
        },
      }),
    ]),
  );

  assert.deepEqual(
    projection.entries.map((entry) => entry.kind),
    ["background_task"],
  );
  assert.equal(projection.activeToolName, null);
  const task = projection.entries[0];
  assert.equal(task?.kind, "background_task");
  if (task?.kind === "background_task") {
    assert.equal(task.taskCommand, "npm run dev");
    assert.equal(task.taskProcessId, "proc-123");
    assert.equal(task.taskOutput, "Listening on 5173");
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

test("projects turn terminal metadata onto completed turn entries", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-terminal",
        payload: { text: "hello" },
      }),
      ev("2", "turn.started", { source: "claude" }),
      ev("3", "item.completed", {
        actor: "tool",
        source: "claude",
        timeline_id: "tool-1",
        payload: { kind: "tool", title: "Read", output: "done" },
      }),
      ev("4", "item.completed", {
        actor: "assistant",
        source: "claude",
        timeline_id: "assistant-1",
        payload: { kind: "message", text: "world" },
      }),
      ev("5", "turn.completed", {
        source: "claude",
        created_at: "2026-05-12T00:00:05.000Z",
      }),
    ]),
  );

  for (const entry of projection.entries) {
    assert.equal(entry.turnTerminalStatus, "completed");
    assert.equal(entry.turnTerminalAt, "2026-05-12T00:00:05.000Z");
    assert.equal(entry.turnTerminalEventId, "5");
  }
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

test("projects completed result_failed items as failed tools", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", { source: "codex" }),
      ev("2", "item.completed", {
        actor: "tool",
        source: "codex",
        timeline_id: "tool-test",
        payload: {
          kind: "command_execution",
          title: "npm test",
          output: "1 failed",
          outcome: { kind: "result_failed", reason: "exit_code", code: 1 },
        },
      }),
    ]),
  );

  assert.equal(projection.entries[0]?.kind, "tool");
  if (projection.entries[0]?.kind === "tool") {
    assert.equal(projection.entries[0].toolStatus, "failed");
    assert.equal(projection.entries[0].toolOutput, "1 failed");
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
