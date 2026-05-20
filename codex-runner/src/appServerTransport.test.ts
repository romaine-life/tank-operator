import { test } from "node:test";
import assert from "node:assert/strict";
import type { ThreadOptions } from "@openai/codex-sdk";

import { CodexAppServerTransport, appServerItemToCodexItem } from "./appServerTransport.js";

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
