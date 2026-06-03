import { test } from "node:test";
import assert from "node:assert/strict";
import type { ThreadOptions } from "@openai/codex-sdk";

import { CodexAppServerTransport, appServerItemToCodexItem } from "./appServerTransport.js";
import { registry } from "./metrics.js";

test("appServerItemToCodexItem preserves unified exec process metadata", () => {
  const item = appServerItemToCodexItem({
    id: "item-1",
    type: "commandExecution",
    command: "npm run dev",
    cwd: "/workspace/app",
    processId: "proc-1",
    source: "unifiedExecStartup",
    aggregatedOutput: "ready",
    exitCode: 0,
    durationMs: 1234,
    status: "succeeded",
  });

  assert.deepEqual(item, {
    id: "item-1",
    type: "command_execution",
    command: "npm run dev",
    cwd: "/workspace/app",
    process_id: "proc-1",
    source: "unifiedExecStartup",
    aggregated_output: "ready",
    exit_code: 0,
    status: "completed",
    duration_ms: 1234,
  });
});

test("unmapped app-server notifications increment the provider-event counter", async () => {
  await registry.resetMetrics();
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  const internals = transport as unknown as {
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };

  internals.handleNotification("thread/futureNotification", {
    threadId: "thread-1",
    turnId: "turn-provider-1",
  });

  const metrics = await registry.metrics();
  assert.match(
    metrics,
    /tank_runner_unmapped_provider_event_total\{(?=[^}]*type="thread\/futureNotification")(?=[^}]*subtype="none")(?=[^}]*mode="codex")[^}]*\} 1/,
  );
});

test("runTurn maps Codex thread/compacted notifications to context.compacted events", async () => {
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  const internals = transport as unknown as {
    start: () => Promise<void>;
    ensureThread: (threadOptions: ThreadOptions) => Promise<string>;
    request: (method: string, params: unknown) => Promise<unknown>;
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };
  internals.start = async () => {};
  internals.ensureThread = async () => "thread-1";
  internals.request = async () => ({ turn: { id: "turn-provider-1" } });

  const iter = transport.runTurn("compact context", {} as ThreadOptions);
  const first = iter.next();
  await new Promise((resolve) => setImmediate(resolve));
  internals.handleNotification("thread/compacted", {
    threadId: "thread-1",
    turnId: "turn-provider-1",
  });

  assert.deepEqual(await first, {
    done: false,
    value: {
      type: "context.compacted",
      id: "thread/compacted:turn-provider-1",
      thread_id: "thread-1",
      turn_id: "turn-provider-1",
      trigger: "auto",
    },
  });

  internals.handleNotification("turn/completed", {
    threadId: "thread-1",
    turn: { id: "turn-provider-1", status: "completed" },
  });
  const terminal = await iter.next();
  assert.equal(terminal.done, false);
  if (terminal.done) throw new Error("expected terminal event");
  assert.equal(terminal.value.type, "turn.completed");
});

test("runTurn maps Codex contextCompaction item frames once", async () => {
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  const internals = transport as unknown as {
    start: () => Promise<void>;
    ensureThread: (threadOptions: ThreadOptions) => Promise<string>;
    request: (method: string, params: unknown) => Promise<unknown>;
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };
  internals.start = async () => {};
  internals.ensureThread = async () => "thread-1";
  internals.request = async () => ({ turn: { id: "turn-provider-1" } });

  const iter = transport.runTurn("compact context", {} as ThreadOptions);
  const first = iter.next();
  await new Promise((resolve) => setImmediate(resolve));
  const params = {
    threadId: "thread-1",
    turnId: "turn-provider-1",
    item: { id: "context-compaction-1", type: "contextCompaction" },
  };
  internals.handleNotification("item/started", params);
  internals.handleNotification("item/completed", params);

  assert.deepEqual(await first, {
    done: false,
    value: {
      type: "context.compacted",
      id: "turn-provider-1:context-compaction-1",
      thread_id: "thread-1",
      turn_id: "turn-provider-1",
      item_id: "context-compaction-1",
      trigger: "auto",
    },
  });

  internals.handleNotification("turn/completed", {
    threadId: "thread-1",
    turn: { id: "turn-provider-1", status: "completed" },
  });
  const terminal = await iter.next();
  assert.equal(terminal.done, false);
  if (terminal.done) throw new Error("expected terminal event");
  assert.equal(terminal.value.type, "turn.completed");
});

test("runTurn abort cuts ahead of queued events and interrupts once turn id is known", async () => {
  const ctrl = new AbortController();
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  let resolveTurnStart!: (value: unknown) => void;
  let turnStartCalled!: () => void;
  const turnStartRequest = new Promise<void>((resolve) => {
    turnStartCalled = resolve;
  });
  const interrupts: unknown[] = [];
  const internals = transport as unknown as {
    start: () => Promise<void>;
    ensureThread: (threadOptions: ThreadOptions) => Promise<string>;
    request: (method: string, params: unknown) => Promise<unknown>;
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };
  internals.start = async () => {};
  internals.ensureThread = async () => "thread-1";
  internals.request = async (method, params) => {
    if (method === "turn/start") {
      turnStartCalled();
      return new Promise((resolve) => {
        resolveTurnStart = resolve;
      });
    }
    if (method === "turn/interrupt") {
      interrupts.push(params);
      return {};
    }
    return {};
  };

  const iter = transport.runTurn("start server", {} as ThreadOptions, ctrl.signal);
  const first = iter.next();
  await turnStartRequest;
  internals.handleNotification("item/started", {
    item: { id: "item-1", type: "agentMessage", text: "working" },
  });
  assert.deepEqual(await first, {
    done: false,
    value: { type: "item.started", item: { id: "item-1", type: "agent_message", text: "working" } },
  });

  internals.handleNotification("item/updated", {
    item: { id: "item-1", type: "agentMessage", text: "still working" },
  });
  ctrl.abort();
  await assert.rejects(iter.next(), { name: "AbortError" });

  internals.handleNotification("turn/started", { turn: { id: "turn-provider-1" } });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(interrupts, [{ threadId: "thread-1", turnId: "turn-provider-1" }]);

  resolveTurnStart({ turn: { id: "turn-provider-1" } });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(interrupts, [{ threadId: "thread-1", turnId: "turn-provider-1" }]);
});

test("runTurn attaches latest app-server token usage to terminal events", async () => {
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  const internals = transport as unknown as {
    start: () => Promise<void>;
    ensureThread: (threadOptions: ThreadOptions) => Promise<string>;
    request: (method: string, params: unknown) => Promise<unknown>;
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };
  internals.start = async () => {};
  internals.ensureThread = async () => "thread-1";
  internals.request = async () => ({ turn: { id: "turn-provider-1" } });

  const iter = transport.runTurn("track usage", {} as ThreadOptions);
  const first = iter.next();
  await new Promise((resolve) => setImmediate(resolve));
  internals.handleNotification("thread/tokenUsage/updated", {
    threadId: "thread-1",
    turnId: "turn-provider-1",
    tokenUsage: {
      total: {
        totalTokens: 125,
        inputTokens: 100,
        cachedInputTokens: 40,
        outputTokens: 20,
        reasoningOutputTokens: 5,
      },
      last: {
        totalTokens: 50,
        inputTokens: 30,
        cachedInputTokens: 10,
        outputTokens: 15,
        reasoningOutputTokens: 5,
      },
      modelContextWindow: 200_000,
    },
  });
  internals.handleNotification("turn/completed", {
    threadId: "thread-1",
    turn: { id: "turn-provider-1", status: "completed" },
  });

  const usageResult = await first;
  assert.equal(usageResult.done, false);
  if (usageResult.done) throw new Error("expected usage event");
  assert.equal(usageResult.value.type, "turn.usage");
  assert.deepEqual(usageResult.value.usage, {
    input_tokens: 100,
    cached_input_tokens: 40,
    output_tokens: 20,
    reasoning_output_tokens: 5,
    total_tokens: 125,
  });
  assert.deepEqual(pickUsageObservationFields(usageResult.value.usage_observation), {
    provider_turn_id: "turn-provider-1",
    usage_source: "thread.tokenUsage.updated",
    terminal_had_usage: false,
    terminal_had_token_usage: false,
    cached_usage_available: true,
    update_count: 1,
  });
  assert.equal(typeof (usageResult.value.usage_observation as Record<string, unknown>)?.event_at, "string");
  assert.equal(typeof (usageResult.value.usage_observation as Record<string, unknown>)?.first_update_at, "string");
  assert.equal(typeof (usageResult.value.usage_observation as Record<string, unknown>)?.last_update_at, "string");

  const terminalResult = await iter.next();
  assert.equal(terminalResult.done, false);
  if (terminalResult.done) throw new Error("expected terminal event");
  assert.equal(terminalResult.value.type, "turn.completed");
  assert.deepEqual(terminalResult.value.usage, {
    input_tokens: 100,
    cached_input_tokens: 40,
    output_tokens: 20,
    reasoning_output_tokens: 5,
    total_tokens: 125,
  });
  assert.deepEqual(pickUsageObservationFields(terminalResult.value.usage_observation), {
    provider_turn_id: "turn-provider-1",
    usage_source: "thread.tokenUsage.updated",
    terminal_had_usage: false,
    terminal_had_token_usage: false,
    cached_usage_available: true,
    update_count: 1,
  });
  assert.equal(typeof (terminalResult.value.usage_observation as Record<string, unknown>)?.terminal_at, "string");
  assert.equal(typeof (terminalResult.value.usage_observation as Record<string, unknown>)?.first_update_at, "string");
  assert.equal(typeof (terminalResult.value.usage_observation as Record<string, unknown>)?.last_update_at, "string");
  assert.equal(typeof (terminalResult.value.usage_observation as Record<string, unknown>)?.last_update_age_ms, "number");
});

test("runTurn records direct terminal usage separately from cached token updates", async () => {
  const transport = new CodexAppServerTransport({
    cwd: "/workspace/app",
    onRequestUserInput: async () => ({ answers: {} }),
  });
  const internals = transport as unknown as {
    start: () => Promise<void>;
    ensureThread: (threadOptions: ThreadOptions) => Promise<string>;
    request: (method: string, params: unknown) => Promise<unknown>;
    handleNotification: (method: string, params?: Record<string, unknown>) => void;
  };
  internals.start = async () => {};
  internals.ensureThread = async () => "thread-1";
  internals.request = async () => ({ turn: { id: "turn-provider-1" } });

  const iter = transport.runTurn("track direct usage", {} as ThreadOptions);
  const first = iter.next();
  await new Promise((resolve) => setImmediate(resolve));
  internals.handleNotification("thread/tokenUsage/updated", {
    threadId: "thread-1",
    turnId: "turn-provider-1",
    tokenUsage: {
      total: {
        totalTokens: 125,
        inputTokens: 100,
        cachedInputTokens: 40,
        outputTokens: 20,
        reasoningOutputTokens: 5,
      },
    },
  });
  internals.handleNotification("turn/completed", {
    threadId: "thread-1",
    turn: {
      id: "turn-provider-1",
      status: "completed",
      usage: {
        inputTokens: 12,
        cachedInputTokens: 3,
        outputTokens: 4,
        reasoningOutputTokens: 1,
        totalTokens: 16,
      },
    },
  });

  const usageResult = await first;
  assert.equal(usageResult.done, false);
  if (usageResult.done) throw new Error("expected usage event");
  assert.equal(usageResult.value.type, "turn.usage");
  assert.deepEqual(usageResult.value.usage, {
    input_tokens: 100,
    cached_input_tokens: 40,
    output_tokens: 20,
    reasoning_output_tokens: 5,
    total_tokens: 125,
  });

  const terminalResult = await iter.next();
  assert.equal(terminalResult.done, false);
  if (terminalResult.done) throw new Error("expected terminal event");
  assert.equal(terminalResult.value.type, "turn.completed");
  assert.deepEqual(terminalResult.value.usage, {
    input_tokens: 12,
    cached_input_tokens: 3,
    output_tokens: 4,
    reasoning_output_tokens: 1,
    total_tokens: 16,
  });
  assert.deepEqual(pickUsageObservationFields(terminalResult.value.usage_observation), {
    provider_turn_id: "turn-provider-1",
    usage_source: "turn.usage",
    terminal_had_usage: true,
    terminal_had_token_usage: false,
    cached_usage_available: true,
    update_count: 1,
  });
});

function pickUsageObservationFields(value: unknown): Record<string, unknown> {
  assert.ok(value && typeof value === "object");
  const record = value as Record<string, unknown>;
  return {
    provider_turn_id: record.provider_turn_id,
    usage_source: record.usage_source,
    terminal_had_usage: record.terminal_had_usage,
    terminal_had_token_usage: record.terminal_had_token_usage,
    cached_usage_available: record.cached_usage_available,
    update_count: record.update_count,
  };
}
