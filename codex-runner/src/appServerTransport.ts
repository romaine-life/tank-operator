import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createRequire } from "node:module";
import { createInterface } from "node:readline";
import type { ThreadOptions } from "@openai/codex-sdk";

import type { CodexEvent } from "./sessionEvents.js";
import { providerControlTotal, providerErrorTotal } from "./metrics.js";

type JsonRecord = Record<string, unknown>;
const require = createRequire(import.meta.url);
const CODEX_BIN = require.resolve("@openai/codex/bin/codex.js");

export type AppServerUserInputQuestion = {
  id: string;
  header: string;
  question: string;
  isOther?: boolean;
  isSecret?: boolean;
  options: Array<{ label: string; description: string }> | null;
};

export type AppServerUserInputRequest = {
  requestID: string;
  threadID: string;
  providerTurnID: string;
  providerItemID: string;
  questions: AppServerUserInputQuestion[];
};

export type AppServerUserInputResponse = {
  answers: Record<string, { answers: string[] }>;
};

type AppServerTransportOptions = {
  cwd: string;
  onRequestUserInput: (
    request: AppServerUserInputRequest,
    signal?: AbortSignal,
  ) => Promise<AppServerUserInputResponse>;
  onRuntimeConfigApplied?: (threadOptions: ThreadOptions) => void;
};

type PendingRequest = {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
};

type QueuedEvent =
  | { kind: "event"; event: CodexEvent }
  | { kind: "error"; error: Error };

function abortError(message = "turn interrupted"): Error {
  const err = new Error(message);
  err.name = "AbortError";
  return err;
}

class AsyncEventQueue {
  private readonly items: QueuedEvent[] = [];
  private readonly waiters: Array<(item: QueuedEvent | null) => void> = [];
  private closed = false;

  push(item: QueuedEvent): void {
    if (this.closed) return;
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter(item);
      return;
    }
    this.items.push(item);
  }

  fail(error: Error): void {
    if (this.closed) return;
    this.items.length = 0;
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter({ kind: "error", error });
    } else {
      this.items.push({ kind: "error", error });
    }
    this.close();
  }

  close(): void {
    this.closed = true;
    for (const waiter of this.waiters.splice(0)) waiter(null);
  }

  async next(): Promise<QueuedEvent | null> {
    const item = this.items.shift();
    if (item) return item;
    if (this.closed) return null;
    return new Promise((resolve) => this.waiters.push(resolve));
  }
}

export class CodexAppServerTransport {
  private child: ChildProcessWithoutNullStreams | null = null;
  private nextID = 1;
  private threadID: string | null = null;
  private readonly pending = new Map<number, PendingRequest>();
  private activeQueue: AsyncEventQueue | null = null;
  private activeProviderTurnID: string | null = null;
  private activeTurnControl: {
    threadID: string;
    abortRequested: boolean;
    interruptSent: boolean;
  } | null = null;
  private readonly itemsByID = new Map<string, JsonRecord>();

  constructor(private readonly opts: AppServerTransportOptions) {}

  async start(): Promise<void> {
    if (this.child) return;
    const child = spawn(process.execPath, [CODEX_BIN, "app-server", "--listen", "stdio://"], {
      cwd: this.opts.cwd,
      env: process.env,
    });
    this.child = child;
    child.once("exit", (code, signal) => {
      const err = new Error(`codex app-server exited with ${signal ?? `code ${code ?? 1}`}`);
      for (const pending of this.pending.values()) pending.reject(err);
      this.pending.clear();
      this.activeQueue?.push({ kind: "error", error: err });
      this.activeQueue?.close();
    });
    child.stderr.on("data", (chunk) => process.stderr.write(chunk));
    const rl = createInterface({ input: child.stdout, crlfDelay: Infinity });
    void (async () => {
      for await (const line of rl) {
        this.handleLine(String(line));
      }
    })().catch((err) => {
      this.activeQueue?.push({ kind: "error", error: err instanceof Error ? err : new Error(String(err)) });
    });
    await this.request("initialize", {
      clientInfo: { name: "tank-operator", title: "Tank Operator", version: "dev" },
      capabilities: { experimentalApi: true },
    });
    this.notify("initialized");
  }

  async stop(): Promise<void> {
    const child = this.child;
    this.child = null;
    this.activeQueue?.close();
    if (child && !child.killed) child.kill();
  }

  async cleanBackgroundTerminals(): Promise<void> {
    await this.start();
    const threadID = this.threadID;
    if (!threadID) throw new Error("codex app-server thread is not available");
    try {
      await this.request("thread/backgroundTerminals/clean", {
        threadId: threadID,
      });
      providerControlTotal.labels("background_terminals_clean", "sent").inc();
    } catch (err) {
      providerControlTotal.labels("background_terminals_clean", "failed").inc();
      providerErrorTotal.labels("background_terminals_clean").inc();
      throw err;
    }
  }

  async *runTurn(
    input: string,
    threadOptions: ThreadOptions,
    signal?: AbortSignal,
  ): AsyncGenerator<CodexEvent> {
    if (signal?.aborted) throw abortError();
    await this.start();
    if (signal?.aborted) throw abortError();
    const threadID = await this.ensureThread(threadOptions);
    if (signal?.aborted) throw abortError();
    const queue = new AsyncEventQueue();
    this.activeQueue = queue;
    this.activeProviderTurnID = null;
    const turnControl = {
      threadID,
      abortRequested: false,
      interruptSent: false,
    };
    this.activeTurnControl = turnControl;
    this.itemsByID.clear();
    let terminalSeen = false;
    let abortRequested = false;
    const onAbort = () => {
      if (terminalSeen) return;
      abortRequested = true;
      turnControl.abortRequested = true;
      if (this.activeProviderTurnID) {
        this.interruptProviderTurn(turnControl, this.activeProviderTurnID);
      }
      queue.fail(abortError());
    };
    signal?.addEventListener("abort", onAbort, { once: true });
    try {
      if (signal?.aborted) {
        onAbort();
      }
      const turnStart = this.request("turn/start", {
        threadId: threadID,
        input: [{ type: "text", text: input, text_elements: [] }],
        cwd: this.opts.cwd,
        approvalPolicy: "never",
        sandboxPolicy: { type: "dangerFullAccess" },
      });
      void turnStart.then((result) => {
        const turnID = turnIDFromTurnStartResponse(result);
        if (turnID && this.activeQueue === queue && !this.activeProviderTurnID) {
          this.activeProviderTurnID = turnID;
        }
        if (abortRequested && turnID) {
          this.interruptProviderTurn(turnControl, turnID);
        }
      }).catch((err) => {
        queue.push({ kind: "error", error: err instanceof Error ? err : new Error(String(err)) });
      }).finally(() => {
        if (this.activeTurnControl === turnControl && (!turnControl.abortRequested || turnControl.interruptSent)) {
          this.activeTurnControl = null;
        }
      });
      for (;;) {
        const item = await queue.next();
        if (!item) break;
        if (item.kind === "error") throw item.error;
        yield item.event;
        if (
          item.event.type === "turn.completed" ||
          item.event.type === "turn.failed" ||
          item.event.type === "turn.interrupted"
        ) {
          terminalSeen = true;
          break;
        }
      }
      if (abortRequested && !terminalSeen) throw abortError();
      await turnStart.catch((err) => {
        throw err instanceof Error ? err : new Error(String(err));
      });
    } finally {
      signal?.removeEventListener("abort", onAbort);
      if (this.activeQueue === queue) this.activeQueue = null;
      if (this.activeTurnControl === turnControl && (!turnControl.abortRequested || turnControl.interruptSent)) {
        this.activeTurnControl = null;
        this.activeProviderTurnID = null;
      }
      this.itemsByID.clear();
      queue.close();
    }
  }

  private async ensureThread(threadOptions: ThreadOptions): Promise<string> {
    if (this.threadID) return this.threadID;
    const response = await this.request("thread/start", {
      cwd: this.opts.cwd,
      sandbox: "danger-full-access",
      approvalPolicy: "never",
      config: {
        features: { default_mode_request_user_input: true },
        ...(threadOptions.model ? { model: threadOptions.model } : {}),
        ...(threadOptions.modelReasoningEffort
          ? { model_reasoning_effort: threadOptions.modelReasoningEffort }
          : {}),
      },
    }) as JsonRecord;
    const thread = response.thread as JsonRecord | undefined;
    const id = typeof thread?.id === "string" ? thread.id : undefined;
    if (!id) throw new Error("codex app-server thread/start response did not include thread.id");
    this.threadID = id;
    this.opts.onRuntimeConfigApplied?.(threadOptions);
    this.activeQueue?.push({ kind: "event", event: { type: "thread.started", thread_id: id } });
    return id;
  }

  private request(method: string, params: unknown): Promise<unknown> {
    const child = this.child;
    if (!child) return Promise.reject(new Error("codex app-server is not running"));
    const id = this.nextID++;
    const message = JSON.stringify({ id, method, params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      child.stdin.write(`${message}\n`);
    });
  }

  private respond(id: number | string, result: unknown): void {
    this.child?.stdin.write(`${JSON.stringify({ id, result })}\n`);
  }

  private notify(method: string, params?: unknown): void {
    this.child?.stdin.write(`${JSON.stringify({ method, ...(params === undefined ? {} : { params }) })}\n`);
  }

  private handleLine(line: string): void {
    let message: JsonRecord;
    try {
      message = JSON.parse(line) as JsonRecord;
    } catch {
      return;
    }
    if (typeof message.id === "number" && ("result" in message || "error" in message)) {
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) {
        pending.reject(new Error(JSON.stringify(message.error)));
      } else {
        pending.resolve(message.result);
      }
      return;
    }
    if (typeof message.method !== "string") return;
    if ("id" in message && (typeof message.id === "number" || typeof message.id === "string")) {
      void this.handleServerRequest(message.method, message.id, message.params as JsonRecord | undefined);
      return;
    }
    this.handleNotification(message.method, message.params as JsonRecord | undefined);
  }

  private handleNotification(method: string, params?: JsonRecord): void {
    if (method === "turn/started") {
      const turn = params?.turn;
      const turnID =
        turn && typeof turn === "object" && typeof (turn as JsonRecord).id === "string"
          ? (turn as JsonRecord).id as string
          : undefined;
      if (turnID) {
        this.activeProviderTurnID = turnID;
        const control = this.activeTurnControl;
        if (control?.abortRequested) this.interruptProviderTurn(control, turnID);
      }
      const queue = this.activeQueue;
      if (queue) queue.push({ kind: "event", event: { type: "turn.started", id: turnID } });
      return;
    }
    const queue = this.activeQueue;
    if (!queue) return;
    if (method === "turn/completed") {
      const turn = params?.turn;
      const status =
        turn && typeof turn === "object" && typeof (turn as JsonRecord).status === "string"
          ? (turn as JsonRecord).status
          : "";
      queue.push({
        kind: "event",
        event: {
          type:
            status === "interrupted"
              ? "turn.interrupted"
              : status === "failed"
                ? "turn.failed"
                : "turn.completed",
          usage: null,
        },
      });
      return;
    }
    if (method === "item/started" || method === "item/completed" || method === "item/updated") {
      const item = params?.item;
      if (!item || typeof item !== "object") return;
      const codexItem = appServerItemToCodexItem(item as JsonRecord);
      const itemID = typeof codexItem.id === "string" ? codexItem.id : "";
      if (itemID) this.itemsByID.set(itemID, codexItem);
      queue.push({
        kind: "event",
        event: {
          type:
            method === "item/started"
              ? "item.started"
              : method === "item/completed"
                ? "item.completed"
                : "item.updated",
          item: codexItem,
        },
      });
      return;
    }
    if (method === "item/commandExecution/outputDelta") {
      const itemID = typeof params?.itemId === "string" ? params.itemId : "";
      if (!itemID) return;
      const existing = this.itemsByID.get(itemID) ?? {
        id: itemID,
        type: "command_execution",
        aggregated_output: "",
        status: "in_progress",
      };
      const delta = typeof params?.delta === "string" ? params.delta : "";
      const next = {
        ...existing,
        aggregated_output: `${String(existing.aggregated_output ?? "")}${delta}`,
        status: existing.status ?? "in_progress",
      };
      this.itemsByID.set(itemID, next);
      queue.push({ kind: "event", event: { type: "item.updated", item: next } });
      return;
    }
    if (method === "error") {
      queue.push({
        kind: "event",
        event: { type: "error", message: String(params?.message ?? params?.error ?? "codex app-server error") },
      });
    }
  }

  private async handleServerRequest(method: string, id: number | string, params?: JsonRecord): Promise<void> {
    if (method === "item/tool/requestUserInput") {
      const request = params as {
        threadId?: string;
        turnId?: string;
        itemId?: string;
        questions?: AppServerUserInputQuestion[];
      };
      const result = await this.opts.onRequestUserInput({
        requestID: String(id),
        threadID: String(request.threadId ?? ""),
        providerTurnID: String(request.turnId ?? ""),
        providerItemID: String(request.itemId ?? `request_user_input:${id}`),
        questions: Array.isArray(request.questions) ? request.questions : [],
      });
      this.respond(id, result);
      return;
    }
    if (method === "item/commandExecution/requestApproval") {
      this.respond(id, { decision: "accept" });
      return;
    }
    if (method === "item/fileChange/requestApproval") {
      this.respond(id, { decision: "accept" });
      return;
    }
    if (method === "applyPatchApproval" || method === "execCommandApproval") {
      this.respond(id, { decision: "approved" });
      return;
    }
    this.respond(id, {});
  }

  private interruptProviderTurn(
    control: { threadID: string; interruptSent: boolean },
    turnID: string,
  ): void {
    if (!turnID || control.interruptSent) return;
    control.interruptSent = true;
    try {
      void this.request("turn/interrupt", {
        threadId: control.threadID,
        turnId: turnID,
      }).then(() => {
        providerControlTotal.labels("interrupt", "sent").inc();
      }).catch((err) => {
        providerControlTotal.labels("interrupt", "failed").inc();
        providerErrorTotal.labels("interrupt").inc();
        console.error("codex app-server turn/interrupt failed after Stop terminal was emitted:", err);
      });
    } catch (err) {
      providerControlTotal.labels("interrupt", "failed").inc();
      providerErrorTotal.labels("interrupt").inc();
      console.error("codex app-server turn/interrupt failed; continuing with durable Stop terminal:", err);
    }
    if (this.activeTurnControl === control) {
      this.activeTurnControl = null;
      this.activeProviderTurnID = null;
    }
  }
}

function turnIDFromTurnStartResponse(result: unknown): string {
  if (!result || typeof result !== "object") return "";
  const turn = (result as JsonRecord).turn;
  if (!turn || typeof turn !== "object") return "";
  const id = (turn as JsonRecord).id;
  return typeof id === "string" ? id : "";
}

export function appServerItemToCodexItem(item: JsonRecord): JsonRecord {
  const type = item.type;
  if (type === "agentMessage") {
    return { id: item.id, type: "agent_message", text: item.text };
  }
  if (type === "reasoning") {
    const summary = Array.isArray(item.summary) ? item.summary.join("\n") : undefined;
    const content = Array.isArray(item.content) ? item.content.join("\n") : undefined;
    return { id: item.id, type: "reasoning", text: content || summary };
  }
  if (type === "commandExecution") {
    return {
      id: item.id,
      type: "command_execution",
      command: item.command,
      cwd: item.cwd,
      process_id: item.processId,
      source: item.source,
      aggregated_output: item.aggregatedOutput ?? "",
      exit_code: item.exitCode ?? undefined,
      status: codexStatus(item.status),
      duration_ms: item.durationMs,
    };
  }
  if (type === "fileChange") {
    return { id: item.id, type: "file_change", changes: item.changes, status: item.status };
  }
  if (type === "mcpToolCall") {
    return {
      id: item.id,
      type: "mcp_tool_call",
      server: item.server,
      tool: item.tool,
      arguments: item.arguments,
      result: item.result,
      error: item.error,
      status: codexStatus(item.status),
    };
  }
  if (type === "webSearch") {
    return { id: item.id, type: "web_search", query: item.query };
  }
  return { ...item, type: typeof type === "string" ? type : "item" };
}

function codexStatus(status: unknown): string {
  if (status === "running") return "in_progress";
  if (status === "succeeded" || status === "completed") return "completed";
  if (status === "failed") return "failed";
  return typeof status === "string" ? status : "completed";
}
