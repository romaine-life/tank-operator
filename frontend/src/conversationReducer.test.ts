import { test, expect } from "vitest";

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
  expect(isTankConversationEvent(event)).toBe(true);
});

test("turn.claimed is a valid Tank envelope and keeps the turn submitted", () => {
  const event = ev("2", "turn.claimed", { source: "claude", client_nonce: "client-1" });
  expect(isTankConversationEvent(event)).toBe(true);
  const state = reduceConversationEvents([
    ev("1", "turn.submitted"),
    event,
  ]);
  expect(state.runStatus).toBe("submitted");
  expect(state.activeTurnId).toBe("turn-1");
});

test("late turn.started after terminal does not reactivate a stopped turn", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.submitted"),
    ev("2", "turn.interrupted", { source: "claude", payload: { reason: "client_interrupt" } }),
    ev("3", "turn.started", { source: "claude" }),
  ]);
  expect(state.runStatus).toBe("stopped");
  expect(state.activeTurnId).toBe(null);
});

test("context.compacted is an informational no-op for run state", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.submitted"),
    ev("2", "turn.started"),
    ev("3", "context.compacted", { source: "claude", payload: { trigger: "auto", pre_tokens: 158000 } }),
  ]);
  expect(state.runStatus).toBe("streaming");
  expect(state.activeTurnId).toBe("turn-1");
  expect(state.needsInput).toBe(false);
  expect(state.failed).toBe(false);
  expect(state.seenEventIds.includes("3")).toBe(true);
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

  expect(state.runStatus).toBe("stopped");
  expect(state.failed).toBe(false);
  expect(state.messages.length).toBe(1);
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

  expect(state.messages.map((message) => message.role)).toEqual(["system", "system"]);
  expect(state.messages.map((message) => message.text)).toEqual([
        "Session is loading.",
        "Session is ready.",
      ]);
  expect(state.runStatus).toBe("ready");
});

function provisionEv(
  phase: string,
  fields: Partial<TankConversationEvent> = {},
): TankConversationEvent {
  return ev(`test-provision:run-1:${phase}`, "test_provision.updated", {
    actor: "system",
    source: "tank",
    timeline_id: `test-provision:run-1:${phase}`,
    client_nonce: "test-provision-run-1",
    payload: { kind: "test_provision", run_id: "run-1", phase, text: phase },
    ...fields,
  });
}

test("test_provision.updated records are valid Tank envelopes", () => {
  expect(isTankConversationEvent(provisionEv("creating"))).toBe(true);
  // Bogus phase is rejected.
  expect(
    isTankConversationEvent(
      provisionEv("creating", {
        payload: { kind: "test_provision", run_id: "run-1", phase: "bogus", text: "x" },
      }),
    ),
  ).toBe(false);
});

test("test_provision thread replays as a grouped role:system thread", () => {
  const state = reduceConversationEvents([
    provisionEv("creating", {
      created_at: "2026-06-18T10:00:00.000Z",
      payload: { kind: "test_provision", run_id: "run-1", phase: "creating", text: "Creating test slot." },
    }),
    provisionEv("validating", {
      created_at: "2026-06-18T10:00:01.000Z",
      payload: { kind: "test_provision", run_id: "run-1", phase: "validating", text: "Validating PR readiness…" },
    }),
    provisionEv("ready", {
      created_at: "2026-06-18T10:00:30.000Z",
      payload: {
        kind: "test_provision",
        run_id: "run-1",
        phase: "ready",
        severity: "info",
        text: "Test environment ready at https://slot-1.example/",
        url: "https://slot-1.example/",
      },
    }),
  ]);

  expect(state.messages.map((m) => m.role)).toEqual(["system", "system", "system"]);
  expect(state.messages.map((m) => m.id)).toEqual([
    "test-provision:run-1:creating",
    "test-provision:run-1:validating",
    "test-provision:run-1:ready",
  ]);
  expect(state.messages[2].action).toEqual({
    label: "Open test environment",
    href: "https://slot-1.example/",
  });
});

test("test_provision error phase carries error severity", () => {
  const state = reduceConversationEvents([
    provisionEv("error", {
      payload: {
        kind: "test_provision",
        run_id: "run-1",
        phase: "error",
        severity: "error",
        text: "Couldn't create test slot: CI failed.",
      },
    }),
  ]);
  expect(state.messages).toHaveLength(1);
  expect(state.messages[0].severity).toBe("error");
});

function prReadyEv(
  fields: Partial<TankConversationEvent> = {},
): TankConversationEvent {
  return ev("pr-ready:romaine-life/tank-operator:12", "pr_ready.notified", {
    actor: "system",
    source: "tank",
    timeline_id: "pr-ready:romaine-life/tank-operator:12",
    client_nonce: "pr-ready-pr-ready:romaine-life/tank-operator:12",
    payload: {
      kind: "pr_ready",
      repo: "romaine-life/tank-operator",
      pr_number: 12,
      pr_url: "https://github.com/romaine-life/tank-operator/pull/12",
      head_sha: "abc1234",
      text: "✅ Your governed PR romaine-life/tank-operator #12 is green and mergeable — ready to merge.",
    },
    ...fields,
  });
}

test("pr_ready.notified records are valid Tank envelopes", () => {
  expect(isTankConversationEvent(prReadyEv())).toBe(true);
  // Missing pr_url is rejected.
  expect(
    isTankConversationEvent(
      prReadyEv({
        payload: { kind: "pr_ready", text: "ready" },
      }),
    ),
  ).toBe(false);
});

test("pr_ready.notified renders a system notice and summons from idle", () => {
  const state = reduceConversationEvents([prReadyEv()]);
  expect(state.messages).toHaveLength(1);
  expect(state.messages[0].role).toBe("system");
  expect(state.messages[0].action).toEqual({
    label: "View PR",
    href: "https://github.com/romaine-life/tank-operator/pull/12",
  });
  // The needs_input attention is reused as the "your turn" UI dressing.
  expect(state.runStatus).toBe("needs_input");
  expect(state.needsInput).toBe(true);
});

test("pr_ready.notified does not clobber an in-flight agent turn", () => {
  // CI goes green while the agent is mid-turn: the ping renders as a notice but
  // must not downgrade the live streaming run status.
  const state = reduceConversationEvents([
    ev("1", "turn.submitted", { client_nonce: "run-live" }),
    ev("2", "turn.started", { source: "claude" }),
    prReadyEv(),
  ]);
  expect(state.runStatus).toBe("streaming");
  expect(state.needsInput).toBe(false);
  expect(state.messages.some((m) => m.role === "system")).toBe(true);
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

  expect(state.runStatus).toBe("ready");
  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.text).toBe("summarize");
  expect(state.items.length).toBe(1);
  expect(state.items[0]?.actor).toBe("assistant");
  expect(state.items[0]?.text).toBe("summary");
  expect(state.activeTurnId).toBe(null);
  expect(state.turnTerminals["turn-1"]?.status).toBe("completed");
  expect(state.turnTerminals["turn-1"]?.sourceEventId).toBe("5");
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

  expect(state.runStatus).toBe("streaming");
  expect(state.activeTurnId).toBe("turn-1");
  expect(state.lastUsage).toEqual(latestUsage);
  expect(state.turnUsages["turn-1"]?.orderKey).toBe("0003");
  expect(state.turnUsages["turn-1"]?.endOrderKey).toBe("0004");
  expect(state.turnUsages["turn-1"]?.usage).toEqual(latestUsage);
  expect(state.turnUsages["turn-1"]?.usageObservation).toEqual(usageObservation);
});

test("origin session fields on user message flow onto ConversationMessage", () => {
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
      origin_session_avatar_id: "jp1-grant",
    } as Partial<TankConversationEvent>),
  ]);

  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.originSessionId).toBe("42");
  expect(state.messages[0]?.originSessionAvatarId).toBe("jp1-grant");
});

test("user message without origin_session_id leaves originSessionId undefined", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "human-1",
      payload: { text: "I typed this myself" },
    }),
  ]);

  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.originSessionId).toBe(undefined);
  expect(state.messages[0]?.originSessionAvatarId).toBe(undefined);
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

  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.authorKind).toBe("system");
});

test("user message without author_kind leaves authorKind undefined", () => {
  const state = reduceConversationEvents([
    ev("1", "user_message.created", {
      actor: "user",
      client_nonce: "human-2",
      payload: { text: "I typed this myself" },
    }),
  ]);

  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.authorKind).toBe(undefined);
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

  expect(state.runStatus).toBe("ready");
  expect(state.activeItemId).toBe(null);
  expect(state.items.map((item) => [item.id, item.status, item.title])).toEqual([
        ["toolu-read", "completed", "Read"],
      ]);
  expect(state.items[0]?.startedAt).toBe("2026-05-12T00:00:10.000Z");
  expect(state.items[0]?.completedAt).toBe("2026-05-12T00:00:15.000Z");
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

  expect(state.runStatus).toBe("ready");
  expect(state.items.length).toBe(0);
  expect(state.backgroundTasks.length).toBe(1);
  expect(state.backgroundTasks[0]?.status).toBe("completed");
  expect(state.backgroundTasks[0]?.taskId).toBe("task-abc");
  expect(state.backgroundTasks[0]?.toolUseId).toBe("toolu-monitor");
  expect(state.backgroundTasks[0]?.summary).toBe("Log watch finished");
  expect(state.backgroundTasks[0]?.command).toBe("tail -f app.log");
  expect(state.backgroundTasks[0]?.cwd).toBe("/workspace/app");
  expect(state.backgroundTasks[0]?.processId).toBe("proc-abc");
  expect(state.backgroundTasks[0]?.output).toBe("booting\nready\n");
  expect(state.backgroundTasks[0]?.exitCode).toBe(0);
  expect(state.backgroundTasks[0]?.durationMs).toBe(10_000);
  expect(state.backgroundTasks[0]?.startedAt).toBe("2026-05-12T00:00:10.000Z");
  expect(state.backgroundTasks[0]?.completedAt).toBe("2026-05-12T00:00:20.000Z");
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

  expect(state.messages.length).toBe(1);
  expect(state.items.map((item) => [item.id, item.kind, item.text])).toEqual([["turn-1:item:item-agent-message", "agent_message", "hi"]]);
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

  expect(state.items.length).toBe(1);
  expect(state.items[0]?.status).toBe("completed");
  expect(state.activeItemId).toBe(null);
  expect(state.items[0]?.payload?.raw_item).toEqual({
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

  expect(state.items.length).toBe(1);
  expect(state.items[0]?.status).toBe("failed");
  expect(state.activeItemId).toBe(null);
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

  expect(state.messages.length).toBe(1);
  expect(state.messages[0]?.text).toBe("same prompt");
});

test("turn.awaiting_input hands off; answer submission opens a continuation turn", () => {
  // AskUserQuestion is the Tank-visible response for the asking turn. The
  // durable answer event clears needs_input, then the answer's normal
  // turn.submitted event owns the continuation turn.
  const paused = reduceConversationEvents([
    ev("1", "turn.started", { turn_id: "turn-ask" }),
    ev("2", "turn.awaiting_input", {
      turn_id: "turn-ask",
      source: "claude",
      payload: {
        questions: [{ question: "Proceed?" }],
        provider_item_id: "toolu_ask",
        timeline_id: "turn-ask:item:toolu_ask",
      },
    }),
  ]);
  expect(paused.needsInput).toBe(true);
  expect(paused.runStatus).toBe("needs_input");
  expect(paused.activeTurnId).toBe("turn-ask");

  const resumed = reduceConversationEvents([
    ev("1", "turn.started", { turn_id: "turn-ask" }),
    ev("2", "turn.awaiting_input", {
      turn_id: "turn-ask",
      source: "claude",
      payload: {
        questions: [{ question: "Proceed?" }],
        provider_item_id: "toolu_ask",
        timeline_id: "turn-ask:item:toolu_ask",
      },
    }),
    ev("3", "turn.input_answered", {
      turn_id: "turn-ask",
      source: "tank",
      payload: {
        question_timeline_id: "turn-ask:item:toolu_ask",
        provider_item_id: "toolu_ask",
        answers: { "Proceed?": ["Yes"] },
      },
    }),
    ev("4", "user_message.created", {
      turn_id: "turn-answer",
      client_nonce: "answer-1",
      payload: { text: "1. Proceed?\nAnswer: Yes", display: { kind: "plain" } },
    }),
    ev("5", "turn.submitted", {
      turn_id: "turn-answer",
      client_nonce: "answer-1",
    }),
  ]);
  expect(resumed.needsInput).toBe(false);
  expect(resumed.runStatus).toBe("submitted");
  expect(resumed.activeTurnId).toBe("turn-answer");
});

test("Provider error becomes terminal error state without needs-input", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("3", "turn.failed", {
      source: "claude",
      payload: { reason: "provider_failure", error: "quota exceeded" },
    }),
  ]);

  expect(state.runStatus).toBe("error");
  expect(state.failed).toBe(true);
  expect(state.needsInput).toBe(false);
  expect(state.activeTurnId).toBe(null);
  expect(state.turnTerminals["turn-1"]?.status).toBe("failed");
  expect(state.turnTerminals["turn-1"]?.sourceEventId).toBe("3");
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

  expect(state.runStatus).toBe("stopping");
  expect(state.activeTurnId).toBe("turn-1");
  expect(state.interruptRequests.length).toBe(1);
  expect(state.interruptRequests[0]?.turnId).toBe("turn-1");
});

test("turn.interrupt_requested → turn.interrupted resolves to stopped", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("3", "turn.interrupted", { source: "claude", payload: { reason: "client_interrupt" } }),
  ]);

  expect(state.runStatus).toBe("stopped");
  expect(state.activeTurnId).toBe(null);
  expect(state.interruptRequests.length).toBe(1);
  expect(state.turnTerminals["turn-1"]?.status).toBe("interrupted");
  expect(state.turnTerminals["turn-1"]?.sourceEventId).toBe("3");
});

test("turn.interrupt_requested losing race to turn.completed resolves to ready", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("3", "turn.completed", { source: "claude" }),
  ]);

  expect(state.runStatus).toBe("ready");
  expect(state.activeTurnId).toBe(null);
  // Chip stays as transcript evidence even though stop "lost the race."
  expect(state.interruptRequests.length).toBe(1);
  expect(state.turnTerminals["turn-1"]?.status).toBe("completed");
  expect(state.turnTerminals["turn-1"]?.sourceEventId).toBe("3");
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

  expect(state.runStatus).toBe("error");
  expect(state.failed).toBe(true);
  expect(state.interruptRequests.length).toBe(1);
  expect(state.turnTerminals["turn-1"]?.status).toBe("failed");
  expect(state.turnTerminals["turn-1"]?.sourceEventId).toBe("3");
});

test("Late turn.interrupt_requested after terminal state does NOT downgrade run status", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("2", "turn.completed", { source: "claude" }),
    ev("3", "turn.interrupt_requested", { actor: "system", source: "tank" }),
  ]);

  // Stop request lands after the turn already cleanly finished. Chip is
  // recorded for transcript transparency, but runStatus stays ready.
  expect(state.runStatus).toBe("ready");
  expect(state.interruptRequests.length).toBe(1);
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

  expect(state.runStatus).toBe("streaming");
  expect(state.failed).toBe(false);
  expect(state.lastError).toBe(null);
  expect(state.activeTurnId).toBe("turn-1");
  // The per-item failure must still show in the transcript so the
  // user sees the orange error badge under the tool call.
  const failedItem = state.items.find((item) => item.id === "tool-1");
  expect(failedItem?.status).toBe("failed");
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

  expect(state.runStatus).toBe("streaming");
  expect(state.failed).toBe(false);
  expect(state.items.find((item) => item.id === "tool-warn")?.status).toBe("failed");
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

  expect(state.items.find((item) => item.id === "legacy-exit")?.status).toBe("failed");
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

  expect(state.runStatus).toBe("ready");
  expect(state.failed).toBe(false);
  expect(state.lastError).toBe(null);
});

test("Duplicate turn.interrupt_requested events dedupe by event_id", () => {
  const state = reduceConversationEvents([
    ev("1", "turn.started", { source: "claude" }),
    ev("dup", "turn.interrupt_requested", { actor: "system", source: "tank" }),
    ev("dup", "turn.interrupt_requested", { actor: "system", source: "tank" }),
  ]);

  expect(state.runStatus).toBe("stopping");
  expect(state.interruptRequests.length).toBe(1);
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

  expect(replayThenLive).toEqual(replayOnly);
  expect(replayOnly).not.toEqual(initialConversationState);
});

test("contract guard rejects malformed per-type events", () => {
  expect(isTankConversationEvent(ev("10", "user_message.created", {
        actor: "user",
        timeline_id: "turn-1:user",
        client_nonce: "client-1",
        payload: { text: "hello", display: { kind: "plain" } },
      }))).toBe(true);

  expect(isTankConversationEvent(ev("session:63:status:loading", "session.status", {
        actor: "system",
        timeline_id: "session:63:status:loading",
        payload: { status: "loading", text: "Session is loading." },
      }))).toBe(true);

  expect(isTankConversationEvent(ev("turn-1:usage:1", "turn.usage", {
        source: "codex",
        payload: { usage: { input_tokens: 1, output_tokens: 1 } },
      }))).toBe(true);

  expect(isTankConversationEvent({
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
      })).toBe(false);

  expect(isTankConversationEvent({
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
      })).toBe(false);

  expect(isTankConversationEvent({
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
      })).toBe(false);

  expect(isTankConversationEvent({
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
      })).toBe(false);
});
