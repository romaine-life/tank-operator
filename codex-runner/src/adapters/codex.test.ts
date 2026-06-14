import { test } from "node:test";
import assert from "node:assert/strict";

import type { Config } from "../config.js";
import type { CodexEvent } from "../sessionEvents.js";
import { isTankConversationEvent } from "../../../runner-shared/conversation.js";
import { stampTankEvent } from "../../../runner-shared/conversation-builders.js";
import {
  CodexTankEventAdapter,
  canonicalEventsForCodexEvent,
  type CodexAdapterTurn,
} from "./codex.js";

function cfg(): Config {
  return {
    sessionId: "63",
    sessionStorageKey: "63",
    ownerEmail: "user@example.com",
    natsURL: "nats://example.invalid:4222",
    natsToken: "",
    natsStream: "TANK_SESSION_BUS",
    natsCommandStream: "TANK_SESSION_COMMANDS",
    operatorInternalURL: "",
    operatorTokenPath: "",
    workspace: "/workspace",
  };
}

function acceptedTurn(fields: Partial<CodexAdapterTurn> = {}): CodexAdapterTurn {
  return {
    turnID: "turn-run-123",
    clientNonce: "run-123",
    turnSeq: 7,
    ...fields,
  };
}

function mappedEvent(adapter: CodexTankEventAdapter, event: CodexEvent) {
  const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), event);
  assert.equal(events.length, 1);
  // Adapter output is pre-stamp; the runner stamps order_key + sequence
  // before publishing. Validate the full envelope here so the test
  // mirrors what the persister receives.
  assert.equal(isTankConversationEvent(stampTankEvent(events[0]!)), true);
  return events[0]!;
}

test("emits no Tank event for Codex item.updated frames (live-only retired)", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
    type: "item.updated",
    item: {
      id: "item_reasoning_1",
      type: "reasoning",
      text: "think",
    },
  });
  assert.deepEqual(events, []);
});

test("suppresses Codex userMessage provider echoes from the durable Tank transcript", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  for (const event of [
    {
      type: "item.started",
      item: {
        id: "item_user_echo_started",
        type: "userMessage",
        text: "hello",
      },
    },
    {
      type: "item.updated",
      item: {
        id: "item_user_echo_updated",
        type: "userMessage",
        text: "hello again",
      },
    },
    {
      type: "item.completed",
      item: {
        id: "item_user_echo_completed",
        type: "userMessage",
        text: "hello",
      },
    },
    {
      type: "item.completed",
      item: {
        id: "item_user_echo_snake",
        type: "user_message",
        text: "hello",
      },
    },
  ] satisfies CodexEvent[]) {
    assert.deepEqual(
      adapter.canonicalEventsForCodexEvent(acceptedTurn(), event),
      [],
      `${event.type} ${String(event.item?.type)} must not produce a Tank event`,
    );
  }
});

test("preserves completed Codex text as durable Tank item text", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_agent_1",
      type: "agent_message",
      text: "All tests passed.",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.equal(event.source, "codex");
  assert.equal(event.actor, "assistant");
  assert.equal(event.timeline_id, "turn-run-123:item:item_agent_1");
  assert.equal(event.provider_item_id, "item_agent_1");
  assert.equal(event.visibility, "durable");
  assert.equal(event.payload?.kind, "agent_message");
  assert.equal(event.payload?.text, "All tests passed.");
  assert.equal(event.payload?.delta, undefined);
});

test("carries streamed Codex text into durable completion when final item omits text", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  // item.updated frames emit no Tank event, but the adapter must
  // remember the running text so item.completed can fall back to it.
  for (const frame of ["Partial", "Partial response"]) {
    const events = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
      type: "item.updated",
      item: { id: "item_agent_streamed", type: "agent_message", text: frame },
    });
    assert.deepEqual(events, [], "item.updated must not produce a Tank event");
  }

  const completed = mappedEvent(adapter, {
    type: "item.completed",
    item: {
      id: "item_agent_streamed",
      type: "agent_message",
    },
  });

  assert.equal(completed.type, "item.completed");
  assert.equal(completed.visibility, "durable");
  assert.equal(completed.payload?.kind, "agent_message");
  assert.equal(completed.payload?.text, "Partial response");
  assert.equal(completed.payload?.delta, undefined);
});

test("marks only the latest completed Codex assistant message as the final answer", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const turn = acceptedTurn();
  const preliminary = adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.completed",
    item: {
      id: "item_agent_prelim",
      type: "agent_message",
      text: "I found two separate levers.",
    },
  });
  const final = adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.completed",
    item: {
      id: "item_agent_final",
      type: "agent_message",
      text: "Yes, use Ambience.",
    },
  });
  const completed = adapter.canonicalEventsForCodexEvent(turn, {
    type: "turn.completed",
    usage: { input_tokens: 10 },
  });

  assert.equal(preliminary.length, 1);
  assert.equal(final.length, 1);
  assert.equal(completed.length, 1);
  assert.equal(isTankConversationEvent(stampTankEvent(completed[0]!)), true);
  assert.deepEqual(completed[0]?.payload?.final_answer, {
    timeline_ids: ["turn-run-123:item:item_agent_final"],
    provider_item_ids: ["item_agent_final"],
  });
});

test("exposes a defensive copy of the current Codex final-answer candidate", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const turn = acceptedTurn();
  adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.completed",
    item: {
      id: "item_agent_final",
      type: "agent_message",
      text: "Use the staged rollout.",
    },
  });

  const first = adapter.finalAnswerForTurn(turn.turnID);
  assert.deepEqual(first, {
    timelineIDs: ["turn-run-123:item:item_agent_final"],
    providerItemIDs: ["item_agent_final"],
  });
  first?.timelineIDs.push("mutated");
  assert.deepEqual(adapter.finalAnswerForTurn(turn.turnID), {
    timelineIDs: ["turn-run-123:item:item_agent_final"],
    providerItemIDs: ["item_agent_final"],
  });
});

test("clears a Codex assistant final-answer candidate when later tool activity arrives", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const turn = acceptedTurn();
  adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.completed",
    item: {
      id: "item_agent_prelim",
      type: "agent_message",
      text: "I will inspect that.",
    },
  });
  adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.started",
    item: {
      id: "item_command_after",
      type: "command_execution",
      command: "pwd",
    },
  });
  const completed = adapter.canonicalEventsForCodexEvent(turn, { type: "turn.completed" });

  assert.equal(completed.length, 1);
  assert.equal(completed[0]?.payload?.final_answer, undefined);
});

test("maps Codex tool items to Tank tool items with command payload", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_1",
      type: "command_execution",
      command: "npm test",
      aggregated_output: "ok",
      exit_code: 0,
      status: "completed",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.equal(event.actor, "tool");
  assert.equal(event.timeline_id, "turn-run-123:item:item_command_1");
  assert.equal(event.provider_item_id, "item_command_1");
  assert.equal(event.payload?.kind, "command_execution");
  assert.equal(event.payload?.title, "npm test");
  assert.equal(event.payload?.text, "ok");
  assert.deepEqual(event.payload?.outcome, { kind: "ok" });
  assert.deepEqual(event.payload?.raw_item, {
    id: "item_command_1",
    type: "command_execution",
    command: "npm test",
    aggregated_output: "ok",
    exit_code: 0,
    status: "completed",
  });
});

test("keeps completed Codex unified exec startup commands as foreground tool items", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const started = mappedEvent(adapter, {
    type: "item.started",
    item: {
      id: "item_unified_exec_start",
      type: "command_execution",
      command: "npm run dev",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "in_progress",
    },
  });
  assert.equal(started.type, "item.started");
  assert.equal(started.actor, "tool");
  assert.equal(started.provider_item_id, "item_unified_exec_start");
  assert.equal(started.payload?.command, "npm run dev");

  const updated = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
    type: "item.updated",
    item: {
      id: "item_unified_exec_start",
      type: "command_execution",
      command: "npm run dev",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "in_progress",
      aggregated_output: "Listening on 5173",
    },
  });
  assert.deepEqual(updated, []);

  const exited = mappedEvent(adapter, {
    type: "item.completed",
    item: {
      id: "item_unified_exec_start",
      type: "command_execution",
      command: "npm run dev",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "completed",
      exit_code: 0,
    },
  });
  assert.equal(exited.type, "item.completed");
  assert.equal(exited.timeline_id, started.timeline_id);
  assert.equal(exited.payload?.text, "Listening on 5173");
  assert.equal(exited.payload?.exit_code, 0);
});

test("promotes unfinished Codex unified exec startup commands to shell tasks at turn boundary", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const started = mappedEvent(adapter, {
    type: "item.started",
    item: {
      id: "item_unified_exec_bg",
      type: "command_execution",
      command: "npm run dev",
      cwd: "/workspace/app",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "in_progress",
    },
  });
  assert.equal(started.type, "item.started");

  const updated = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
    type: "item.updated",
    item: {
      id: "item_unified_exec_bg",
      type: "command_execution",
      command: "npm run dev",
      cwd: "/workspace/app",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "in_progress",
      aggregated_output: "Listening on 5173",
    },
  });
  assert.deepEqual(updated, []);

  const completedTurn = adapter.canonicalEventsForCodexEvent(acceptedTurn(), {
    type: "turn.completed",
    usage: { input_tokens: 10 },
  });
  assert.equal(completedTurn.length, 2);
  const promoted = completedTurn[0]!;
  assert.equal(promoted.type, "shell_task.started");
  assert.equal(promoted.task_id, "proc-123");
  assert.equal(promoted.provider_item_id, "item_unified_exec_bg");
  assert.equal(promoted.payload?.status, "running");
  assert.equal(promoted.payload?.command, "npm run dev");
  assert.equal(promoted.payload?.cwd, "/workspace/app");
  assert.equal(promoted.payload?.output, "Listening on 5173");
  assert.equal(completedTurn[1]?.type, "turn.completed");

  const exited = mappedEvent(adapter, {
    type: "item.completed",
    item: {
      id: "item_unified_exec_bg",
      type: "command_execution",
      command: "npm run dev",
      process_id: "proc-123",
      source: "unifiedExecStartup",
      status: "completed",
      exit_code: 0,
    },
  });
  assert.equal(exited.type, "shell_task.exited");
  assert.equal(exited.timeline_id, promoted.timeline_id);
  assert.equal(exited.payload?.exit_code, 0);
});

test("maps Codex unified exec interaction events to shell task lifecycle", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.started",
    item: {
      id: "item_unified_exec_interaction",
      type: "command_execution",
      command: "wait proc-123",
      process_id: "proc-123",
      source: "unifiedExecInteraction",
      status: "in_progress",
    },
  });

  assert.equal(event.type, "shell_task.started");
  assert.equal(event.task_id, "proc-123");
  assert.equal(event.payload?.command, "wait proc-123");
});

test("maps Codex nonzero exit codes to completed result_failed outcomes", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_failed_result",
      type: "command_execution",
      command: "npm test",
      aggregated_output: "1 failed",
      exit_code: 1,
      status: "completed",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.deepEqual(event.payload?.outcome, { kind: "result_failed", reason: "exit_code", code: 1 });
  assert.equal(event.payload?.exit_code, 1);
});

test("maps camelCase or nested Codex exit codes to result_failed outcomes", () => {
  const camel = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_camel_exit",
      type: "command_execution",
      command: "false",
      exitCode: 2,
    },
  });
  assert.deepEqual(camel.payload?.outcome, { kind: "result_failed", reason: "exit_code", code: 2 });
  assert.equal(camel.payload?.exit_code, 2);

  const nested = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_nested_exit",
      type: "command_execution",
      command: "false",
      result: { exit_code: 3 },
    },
  });
  assert.deepEqual(nested.payload?.outcome, { kind: "result_failed", reason: "exit_code", code: 3 });
  assert.equal(nested.payload?.exit_code, 3);
});

test("maps Codex failed status without execution error to completed result_failed outcomes", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_mcp_failed_status",
      type: "mcp_tool_call",
      tool: "mcp__server__action",
      error: null,
      status: "failed",
    },
  });

  assert.equal(event.type, "item.completed");
  assert.deepEqual(event.payload?.outcome, {
    kind: "result_failed",
    reason: "codex_item_status_failed",
  });
});

test("maps Codex execution errors to Tank item.failed", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "item.completed",
    item: {
      id: "item_command_2",
      type: "command_execution",
      command: "npm test",
      error: "failed",
    },
  });

  assert.equal(event.type, "item.failed");
  assert.equal(event.actor, "tool");
  assert.equal(event.visibility, "durable");
  assert.equal(event.payload?.error, "failed");
  assert.deepEqual(event.payload?.outcome, {
    kind: "execution_failed",
    reason: "provider_item_error",
  });
});

test("maps Codex terminal events to Tank turn lifecycle", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const usageObservation = {
    usage_source: "thread.tokenUsage.updated",
    provider_turn_id: "turn-provider-1",
  };
  const usageUpdate = mappedEvent(adapter, {
    type: "turn.usage",
    id: "turn-provider-1:usage:1",
    usage: { input_tokens: 9 },
    usage_observation: usageObservation,
  });
  assert.equal(usageUpdate.type, "turn.usage");
  assert.equal(usageUpdate.event_id, "turn-run-123:turn.usage:turn-provider-1:usage:1");
  assert.deepEqual(usageUpdate.payload?.usage, { input_tokens: 9 });
  assert.deepEqual(usageUpdate.payload?.usage_observation, usageObservation);

  const completed = mappedEvent(adapter, {
    type: "turn.completed",
    usage: { input_tokens: 10 },
    usage_observation: usageObservation,
  });
  assert.equal(completed.type, "turn.completed");
  assert.deepEqual(completed.payload?.usage, { input_tokens: 10 });
  assert.deepEqual(completed.payload?.usage_observation, usageObservation);

  const interrupted = mappedEvent(adapter, { type: "turn.interrupted", usage: { input_tokens: 5 } });
  assert.equal(interrupted.type, "turn.interrupted");
  assert.equal(interrupted.payload?.reason, "client_interrupt");
  assert.deepEqual(interrupted.payload?.usage, { input_tokens: 5 });

  const failed = mappedEvent(adapter, { type: "error", message: "quota exceeded", usage: { input_tokens: 7 } });
  assert.equal(failed.type, "turn.failed");
  assert.equal(failed.payload?.reason, "provider_failure");
  assert.equal(failed.payload?.error, "quota exceeded");
  assert.deepEqual(failed.payload?.usage, { input_tokens: 7 });
});

test("maps Codex context compaction to a durable Tank notice", () => {
  const event = mappedEvent(new CodexTankEventAdapter(cfg()), {
    type: "context.compacted",
    id: "thread/compacted:turn-provider-1",
    thread_id: "thread-1",
    turn_id: "turn-provider-1",
    trigger: "auto",
  });

  assert.equal(event.type, "context.compacted");
  assert.equal(event.source, "codex");
  assert.equal(event.actor, "runner");
  assert.equal(event.turn_id, "turn-run-123");
  assert.equal(event.visibility, "durable");
  assert.equal(event.producer?.provider_event_id, "turn-provider-1");
  assert.deepEqual(event.payload, { trigger: "auto" });
});

test("ignores unknown Codex provider event types", () => {
  const events = canonicalEventsForCodexEvent(
    cfg(),
    acceptedTurn(),
    { type: "future.experimental.event", value: true },
  );
  assert.deepEqual(events, []);
});

// --- Idle background completion + park stamp (codex park/re-invoke/fold parity) ---
//
// A unified-exec shell that outlives its turn must (a) stamp the turn
// terminal with background_work_pending so the session parks scheduled
// instead of summoning, and (b) when its completion arrives with NO active
// turn, publish shell_task.exited attributed to the ORIGINATING turn — the
// durable fold edge that lets the later turn_bgtask wake turn fold into it.

test("turn terminal stamps background_work_pending while a unified-exec shell runs", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const turn = acceptedTurn({ turnID: "turn-bg-1", clientNonce: "bg-1" });

  const started = adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.started",
    item: {
      id: "item_bg_shell",
      type: "command_execution",
      command: "sleep 90 && echo DONE",
      process_id: "proc-9",
      source: "unifiedExecInteraction",
      status: "in_progress",
    },
  });
  assert.equal(started.length, 1);
  assert.equal(started[0]!.type, "shell_task.started");
  assert.equal(started[0]!.turn_id, "turn-bg-1");

  const terminal = adapter.canonicalEventsForCodexEvent(turn, {
    type: "turn.completed",
  });
  const completed = terminal.find((event) => event.type === "turn.completed");
  assert.ok(completed, "turn.completed missing");
  assert.equal(completed!.payload?.background_work_pending, true);

  const idle = adapter.idleBackgroundShellEvents({
    type: "item.completed",
    item: {
      id: "item_bg_shell",
      type: "command_execution",
      command: "sleep 90 && echo DONE",
      process_id: "proc-9",
      source: "unifiedExecInteraction",
      status: "completed",
      aggregated_output: "DONE",
      exit_code: 0,
    },
  });
  assert.equal(idle.length, 1);
  assert.equal(idle[0]!.type, "shell_task.exited");
  assert.equal(
    idle[0]!.turn_id,
    "turn-bg-1",
    "idle completion must attribute to the originating turn (the fold edge)",
  );
  assert.equal(isTankConversationEvent(stampTankEvent(idle[0]!)), true);

  // After the shell drained, a later terminal must not stamp pending.
  const turn2 = acceptedTurn({ turnID: "turn-bg-2", clientNonce: "bg-2" });
  const terminal2 = adapter.canonicalEventsForCodexEvent(turn2, {
    type: "turn.completed",
  });
  const completed2 = terminal2.find((event) => event.type === "turn.completed");
  assert.equal(completed2!.payload?.background_work_pending, false);
});

test("idle completion for an untracked item publishes nothing", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const idle = adapter.idleBackgroundShellEvents({
    type: "item.completed",
    item: {
      id: "item_unknown",
      type: "command_execution",
      command: "echo hi",
      status: "completed",
    },
  });
  assert.deepEqual(idle, []);
});

test("process-exit completion synthesizes exited attributed to the origin turn", () => {
  const adapter = new CodexTankEventAdapter(cfg());
  const turn = acceptedTurn({ turnID: "turn-pid-1", clientNonce: "pid-1" });
  adapter.canonicalEventsForCodexEvent(turn, {
    type: "item.started",
    item: {
      id: "item_pid_shell",
      type: "command_execution",
      command: "sleep 75 && echo DONE",
      process_id: "424242",
      source: "unifiedExecStartup",
      status: "in_progress",
    },
  });
  // Startup items defer until turn-end promotion (the live session-161
  // shape: shell_task.started and the bwp-stamped terminal land together).
  const terminal = adapter.canonicalEventsForCodexEvent(turn, { type: "turn.completed" });
  assert.equal(
    terminal.find((event) => event.type === "turn.completed")?.payload?.background_work_pending,
    true,
  );
  const pending = adapter.pendingBackgroundTasks();
  assert.equal(pending.length, 1);
  assert.equal(pending[0]!.processID, 424242);

  const exited = adapter.completeBackgroundShellByExit(pending[0]!.taskID);
  assert.equal(exited.length, 1);
  assert.equal(exited[0]!.type, "shell_task.exited");
  assert.equal(exited[0]!.turn_id, "turn-pid-1");
  assert.equal(exited[0]!.payload?.completion_source, "process_exit_observed");
  assert.equal(isTankConversationEvent(stampTankEvent(exited[0]!)), true);
  assert.deepEqual(adapter.pendingBackgroundTasks(), []);
  // Second call is a no-op (already drained) — watcher double-fire safety.
  assert.deepEqual(adapter.completeBackgroundShellByExit(pending[0]!.taskID), []);
});

test("command signatures normalize shell quoting for /proc matching", async () => {
  const { normalizeCommandSignature } = await import("../runner.js");
  // The live shapes from slot-1 session 161: codex REPORTS -lc with
  // quotes; the spawned process's argv is -c without quotes.
  const reported = "/bin/sh -lc 'sleep 60 && echo FINAL_ROUND_DONE'";
  const cmdline = "/bin/sh -c sleep 60 && echo FINAL_ROUND_DONE ";
  assert.equal(
    normalizeCommandSignature(cmdline).includes(normalizeCommandSignature(reported)),
    true,
  );
});

test("adoptBackgroundTask re-seeds a restart-orphaned shell into the watcher and idle paths", () => {
  const adapter = new CodexTankEventAdapter(cfg());

  assert.equal(
    adapter.adoptBackgroundTask("777", {
      turnID: "turn-origin",
      providerItemID: "call_adopted",
      command: "/bin/sh -lc 'sleep 600'",
      processID: 777,
    }),
    true,
  );
  // Adopted shells are watcher-visible like live-tracked ones.
  const pending = adapter.pendingBackgroundTasks();
  assert.equal(pending.length, 1);
  assert.equal(pending[0].taskID, "777");
  assert.equal(pending[0].command, "/bin/sh -lc 'sleep 600'");

  // Double adoption is a no-op.
  assert.equal(
    adapter.adoptBackgroundTask("777", {
      turnID: "turn-other",
      providerItemID: "x",
      command: "y",
      processID: null,
    }),
    false,
  );

  // A late idle item notification for the adopted task maps onto the
  // ORIGINATING turn — the fold edge survives the restart.
  const events = adapter.idleBackgroundShellEvents({
    type: "item.completed",
    item: {
      id: "call_adopted",
      type: "command_execution",
      command: "/bin/sh -lc 'sleep 600'",
      status: "completed",
      process_id: "777",
    },
  } as never);
  assert.equal(events.length, 1);
  assert.equal(events[0].type, "shell_task.exited");
  assert.equal(events[0].turn_id, "turn-origin");
});
