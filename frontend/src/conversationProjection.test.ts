import { test, expect } from "vitest";

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

test("origin session avatar id projects onto user message entry", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "handoff-1",
        payload: { text: "fix the avatar bug" },
        origin_session_id: "42",
        origin_session_avatar_id: "jp1-grant",
      } as Partial<TankConversationEvent>),
    ]),
  );

  const entry = projection.entries[0];
  expect(entry?.kind).toBe("message");
  if (entry?.kind === "message") {
    expect(entry.originSessionId).toBe("42");
    expect(entry.originSessionAvatarId).toBe("jp1-grant");
  }
});

test("test_provision thread renders as visible system messages (info phases survive the fold filter)", () => {
  const provision = (phase: string, payload: Record<string, unknown>, order: string) =>
    ev(`test-provision:run-1:${phase}`, "test_provision.updated", {
      actor: "system",
      source: "tank",
      timeline_id: `test-provision:run-1:${phase}`,
      client_nonce: "test-provision-run-1",
      order_key: order,
      payload: { kind: "test_provision", run_id: "run-1", phase, ...payload },
    });

  const projection = projectConversationState(
    reduceConversationEvents([
      provision("creating", { text: "Creating test slot." }, "0001"),
      provision("validating", { text: "Validating PR readiness…" }, "0002"),
      provision(
        "ready",
        { text: "Test environment ready at https://slot-1.example/", url: "https://slot-1.example/" },
        "0003",
      ),
    ]),
  );

  const systemMessages = projection.entries.filter(
    (entry) => entry.kind === "message" && entry.role === "system",
  );
  // All three phases render — the info-severity creating/validating records are
  // NOT dropped as session-startup noise.
  expect(systemMessages.map((m) => (m.kind === "message" ? m.text : ""))).toEqual([
    "Creating test slot.",
    "Validating PR readiness…",
    "Test environment ready at https://slot-1.example/",
  ]);
  const ready = systemMessages[2];
  if (ready.kind === "message") {
    expect(ready.action).toEqual({
      label: "Open test environment",
      href: "https://slot-1.example/",
    });
  }
});

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

  expect(projection.runStatus).toBe("stopping");
  expect(projection.stopping).toBe(true);
  const meta = projection.entries.find((entry) => entry.kind === "meta");
  expect(meta, "Stop requested chip should appear in projection entries").toBeTruthy();
  if (meta?.kind === "meta") {
    expect(meta.meta.title).toBe("Stop requested");
    expect(meta.meta.detail).toBe("Terminating the active turn.");
    expect(meta.meta.severity).toBe("info");
    expect(meta.turnId).toBe("turn-1");
    expect(meta.orderKey).toBe("0004");
  }
});

test("turn.failed projects as an error meta line at its order_key", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "hi" },
      }),
      ev("2", "turn.submitted", { client_nonce: "run-1" }),
      ev("3", "turn.started", { source: "codex" }),
      ev("4", "turn.failed", {
        source: "codex",
        payload: { reason: "provider_failure", error: "rate limit exceeded" },
      }),
    ]),
  );

  expect(projection.runStatus).toBe("error");
  expect(projection.failed).toBe(true);
  const metas = projection.entries.filter((entry) => entry.kind === "meta");
  expect(metas.length, "turn.failed should produce one meta entry").toBe(1);
  const meta = metas[0];
  if (meta.kind === "meta") {
    expect(meta.meta.title).toBe("Turn failed");
    expect(meta.meta.detail).toBe("rate limit exceeded");
    expect(meta.meta.severity).toBe("error");
    expect(meta.turnId).toBe("turn-1");
    expect(meta.orderKey).toBe("0004");
  }
});

test("turn.failed with structured-error object unwraps .message into the meta detail", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "hi" },
      }),
      ev("2", "turn.failed", {
        source: "codex",
        payload: {
          reason: "provider_failure",
          error: { code: "token_expired", message: "auth token expired" },
        },
      }),
    ]),
  );

  const meta = projection.entries.find((entry) => entry.kind === "meta");
  expect(meta, "turn.failed should produce a meta entry").toBeTruthy();
  if (meta?.kind === "meta") {
    expect(meta.meta.detail).toBe("auth token expired");
  }
});

test("turn.interrupted projects as an info 'Stopped' meta line; not 'failed'", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "long task" },
      }),
      ev("2", "turn.submitted", { client_nonce: "run-1" }),
      ev("3", "turn.started", { source: "codex" }),
      ev("4", "turn.interrupted", {
        source: "codex",
        payload: { reason: "client_interrupt" },
      }),
    ]),
  );

  expect(projection.failed).toBe(false);
  const metas = projection.entries.filter((entry) => entry.kind === "meta");
  expect(metas.length).toBe(1);
  const meta = metas[0];
  if (meta.kind === "meta") {
    expect(meta.meta.title).toBe("Stopped");
    expect(meta.meta.severity).toBe("info");
  }
});

test("turn.completed produces no meta entry — success speaks through the bubble", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "hi" },
      }),
      ev("2", "turn.submitted", { client_nonce: "run-1" }),
      ev("3", "turn.started", { source: "codex" }),
      ev("4", "turn.completed", { source: "codex" }),
    ]),
  );

  const metas = projection.entries.filter((entry) => entry.kind === "meta");
  expect(metas.length).toBe(0);
});

test("turn.usage remains backend plumbing and does not project a visible row", () => {
  const firstUsage = { input_tokens: 100, output_tokens: 25, total_tokens: 125 };
  const latestUsage = { input_tokens: 120, output_tokens: 30, total_tokens: 150 };
  const usageObservation = {
    usage_source: "thread.tokenUsage.updated",
    provider_turn_id: "provider-turn-1",
    update_count: 2,
  };
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "think" },
      }),
      ev("2", "turn.submitted", { client_nonce: "run-1" }),
      ev("3", "turn.started", { source: "codex" }),
      ev("4", "turn.usage", {
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
      ev("5", "item.started", {
        actor: "tool",
        source: "codex",
        timeline_id: "turn-1:item:tool",
        payload: {
          kind: "command_execution",
          command: "go test ./...",
        },
      }),
      ev("6", "turn.usage", {
        source: "codex",
        payload: {
          usage: latestUsage,
          usage_observation: usageObservation,
        },
      }),
    ]),
  );

  expect(projection.entries.some((entry) => entry.kind === "meta")).toBe(false);
  expect(projection.entries.some((entry) => "turnUsage" in entry)).toBe(false);
  expect(projection.entries.some((entry) => "usageObservation" in entry)).toBe(false);
  const toolIndex = projection.entries.findIndex((entry) => entry.kind === "tool");
  expect(toolIndex >= 0, "non-usage turn activity should still project").toBeTruthy();
});

test("terminal usage does not annotate projected transcript rows", () => {
  const midUsage = { input_tokens: 100, output_tokens: 25, total_tokens: 125 };
  const terminalUsage = { input_tokens: 120, output_tokens: 30, total_tokens: 150 };
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        client_nonce: "run-1",
        payload: { text: "think" },
      }),
      ev("2", "turn.started", { source: "codex" }),
      ev("3", "turn.usage", {
        source: "codex",
        payload: { usage: midUsage },
      }),
      ev("4", "turn.completed", {
        source: "codex",
        payload: { usage: terminalUsage },
      }),
    ]),
  );

  expect(projection.entries.some((entry) => entry.kind === "meta")).toBe(false);
  const user = projection.entries.find((entry) => entry.kind === "message" && entry.role === "user");
  expect(user).toBeTruthy();
  expect(user && "turnUsage" in user).toBe(false);
  expect(user && "usageObservation" in user).toBe(false);
});

test("session.status:failed with provider extension carries severity + action onto the system message", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("session:63:provider:codex:status", "session.status", {
        actor: "system",
        timeline_id: "session:63:provider:codex:status",
        created_at: "2026-05-24T18:48:30.000Z",
        payload: {
          status: "failed",
          text: "Codex sign-in expired. Re-authenticate to continue.",
          failure_scope: "provider",
          failure_subject: "codex",
          failure_reason: "refresh_token_reused",
          action: {
            label: "Re-sign-in to Codex",
            href: "https://auth.romaine.life/codex",
          },
        },
      }),
    ]),
  );
  const msg = projection.entries.find((entry) => entry.kind === "message");
  expect(msg, "session.status:failed should produce a message entry").toBeTruthy();
  if (msg?.kind === "message") {
    expect(msg.role).toBe("system");
    expect(msg.severity).toBe("error");
    expect(msg.action).toEqual({
            label: "Re-sign-in to Codex",
            href: "https://auth.romaine.life/codex",
          });
  }
});

test("session.status:ready replaces a prior failed banner with the same timeline_id", () => {
  // Recovery contract: when a provider's auth comes back online, the
  // poller writes a session.status event with status="ready" on the
  // SAME timeline_id as the prior failed banner. The reducer must
  // replace the failed entry rather than appending a second message
  // — otherwise scrollback shows the stale error indefinitely.
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("session:63:provider:codex:status", "session.status", {
        actor: "system",
        timeline_id: "session:63:provider:codex:status",
        created_at: "2026-05-24T18:48:30.000Z",
        payload: {
          status: "failed",
          text: "Codex sign-in expired.",
          failure_scope: "provider",
          failure_subject: "codex",
          failure_reason: "refresh_token_reused",
        },
      }),
      ev("session:63:provider:codex:status:ready", "session.status", {
        actor: "system",
        timeline_id: "session:63:provider:codex:status",
        created_at: "2026-05-24T19:10:00.000Z",
        payload: {
          status: "ready",
          text: "Codex sign-in is back online.",
        },
      }),
    ]),
  );
  const messages = projection.entries.filter(
    (entry) => entry.kind === "message" && entry.role === "system",
  );
  expect(messages.length, "recovery must replace, not append").toBe(1);
  const msg = messages[0];
  if (msg.kind === "message") {
    expect(msg.text).toBe("Codex sign-in is back online.");
    expect(msg.severity).toBe("info");
    expect(msg.action).toBe(undefined);
  }
});

test("session-startup notices are turn noise, not main-transcript messages", () => {
  // The authoritative server projection folds Session is loading./ready. into
  // the owning turn's Turn activity (transcript_projection.go applySessionStatus;
  // docs/features/transcript/contract.md). The client mirror agrees: a plain
  // startup notice does not project as a main-transcript system message.
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

  expect(projection.entries.filter((entry) => entry.kind === "message" && entry.role === "system"), "startup notices must not appear as main-transcript system messages").toEqual([]);
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

  expect(projection.entries.map((entry) => entry.kind)).toEqual(["tool", "background_task", "tool"]);
  const task = projection.entries.find((entry) => entry.kind === "background_task");
  expect(task).toBeTruthy();
  if (task?.kind === "background_task") {
    expect(task.taskId).toBe("task-abc");
    expect(task.taskStatus).toBe("running");
    expect(task.taskSummary).toBe("Watching logs");
    expect(task.taskToolUseId).toBe("toolu-monitor");
    expect(task.taskCommand).toBe("tail -f app.log");
    expect(task.taskCwd).toBe("/workspace/app");
    expect(task.taskProcessId).toBe("proc-abc");
    expect(task.taskOutput).toBe("booting\n");
  }
  expect(projection.backgroundTasks.length).toBe(1);
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

  expect(projection.entries.map((entry) => entry.kind)).toEqual(["background_task"]);
  expect(projection.activeToolName).toBe(null);
  const task = projection.entries[0];
  expect(task?.kind).toBe("background_task");
  if (task?.kind === "background_task") {
    expect(task.taskCommand).toBe("npm run dev");
    expect(task.taskProcessId).toBe("proc-123");
    expect(task.taskOutput).toBe("Listening on 5173");
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

  expect(projection.entries.map((entry) =>
          entry.kind === "message" ? [entry.role, entry.text] : [entry.kind],
        )).toEqual([
          ["user", "hello"],
          ["assistant", "world"],
        ]);
  expect(projection.runStatus).toBe("ready");
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
    expect(entry.turnTerminalStatus).toBe("completed");
    expect(entry.turnTerminalAt).toBe("2026-05-12T00:00:05.000Z");
    expect(entry.turnTerminalEventId).toBe("5");
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

  expect(projection.entries.map((entry) =>
          entry.kind === "message" ? [entry.role, entry.text] : [entry.kind],
        )).toEqual([
          ["user", "first prompt"],
          ["assistant", "first answer"],
          ["user", "second prompt"],
          ["assistant", "second answer"],
        ]);
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

  expect(running.activeToolName).toBe("Read");
  expect(running.entries[0]?.kind).toBe("tool");
  if (running.entries[0]?.kind === "tool") {
    expect(running.entries[0].toolStatus).toBe("started");
    expect(running.entries[0].toolInput ?? "").toMatch(/README\.md/);
    expect(running.entries[0].startedAt).toBe("2026-05-12T00:00:10.000Z");
    expect(running.entries[0].completedAt).toBe(undefined);
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

  expect(completed.activeToolName).toBe(null);
  expect(completed.entries[0]?.kind).toBe("tool");
  if (completed.entries[0]?.kind === "tool") {
    expect(completed.entries[0].toolName).toBe("Read");
    expect(completed.entries[0].toolStatus).toBe("completed");
    expect(completed.entries[0].toolOutput).toBe("README contents");
    expect(completed.entries[0].time).toBe("2026-05-12T00:00:10.000Z");
    expect(completed.entries[0].startedAt).toBe("2026-05-12T00:00:10.000Z");
    expect(completed.entries[0].completedAt).toBe("2026-05-12T00:00:15.000Z");
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

  expect(projection.entries[0]?.kind).toBe("tool");
  if (projection.entries[0]?.kind === "tool") {
    expect(projection.entries[0].toolStatus).toBe("failed");
    expect(projection.entries[0].toolOutput).toBe("1 failed");
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

  expect(projection.runStatus).toBe("streaming");
  expect(projection.activeTurnId).toBe("turn-resumed");
  expect(projection.activeClientNonce).toBe("client-resumed-1");
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

  // Two entries: the user message + the "Stopped" transcript meta line
  // derived from turn.interrupted. Duplicate delivery must not double either.
  expect(projection.entries.length).toBe(2);
  const messageEntries = projection.entries.filter((e) => e.kind === "message");
  const metaEntries = projection.entries.filter((e) => e.kind === "meta");
  expect(messageEntries.length).toBe(1);
  expect(metaEntries.length).toBe(1);
  expect(projection.stopped).toBe(true);
  expect(projection.failed).toBe(false);
});

test("projects author_kind onto the user message entry", () => {
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "user_message.created", {
        actor: "user",
        timeline_id: "turn-1:user",
        client_nonce: "bot-1",
        payload: { text: "posted via bot token" },
        author_kind: "system",
      } as Partial<TankConversationEvent>),
    ]),
  );

  expect(projection.entries.length).toBe(1);
  expect(projection.entries[0]?.kind).toBe("message");
  if (projection.entries[0]?.kind === "message") {
    expect(projection.entries[0].authorKind).toBe("system");
  }
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

  expect(projection.entries.length).toBe(1);
  expect(projection.entries[0]?.kind).toBe("message");
  if (projection.entries[0]?.kind === "message") {
    expect(projection.entries[0].display).toEqual({
            kind: "skill_invocation",
            skill_name: "test",
            supplemental_text: "please verify",
          });
  }
});

test("turn.awaiting_input surfaces the live needs-input signal", () => {
  // The durable awaiting-input card is server-projected (App.tsx renders it
  // from entry.awaitingInput); the live browser projection only carries the
  // session-level needs-input signal — there is no tool item or in-turn
  // approval row anymore.
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.started", { source: "claude", turn_id: "turn-active" }),
      ev("2", "turn.awaiting_input", {
        source: "claude",
        turn_id: "turn-active",
        payload: {
          questions: [{ question: "Proceed?" }],
          provider_item_id: "toolu_ask",
          timeline_id: "turn-active:item:toolu_ask",
        },
      }),
    ]),
  );

  expect(projection.needsInput).toBe(true);
  expect(projection.runStatus).toBe("needs_input");
});

test("turn.input_answered clears needs-input without adding a transcript message", () => {
  // turn.input_answered marks the question card answered. The visible answer
  // bubble is the separate user_message.created written by POST /answer.
  const projection = projectConversationState(
    reduceConversationEvents([
      ev("1", "turn.awaiting_input", {
        source: "claude",
        turn_id: "turn-1",
        payload: {
          questions: [{ question: "Pick one" }],
          provider_item_id: "toolu_ask",
          timeline_id: "turn-1:item:toolu_ask",
        },
      }),
      ev("2", "turn.input_answered", {
        actor: "user",
        source: "tank",
        turn_id: "turn-1",
        timeline_id: "turn-1:item:toolu_ask:answer",
        client_nonce: "answer-123",
        payload: {
          question_timeline_id: "turn-1:item:toolu_ask",
          provider_item_id: "toolu_ask",
          answers: { "Pick one": ["Search"] },
        },
      }),
    ]),
  );

  expect(projection.needsInput).toBe(false);
  expect(projection.entries.some((entry) => entry.kind === "message" && entry.role === "user")).toBe(false);
});

// The interrupted-while-awaiting-input case is owned by the server projection:
// turn.awaiting_input keeps the turn active, so Stop can still interrupt it.
