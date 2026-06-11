import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createRequire } from "node:module";
import { createInterface } from "node:readline";
import type { ThreadOptions } from "@openai/codex-sdk";

import type { CodexEvent } from "./sessionEvents.js";
import { providerControlTotal, providerErrorTotal, unmappedProviderEventTotal } from "./metrics.js";

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
  onRuntimeContextWindowObserved?: (tokens: number) => void;
  // onIdleBackgroundItem surfaces item lifecycle notifications for
  // background (unified-exec) command items that arrive while NO turn is
  // active — a background shell finishing after its turn ended. Without
  // this hook those notifications were silently dropped at the
  // active-queue guard, so the shell_task.exited never published and the
  // background-task wake never registered (the codex half of the
  // park/re-invoke/fold contract; see claude's run_in_background parity).
  onIdleBackgroundItem?: (event: CodexEvent) => void;
};

type PendingRequest = {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
};

type QueuedEvent =
  | { kind: "event"; event: CodexEvent }
  | { kind: "error"; error: Error };

type CodexUsage = {
  input_tokens: number;
  cached_input_tokens: number;
  output_tokens: number;
  reasoning_output_tokens: number;
  total_tokens: number;
};

type ObservedCodexUsage = {
  usage: CodexUsage;
  firstUpdatedAtMs: number;
  lastUpdatedAtMs: number;
  updateCount: number;
};

type CodexUsageObservation = {
  provider_turn_id: string;
  usage_source: "turn.usage" | "turn.tokenUsage" | "thread.tokenUsage.updated" | "missing";
  terminal_had_usage: boolean;
  terminal_had_token_usage: boolean;
  cached_usage_available: boolean;
  update_count: number;
  event_at?: string;
  terminal_at?: string;
  first_update_at?: string;
  last_update_at?: string;
  last_update_age_ms?: number;
};

function abortError(message = "turn interrupted"): Error {
  const err = new Error(message);
  err.name = "AbortError";
  return err;
}

// Unwrap a Codex app-server JSONRPC `error` notification's params into a
// readable string. params?.error is often a structured `{code, type, message,
// param}` object (e.g. token_expired); coercing it with String() yielded
// "[object Object]" in the durable turn.failed payload, hiding the real cause.
function codexErrorMessage(params: JsonRecord | undefined): string {
  const candidate = params?.error ?? params?.message;
  if (typeof candidate === "string" && candidate.length > 0) return candidate;
  if (candidate && typeof candidate === "object") {
    const message = (candidate as { message?: unknown }).message;
    if (typeof message === "string" && message.length > 0) return message;
    try {
      return JSON.stringify(candidate);
    } catch {
      // fall through to generic
    }
  }
  return "codex app-server error";
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
  private readonly latestUsageByProviderTurnID = new Map<string, ObservedCodexUsage>();
  private reportedContextWindowTokens: number | null = null;
  private readonly compactedProviderTurnIDs = new Set<string>();

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
      this.latestUsageByProviderTurnID.clear();
      this.compactedProviderTurnIDs.clear();
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
    // Count any notification handleNotification recognizes no branch for.
    // Checked before the queue guard so unhandled provider notifications
    // surface in metrics regardless of active-turn state.
    const KNOWN_NOTIFICATION =
      method === "turn/completed" ||
      method === "thread/tokenUsage/updated" ||
      method === "thread/compacted" ||
      method === "item/started" ||
      method === "item/completed" ||
      method === "item/updated" ||
      method === "item/commandExecution/outputDelta" ||
      method === "error";
    if (!KNOWN_NOTIFICATION) {
      unmappedProviderEventTotal.labels(method, "none").inc();
      return;
    }
    if (method === "thread/compacted") {
      const event = codexContextCompactedEvent(params);
      if (!event) {
        unmappedProviderEventTotal.labels(method, "invalid_payload").inc();
        return;
      }
      const queue = this.activeQueue;
      if (!queue) {
        unmappedProviderEventTotal.labels(method, "no_active_turn").inc();
        return;
      }
      if (this.rememberContextCompaction(event)) queue.push({ kind: "event", event });
      return;
    }
    const queue = this.activeQueue;
    if (!queue) {
      if (
        (method === "item/started" || method === "item/completed" || method === "item/updated") &&
        isCodexContextCompactionItem(params?.item)
      ) {
        unmappedProviderEventTotal.labels(method, "context_compaction_no_active_turn").inc();
      }
      if (method === "item/completed" || method === "item/updated") {
        const item = params?.item;
        if (
          item &&
          typeof item === "object" &&
          (item as JsonRecord).type === "command_execution"
        ) {
          const codexItem = appServerItemToCodexItem(item as JsonRecord);
          const itemID = typeof codexItem.id === "string" ? codexItem.id : "";
          if (itemID) this.itemsByID.set(itemID, codexItem);
          this.opts.onIdleBackgroundItem?.({
            type: method === "item/completed" ? "item.completed" : "item.updated",
            item: codexItem,
          } as CodexEvent);
        }
      }
      return;
    }
    if (method === "turn/completed") {
      const turn = params?.turn;
      const providerTurnID =
        turn && typeof turn === "object" && typeof (turn as JsonRecord).id === "string"
          ? (turn as JsonRecord).id as string
          : this.activeProviderTurnID ?? "";
      const status =
        turn && typeof turn === "object" && typeof (turn as JsonRecord).status === "string"
          ? (turn as JsonRecord).status
          : "";
      const turnUsage = appServerUsageFromValue((turn as JsonRecord | undefined)?.usage);
      const turnTokenUsage = appServerUsageFromValue((turn as JsonRecord | undefined)?.tokenUsage);
      const cachedUsage = this.latestUsageByProviderTurnID.get(providerTurnID);
      const usage =
        turnUsage ??
        turnTokenUsage ??
        cachedUsage?.usage;
      const terminalAtMs = Date.now();
      const usageObservation = usageObservationForTerminal(
        providerTurnID,
        {
          turnUsage,
          turnTokenUsage,
          cachedUsage,
        },
        terminalAtMs,
      );
      if (providerTurnID) this.latestUsageByProviderTurnID.delete(providerTurnID);
      queue.push({
        kind: "event",
        event: {
          type:
            status === "interrupted"
              ? "turn.interrupted"
              : status === "failed"
                ? "turn.failed"
                : "turn.completed",
          ...(usage ? { usage } : {}),
          usage_observation: usageObservation,
        },
      });
      return;
    }
    if (method === "thread/tokenUsage/updated") {
      const providerTurnID = typeof params?.turnId === "string" ? params.turnId : "";
      this.maybeReportContextWindow(params?.tokenUsage);
      const usage = appServerUsageFromValue(params?.tokenUsage);
      if (providerTurnID && usage) {
        const nowMs = Date.now();
        const existing = this.latestUsageByProviderTurnID.get(providerTurnID);
        const observed = {
          usage,
          firstUpdatedAtMs: existing?.firstUpdatedAtMs ?? nowMs,
          lastUpdatedAtMs: nowMs,
          updateCount: (existing?.updateCount ?? 0) + 1,
        };
        this.latestUsageByProviderTurnID.set(providerTurnID, observed);
        if (!this.activeProviderTurnID) this.activeProviderTurnID = providerTurnID;
        queue.push({
          kind: "event",
          event: {
            type: "turn.usage",
            id: `${providerTurnID}:usage:${observed.updateCount}`,
            usage,
            usage_observation: usageObservationForUpdate(providerTurnID, observed),
          },
        });
      }
      return;
    }
    if (method === "item/started" || method === "item/completed" || method === "item/updated") {
      const item = params?.item;
      if (!item || typeof item !== "object") return;
      if (isCodexContextCompactionItem(item)) {
        const event = codexContextCompactedEvent(params, item as JsonRecord);
        if (!event) {
          unmappedProviderEventTotal.labels(method, "context_compaction_invalid_payload").inc();
          return;
        }
        if (this.rememberContextCompaction(event)) queue.push({ kind: "event", event });
        return;
      }
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
        event: { type: "error", message: codexErrorMessage(params) },
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

  private maybeReportContextWindow(tokenUsage: unknown): void {
    if (!tokenUsage || typeof tokenUsage !== "object") return;
    const tokens = finiteNumber((tokenUsage as JsonRecord).modelContextWindow);
    if (tokens === undefined || tokens <= 0) return;
    const normalized = Math.floor(tokens);
    if (this.reportedContextWindowTokens === null) {
      this.reportedContextWindowTokens = normalized;
      this.opts.onRuntimeContextWindowObserved?.(normalized);
      return;
    }
    if (this.reportedContextWindowTokens !== normalized) {
      console.warn(
        "codex app-server reported a different model context window; keeping first observed value:",
        JSON.stringify({
          first_context_window_tokens: this.reportedContextWindowTokens,
          later_context_window_tokens: normalized,
        }),
      );
    }
  }

  private rememberContextCompaction(event: CodexEvent): boolean {
    const providerTurnID = typeof event.turn_id === "string" ? event.turn_id : "";
    if (!providerTurnID) return true;
    if (this.compactedProviderTurnIDs.has(providerTurnID)) return false;
    this.compactedProviderTurnIDs.add(providerTurnID);
    return true;
  }
}

function turnIDFromTurnStartResponse(result: unknown): string {
  if (!result || typeof result !== "object") return "";
  const turn = (result as JsonRecord).turn;
  if (!turn || typeof turn !== "object") return "";
  const id = (turn as JsonRecord).id;
  return typeof id === "string" ? id : "";
}

function appServerUsageFromValue(value: unknown): CodexUsage | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as JsonRecord;
  const source = record.total && typeof record.total === "object"
    ? record.total as JsonRecord
    : record;
  const inputTokens = finiteNumber(source.inputTokens) ?? finiteNumber(source.input_tokens);
  const cachedInputTokens = finiteNumber(source.cachedInputTokens) ?? finiteNumber(source.cached_input_tokens) ?? 0;
  const outputTokens = finiteNumber(source.outputTokens) ?? finiteNumber(source.output_tokens);
  const reasoningOutputTokens =
    finiteNumber(source.reasoningOutputTokens) ?? finiteNumber(source.reasoning_output_tokens) ?? 0;
  const totalTokens =
    finiteNumber(source.totalTokens) ??
    finiteNumber(source.total_tokens) ??
    ((inputTokens ?? 0) + cachedInputTokens + (outputTokens ?? 0));
  if (inputTokens === undefined || outputTokens === undefined) return undefined;
  return {
    input_tokens: inputTokens,
    cached_input_tokens: cachedInputTokens,
    output_tokens: outputTokens,
    reasoning_output_tokens: reasoningOutputTokens,
    total_tokens: totalTokens,
  };
}

function usageObservationForTerminal(
  providerTurnID: string,
  values: {
    turnUsage?: CodexUsage;
    turnTokenUsage?: CodexUsage;
    cachedUsage?: ObservedCodexUsage;
  },
  terminalAtMs: number,
): CodexUsageObservation {
  const usageSource =
    values.turnUsage
      ? "turn.usage"
      : values.turnTokenUsage
        ? "turn.tokenUsage"
        : values.cachedUsage
          ? "thread.tokenUsage.updated"
          : "missing";
  const observation: CodexUsageObservation = {
    provider_turn_id: providerTurnID,
    usage_source: usageSource,
    terminal_had_usage: Boolean(values.turnUsage),
    terminal_had_token_usage: Boolean(values.turnTokenUsage),
    cached_usage_available: Boolean(values.cachedUsage),
    update_count: values.cachedUsage?.updateCount ?? 0,
    terminal_at: new Date(terminalAtMs).toISOString(),
  };
  if (values.cachedUsage) {
    observation.first_update_at = new Date(values.cachedUsage.firstUpdatedAtMs).toISOString();
    observation.last_update_at = new Date(values.cachedUsage.lastUpdatedAtMs).toISOString();
    observation.last_update_age_ms = Math.max(0, terminalAtMs - values.cachedUsage.lastUpdatedAtMs);
  }
  return observation;
}

function usageObservationForUpdate(
  providerTurnID: string,
  observed: ObservedCodexUsage,
): CodexUsageObservation {
  const updateAt = new Date(observed.lastUpdatedAtMs).toISOString();
  return {
    provider_turn_id: providerTurnID,
    usage_source: "thread.tokenUsage.updated",
    terminal_had_usage: false,
    terminal_had_token_usage: false,
    cached_usage_available: true,
    update_count: observed.updateCount,
    event_at: updateAt,
    first_update_at: new Date(observed.firstUpdatedAtMs).toISOString(),
    last_update_at: updateAt,
  };
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function isCodexContextCompactionItem(item: unknown): boolean {
  if (!item || typeof item !== "object") return false;
  const type = (item as JsonRecord).type;
  return type === "contextCompaction" || type === "context_compaction";
}

function codexContextCompactedEvent(params?: JsonRecord, item?: JsonRecord): CodexEvent | undefined {
  const providerTurnID = nonEmptyString(params?.turnId);
  if (!providerTurnID) return undefined;
  const threadID = nonEmptyString(params?.threadId);
  const providerItemID = nonEmptyString(item?.id);
  const id = providerItemID
    ? `${providerTurnID}:${providerItemID}`
    : `thread/compacted:${providerTurnID}`;
  return {
    type: "context.compacted",
    id,
    ...(threadID ? { thread_id: threadID } : {}),
    turn_id: providerTurnID,
    ...(providerItemID ? { item_id: providerItemID } : {}),
    trigger: "auto",
  };
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
