// Long-lived runner — drives one claude agent process via the SDK for
// the pod's lifetime. The SDK's `query()` takes an async iterable of
// user messages, so we push to a queue as WS clients submit, and the
// SDK iterates events back out. Multi-turn coordination is implicit:
// the SDK serializes turns internally, we just keep feeding it.
//
// Output fan-out per the producer contract:
//   1. For every event, write to Cosmos FIRST (if canonical)
//   2. Then broadcast to WS clients
//   3. If the event contains a ScheduleWakeup, queue a wakeup turn
//
// On error: log and keep running. Single-turn failures shouldn't kill
// the runner; persistent failures will show up via Cosmos write errors
// and the SPA's user can refresh.

import {
  query,
  type Query,
  type SDKMessage,
  type SDKUserMessage,
  type Options,
} from "@anthropic-ai/claude-agent-sdk";
import { randomUUID } from "node:crypto";

import {
  canonicalEventsForClaudeMessage,
  claudeUserMessageText,
  startsClaudeTurn,
} from "./adapters/claude.js";
import type { Config } from "./config.js";
import { CosmosSink, isCanonical, type RunnerEvent } from "./cosmos.js";
import {
  normalizeClientNonce,
  turnEvent,
  turnIDForClientNonce,
  userSubmissionEvents,
  type TankConversationEvent,
} from "./conversation.js";
import { TurnQueue, turnClientNonce, type TurnRecord } from "./turnQueue.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";
import { WSFanout, type ClientFrame } from "./ws.js";

// Pull a single dispatch out as a free function so the producer
// contract — cosmos-first ordering + cosmos-failure suppresses ws — is
// testable without spinning up a Runner (the WSFanout binds to a port
// in its constructor, which makes the full Runner painful to unit-test).
//
// Returns true on a successful end-to-end dispatch; false when the
// canonical write failed and the broadcast was intentionally skipped.
// Callers that don't care about the outcome can ignore it.
interface DispatchSink {
  upsert(message: RunnerEvent & { uuid: string }): Promise<void>;
  create?(message: RunnerEvent & { uuid: string }): Promise<"created" | "exists">;
}
interface DispatchWS {
  broadcastEvent(message: RunnerEvent): void;
}

let tankEventSeq = 0;

function stampTankEvent(message: RunnerEvent): RunnerEvent & { uuid: string } {
  tankEventSeq += 1;
  const now = Date.now();
  const eventID = typeof message.event_id === "string" && message.event_id ? message.event_id : "";
  const uuid = typeof message.uuid === "string" && message.uuid ? message.uuid : eventID || randomUUID();
  const writtenAt = new Date(now).toISOString();
  const tankOrderKey = [
    String(now).padStart(13, "0"),
    String(tankEventSeq).padStart(8, "0"),
    uuid,
  ].join("-");
  return {
    ...message,
    uuid,
    ...(eventID ? { event_id: eventID } : {}),
    tank_event_seq: tankEventSeq,
    tank_order_key: tankOrderKey,
    written_at: writtenAt,
    ...(isTankEvent(message)
      ? {
          order_key:
            typeof message.order_key === "string" && message.order_key ? message.order_key : tankOrderKey,
          sequence: typeof message.sequence === "number" ? message.sequence : tankEventSeq,
          created_at:
            typeof message.created_at === "string" && message.created_at ? message.created_at : writtenAt,
        }
      : {}),
  };
}

export async function dispatch(
  sink: DispatchSink,
  ws: DispatchWS,
  message: RunnerEvent,
): Promise<boolean> {
  const stamped = stampTankEvent(message);
  if (isCanonical(stamped)) {
    try {
      await sink.upsert(stamped);
    } catch (err) {
      console.error("cosmos upsert failed:", err);
      // Don't broadcast a live event we couldn't persist — the SPA's
      // history-replay would then disagree with what it saw live.
      return false;
    }
  }
  ws.broadcastEvent(stamped);
  return true;
}

export async function dispatchCreate(
  sink: DispatchSink,
  ws: DispatchWS,
  message: RunnerEvent,
): Promise<"created" | "exists" | "failed"> {
  const stamped = stampTankEvent(message);
  if (!isCanonical(stamped)) {
    ws.broadcastEvent(stamped);
    return "created";
  }
  try {
    const result = sink.create ? await sink.create(stamped) : (await sink.upsert(stamped), "created");
    if (result === "exists") return "exists";
  } catch (err) {
    console.error("cosmos create failed:", err);
    return "failed";
  }
  ws.broadcastEvent(stamped);
  return "created";
}

function isTankEvent(message: RunnerEvent): message is TankConversationEvent {
  return typeof message.event_id === "string" && typeof message.visibility === "string";
}

export interface PendingTurn {
  turnID: string;
  clientNonce: string;
  text: string;
  started: boolean;
  interrupted: boolean;
  terminalEmitted: boolean;
  queueRecord?: TurnRecord;
  stopLeaseRenewal?: () => void;
}

// AsyncQueue is a one-writer-many-no-readers queue that yields each
// pushed item exactly once. The SDK consumes this as the prompt source.
class AsyncQueue<T> {
  private readonly items: T[] = [];
  private waiters: ((v: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(v: T): void {
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }

  close(): void {
    this.closed = true;
    for (const w of this.waiters) w({ value: undefined as any, done: true });
    this.waiters = [];
  }

  [Symbol.asyncIterator](): AsyncIterator<T> {
    const self = this;
    return {
      next(): Promise<IteratorResult<T>> {
        if (self.items.length > 0) {
          return Promise.resolve({ value: self.items.shift()!, done: false });
        }
        if (self.closed) {
          return Promise.resolve({ value: undefined as any, done: true });
        }
        return new Promise((resolve) => self.waiters.push(resolve));
      },
    };
  }
}

export class Runner {
  private readonly sink: CosmosSink;
  private readonly ws: WSFanout;
  private readonly turnQueue: TurnQueue;
  private readonly userQueue = new AsyncQueue<SDKUserMessage>();
  private readonly pendingTurns: PendingTurn[] = [];
  private readonly needsInputItemIDs = new Set<string>();
  private activeTurn: PendingTurn | null = null;
  private sdkQuery: Query | null = null;
  private userFrameChain: Promise<void> = Promise.resolve();

  constructor(private readonly cfg: Config) {
    this.sink = new CosmosSink(cfg);
    this.ws = new WSFanout(cfg.wsPort);
    this.turnQueue = new TurnQueue(cfg, "claude");
    this.ws.onMessage((frame) => {
      if (frame.type === "user") {
        this.userFrameChain = this.userFrameChain
          .then(() => this.handleClientFrame(frame))
          .catch((err) => console.error("user frame handling failed:", err));
        return;
      }
      void this.handleClientFrame(frame);
    });
  }

  // Run forever (or until externally aborted). Drives the SDK against
  // the user queue and fans events out to both sinks.
  async run(signal: AbortSignal): Promise<void> {
    const stopPolling = this.startTurnQueuePolling(signal);
    const onAbort = () => {
      this.userQueue.close();
      this.sdkQuery?.interrupt();
    };
    signal.addEventListener("abort", onAbort, { once: true });
    const options: Options = {
      cwd: this.cfg.workspace,
      // The api-proxy injects OAuth from KV when the placeholder bearer
      // is seen — both the SDK and the raw CLI go through this path.
      // Permission bypass matches what k8s/session-config/headless-run.sh
      // used to set via --dangerously-skip-permissions.
      permissionMode: "bypassPermissions",
      // Resume an on-disk JSONL if one exists from a prior process
      // life (e.g., agent-runner restart within the same pod).
      // First boot with no JSONL: no-op.
      continue: true,
      // include_partial_messages keeps the typewriter effect — the SPA
      // renders stream_event deltas live and snapshots to the canonical
      // assistant message when it arrives.
      includePartialMessages: true,
      mcpServers: undefined, // file-mounted via --mcp-config below
      // Bare mode would skip CLAUDE.md / skills / hooks; we want those.
    };

    this.sdkQuery = query({ prompt: this.userQueue, options });
    try {
      for await (const message of this.sdkQuery) {
        if (signal.aborted) break;
        await this.handleEvent(message);
      }
    } catch (err) {
      console.error("SDK query exited with error:", err);
      await this.failActiveQueuedTurn(err);
    } finally {
      signal.removeEventListener("abort", onAbort);
      stopPolling();
      if (signal.aborted) {
        await this.interruptActiveTurn("runner_shutdown");
      }
      this.userQueue.close();
      this.ws.close();
    }
  }

  private async handleEvent(message: SDKMessage): Promise<void> {
    const runnerEvent = message as RunnerEvent;
    const activeTurn = await this.ensureActiveTurn(runnerEvent);

    // 1+2. Dual-sink dispatch (cosmos-first ordering). Extracted so the
    // contract — canonical: cosmos before ws, ws skipped on cosmos
    // failure; live-only: ws only — can be tested without spinning up a
    // Runner.
    await dispatch(this.sink, this.ws, runnerEvent);

    for (const event of canonicalEventsForClaudeMessage(
      this.cfg,
      activeTurn,
      runnerEvent,
      this.needsInputItemIDs,
    )) {
      const dispatched = await dispatch(this.sink, this.ws, event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.interrupted") {
        if (activeTurn) activeTurn.terminalEmitted = true;
        if (dispatched && activeTurn?.queueRecord) {
          await this.markQueuedTurnTerminal(activeTurn, event.type);
        }
      }
    }
    if (runnerEvent.type === "result" && this.activeTurn === activeTurn) {
      this.activeTurn = null;
    }

    // 3. ScheduleWakeup detection
    const wakeup = extractWakeup(message);
    if (wakeup) {
      this.scheduleWakeup(wakeup);
    }
  }

  private async handleClientFrame(frame: ClientFrame): Promise<void> {
    if (frame.type === "user") {
      const text = claudeUserMessageText(frame.message?.content);
      const pendingTurn = await this.recordUserSubmission(text, frame.message, frame.client_nonce);
      if (!pendingTurn) return;
      this.pendingTurns.push(pendingTurn);

      // Hand to the SDK iterator only after Tank owns the durable user
      // message. The SDK enforces "wait for result before next message"
      // internally; we don't need to gate.
      this.userQueue.push({
        type: "user",
        session_id: "",
        message: frame.message,
        parent_tool_use_id: null,
      } as unknown as SDKUserMessage);
    } else if (frame.type === "interrupt") {
      await this.interruptActiveTurn("client_interrupt");
      this.sdkQuery?.interrupt();
    }
  }

  private startTurnQueuePolling(signal: AbortSignal): () => void {
    let stopped = false;
    const stop = () => {
      stopped = true;
    };
    void (async () => {
      while (!stopped && !signal.aborted) {
        try {
          const record = await this.turnQueue.claimNext();
          if (record) {
            await this.acceptQueuedTurn(record);
            continue;
          }
        } catch (err) {
          console.error("turn queue poll failed:", err);
        }
        await sleep(this.cfg.turnQueuePollMs, signal, () => stopped);
      }
    })().catch((err) => console.error("turn queue poller crashed:", err));
    return stop;
  }

  private async acceptQueuedTurn(record: TurnRecord): Promise<void> {
    const clientNonce = turnClientNonce(record);
    if (await this.finalizeQueuedRecordIfAlreadyTerminal(record, clientNonce)) {
      return;
    }
    if (this.turnQueue.attemptsExceeded(record)) {
      await this.failQueuedRecord(
        record,
        new Error(`turn queue exceeded ${record.attempt_count ?? "unknown"} claim attempts`),
      );
      return;
    }
    const pendingTurn = await this.recordUserSubmission(
      record.prompt,
      { role: "user", content: record.prompt },
      clientNonce,
      record,
    );
    if (!pendingTurn) {
      await this.turnQueue.markFailed(record, new Error("queued turn was not accepted"));
      return;
    }
    pendingTurn.stopLeaseRenewal = this.turnQueue.startLeaseRenewal(record);
    this.pendingTurns.push(pendingTurn);
    this.userQueue.push({
      type: "user",
      session_id: "",
      message: { role: "user", content: record.prompt },
      parent_tool_use_id: null,
    } as unknown as SDKUserMessage);
  }

  private async recordUserSubmission(
    text: string,
    message: unknown,
    rawClientNonce: unknown,
    queueRecord?: TurnRecord,
  ): Promise<PendingTurn | null> {
    const clientNonce = normalizeClientNonce(rawClientNonce);
    if (!clientNonce) {
      await dispatch(this.sink, this.ws, {
        type: "error",
        message: "client_nonce is required for user submissions",
      });
      return null;
    }
    const { turnID, userMessage, turnSubmitted } = userSubmissionEvents({
      sessionID: this.cfg.sessionId,
      clientNonce,
      text,
      message,
      runtime: "claude",
    });
    const userResult = await dispatchCreate(this.sink, this.ws, userMessage);
    if (userResult === "failed") return null;
    const submittedResult = await dispatchCreate(this.sink, this.ws, turnSubmitted);
    if (submittedResult === "failed") return null;
    if (submittedResult === "exists" && !queueRecord) return null;
    return {
      turnID,
      clientNonce,
      text,
      started: false,
      interrupted: false,
      terminalEmitted: false,
      ...(queueRecord ? { queueRecord } : {}),
    };
  }

  private async ensureActiveTurn(event: RunnerEvent): Promise<PendingTurn | null> {
    if (!this.activeTurn && this.pendingTurns.length > 0 && startsClaudeTurn(event)) {
      this.activeTurn = this.pendingTurns.shift() ?? null;
      if (this.activeTurn && !this.activeTurn.started) {
        this.activeTurn.started = true;
        await dispatch(
          this.sink,
          this.ws,
          turnEvent({
            sessionID: this.cfg.sessionId,
            turnID: this.activeTurn.turnID,
            clientNonce: this.activeTurn.clientNonce,
            source: "claude",
            type: "turn.started",
          }),
        );
      }
    }
    return this.activeTurn;
  }

  private async interruptActiveTurn(reason: "client_interrupt" | "runner_shutdown"): Promise<void> {
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (!turn || turn.terminalEmitted) return;
    turn.interrupted = true;
    turn.terminalEmitted = true;
    const dispatched = await dispatch(
      this.sink,
      this.ws,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.interrupted",
        reason,
      }),
    );
    if (dispatched && turn.queueRecord) {
      await this.markQueuedTurnTerminal(turn, "turn.interrupted");
    }
  }

  private async markQueuedTurnTerminal(
    turn: PendingTurn,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ): Promise<void> {
    if (!turn.queueRecord) return;
    const record = turn.queueRecord;
    turn.stopLeaseRenewal?.();
    turn.stopLeaseRenewal = undefined;
    turn.queueRecord = undefined;
    try {
      if (type === "turn.completed") {
        await this.turnQueue.markCompleted(record);
      } else {
        await this.turnQueue.markFailed(record, new Error(type));
      }
    } catch (err) {
      console.error("turn queue terminal mark failed:", err);
    }
  }

  private async failActiveQueuedTurn(err: unknown): Promise<void> {
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (!turn?.queueRecord) return;
    if (!turn.terminalEmitted) {
      turn.terminalEmitted = true;
      const dispatched = await dispatch(
        this.sink,
        this.ws,
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "claude",
          type: "turn.failed",
          reason: "provider_failure",
          error: err instanceof Error ? err.message : String(err),
        }),
      );
      if (!dispatched) return;
    }
    await this.markQueuedTurnTerminal(turn, "turn.failed").catch((markErr) =>
      console.error("turn queue failure mark failed:", markErr, "original:", err),
    );
  }

  private scheduleWakeup(req: WakeupRequest): void {
    void this.enqueueWakeup(req).catch((err) =>
      console.error("schedule wakeup failed:", err),
    );
  }

  private async enqueueWakeup(req: WakeupRequest): Promise<void> {
    const delayMs = Math.max(0, req.delayMs);
    await this.turnQueue.enqueueDelayed({
      prompt: req.prompt,
      clientNonce: `schedule_wakeup-${randomUUID()}`,
      availableAt: new Date(Date.now() + delayMs).toISOString(),
    });
  }

  private async finalizeQueuedRecordIfAlreadyTerminal(
    record: TurnRecord,
    clientNonce: string,
  ): Promise<boolean> {
    const terminal = await this.sink.findTurnTerminal(turnIDForClientNonce(clientNonce));
    if (!terminal) return false;
    if (terminal.type === "turn.completed") {
      await this.turnQueue.markCompleted(record);
    } else {
      await this.turnQueue.markFailed(record, new Error(String(terminal.type)));
    }
    return true;
  }

  private async failQueuedRecord(record: TurnRecord, err: unknown): Promise<void> {
    const pendingTurn = await this.recordUserSubmission(
      record.prompt,
      { role: "user", content: record.prompt },
      turnClientNonce(record),
      record,
    );
    if (!pendingTurn) {
      await this.turnQueue.markFailed(record, err);
      return;
    }
    pendingTurn.stopLeaseRenewal = this.turnQueue.startLeaseRenewal(record);
    pendingTurn.terminalEmitted = true;
    const dispatched = await dispatch(
      this.sink,
      this.ws,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: pendingTurn.turnID,
        clientNonce: pendingTurn.clientNonce,
        source: "claude",
        type: "turn.failed",
        reason: "turn_queue_attempts_exceeded",
        error: err instanceof Error ? err.message : String(err),
      }),
    );
    if (dispatched) {
      await this.markQueuedTurnTerminal(pendingTurn, "turn.failed");
    }
  }
}

function sleep(ms: number, signal: AbortSignal, stopped: () => boolean): Promise<void> {
  if (signal.aborted || stopped()) return Promise.resolve();
  return new Promise((resolve) => {
    const timeout = setTimeout(resolve, Math.max(100, ms));
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timeout);
        resolve();
      },
      { once: true },
    );
  });
}
