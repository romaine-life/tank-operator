// Long-lived runner — drives one claude agent process via the SDK for
// the pod's lifetime. The SDK's `query()` takes an async iterable of
// user messages, so we push durable session commands into it. Multi-turn
// coordination is implicit: the SDK serializes turns internally, we just
// keep feeding it.
//
// Output contract: adapters/claude.ts converts raw Claude SDK messages
// into Tank conversation events; the runner stamps and publishes those
// Tank events on the session bus. Raw provider events never reach the
// bus. Boundary events (user_message.created, turn.submitted) are owned
// by the backend (handlers_turns.go) — the runner does not republish them.
// ScheduleWakeup is a pod-local setTimeout that re-enqueues a submit_turn
// command when the timer fires.
//
// On error: log and keep running. Single-turn failures shouldn't kill the
// runner; persistent failures will surface via session-bus publish errors.

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
  startsClaudeTurn,
  type ClaudeProviderEvent,
} from "./adapters/claude.js";
import type { Config } from "./config.js";
import { SessionEventSink, type StampedTankEvent } from "./sessionEvents.js";
import {
  isDurableTankConversationEvent,
  normalizeClientNonce,
  type TankConversationEvent,
} from "../../runner-shared/conversation.js";
import {
  stampTankEvent,
  turnEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import {
  SessionCommandBus,
  isInputReplyCommand,
  isInterruptCommand,
  commandClientNonce,
  type SessionCommandRecord,
} from "./sessionCommands.js";
import {
  commandsConsumedTotal,
  natsPublishFailureTotal,
  pendingWakeupsGauge,
  providerErrorTotal,
  recordTurnStart,
  recordTurnTerminal,
} from "./metrics.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";

// Pull a single dispatch out as a free function so the session-bus publish
// contract is testable without spinning up a Runner. The sink only accepts
// stamped Tank conversation events; the durable filter here matches the
// persister-side ValidateEventMap rules.
//
// Returns true on a successful end-to-end dispatch (or when the event was
// non-durable and intentionally dropped); false when the publish failed.
interface DispatchSink {
  upsert(message: StampedTankEvent): Promise<void>;
}

export async function dispatch(
  sink: DispatchSink,
  message: TankConversationEvent,
): Promise<boolean> {
  const stamped = stampTankEvent(message);
  if (!isDurableTankConversationEvent(stamped)) {
    return true;
  }
  try {
    await sink.upsert(stamped);
  } catch (err) {
    console.error("session bus publish failed:", err);
    natsPublishFailureTotal.inc();
    return false;
  }
  return true;
}

export interface PendingTurn {
  turnID: string;
  clientNonce: string;
  text: string;
  started: boolean;
  interrupted: boolean;
  terminalEmitted: boolean;
  commandRecord?: SessionCommandRecord;
  stopCommandHeartbeat?: () => void;
}

interface PendingInputReply {
  record: SessionCommandRecord;
  stopCommandHeartbeat?: () => void;
}

type InterruptOutcome = "interrupted" | "not_found" | "publish_failed";

export function inputReplyTargetProviderItemID(record: SessionCommandRecord): string {
  return String(record.target_provider_item_id ?? "").trim();
}

export function inputReplyText(record: SessionCommandRecord): string {
  return String(record.input_reply ?? "").trim();
}

export function buildInputReplyMessage(providerItemID: string, text: string): SDKUserMessage {
  return {
    type: "user",
    session_id: "",
    message: {
      role: "user",
      content: [
        {
          type: "tool_result",
          tool_use_id: providerItemID,
          content: text,
        },
      ],
    },
    parent_tool_use_id: providerItemID,
  } as unknown as SDKUserMessage;
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
  private readonly sink: SessionEventSink;
  private readonly commandBus: SessionCommandBus;
  private readonly userQueue = new AsyncQueue<SDKUserMessage>();
  private readonly pendingTurns: PendingTurn[] = [];
  private readonly needsInputProviderItemIDs = new Set<string>();
  private readonly pendingInputReplies = new Map<string, PendingInputReply>();
  private activeTurn: PendingTurn | null = null;
  private sdkQuery: Query | null = null;

  constructor(private readonly cfg: Config) {
    this.sink = new SessionEventSink(cfg);
    this.commandBus = new SessionCommandBus(cfg, "claude");
  }

  // Run forever (or until externally aborted). Drives the SDK against
  // the user queue and fans events out to both sinks.
  async run(signal: AbortSignal): Promise<void> {
    const stopConsumer = this.startCommandConsumer(signal);
    const onAbort = () => {
      this.userQueue.close();
      this.sdkQuery?.interrupt();
    };
    signal.addEventListener("abort", onAbort, { once: true });
    const options: Options = {
      cwd: this.cfg.workspace,
      // The api-proxy injects OAuth from KV when the placeholder bearer
      // is seen — both the SDK and the raw CLI go through this path.
      // Match the browser chat's permissive editing mode.
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
      providerErrorTotal.labels("query").inc();
      await this.failActiveCommandTurn(err);
    } finally {
      signal.removeEventListener("abort", onAbort);
      stopConsumer();
      if (signal.aborted) {
        await this.interruptActiveTurn("runner_shutdown");
      }
      this.userQueue.close();
    }
  }

  private async handleEvent(message: SDKMessage): Promise<void> {
    // Claude SDK events are adapter inputs, not bus content. The adapter
    // converts them into Tank conversation events; only those reach the
    // durable session bus.
    const providerEvent = message as ClaudeProviderEvent;
    const activeTurn = await this.ensureActiveTurn(providerEvent);

    for (const event of canonicalEventsForClaudeMessage(
      this.cfg,
      activeTurn,
      providerEvent,
      this.needsInputProviderItemIDs,
    )) {
      const dispatched = await dispatch(this.sink, event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.interrupted") {
        if (dispatched && activeTurn) {
          activeTurn.terminalEmitted = true;
          if (activeTurn.commandRecord) {
            await this.markCommandTerminal(activeTurn, event.type);
          }
        }
      }
      if (dispatched && event.type === "tool.approval_resolved" && event.provider_item_id) {
        await this.markInputReplyCompleted(event.provider_item_id as string);
      }
    }
    if (providerEvent.type === "result" && this.activeTurn === activeTurn) {
      this.activeTurn = null;
    }

    const wakeup = extractWakeup(message);
    if (wakeup) {
      this.scheduleWakeup(wakeup);
    }
  }

  private startCommandConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startCommandConsumer(async (record) => {
        if (isInputReplyCommand(record)) {
          await this.acceptInputReply(record);
          return;
        }
        if (isInterruptCommand(record)) {
          await this.acceptInterrupt(record);
          return;
        }
        await this.acceptCommandTurn(record);
      }, signal)
      .then((stop) => {
        stopConsumer = stop;
      })
      .catch((err) => console.error("session bus command consumer crashed:", err));
    return () => {
      void stopConsumer?.();
    };
  }

  private async acceptCommandTurn(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("submit_turn", "accepted").inc();
    const clientNonce = commandClientNonce(record);
    const prompt = String(record.prompt ?? "").trim();
    if (!prompt) {
      commandsConsumedTotal.labels("submit_turn", "invalid").inc();
      await this.commandBus.markFailed(record, new Error("submit command missing prompt"));
      return;
    }
    if (await this.finalizeCommandIfAlreadyTerminal(record, clientNonce)) {
      commandsConsumedTotal.labels("submit_turn", "already_terminal").inc();
      return;
    }
    if (this.commandBus.attemptsExceeded(record)) {
      commandsConsumedTotal.labels("submit_turn", "attempts_exceeded").inc();
      await this.failCommandRecord(
        record,
        new Error(`session command exceeded ${record.attempt_count ?? "unknown"} claim attempts`),
      );
      return;
    }
    const pendingTurn = this.acceptTurn(prompt, clientNonce, record);
    if (!pendingTurn) {
      commandsConsumedTotal.labels("submit_turn", "invalid").inc();
      await this.commandBus.markFailed(record, new Error("session command was not accepted"));
      return;
    }
    pendingTurn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(record);
    this.pendingTurns.push(pendingTurn);
    this.userQueue.push({
      type: "user",
      session_id: "",
      message: { role: "user", content: prompt },
      parent_tool_use_id: null,
    } as unknown as SDKUserMessage);
  }

  private async acceptInterrupt(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("interrupt_turn", "accepted").inc();
    const outcome = await this.interruptActiveTurn(
      "client_interrupt",
      record.target_turn_id || record.client_nonce,
    );
    if (outcome === "interrupted") {
      try {
        this.sdkQuery?.interrupt();
      } catch (err) {
        providerErrorTotal.labels("interrupt").inc();
        throw err;
      }
      await this.commandBus.markCompleted(record);
      return;
    }
    if (outcome === "publish_failed") {
      await this.commandBus.markFailed(record, new Error("interrupt event publish failed"));
      return;
    }
    await this.commandBus.markCompleted(record);
  }

  private async acceptInputReply(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("input_reply", "accepted").inc();
    const targetProviderItemID = inputReplyTargetProviderItemID(record);
    const text = inputReplyText(record);
    if (!targetProviderItemID || !text) {
      commandsConsumedTotal.labels("input_reply", "invalid").inc();
      await this.commandBus.markFailed(record, new Error("input reply missing target or text"));
      return;
    }
    if (!this.activeTurn || !this.turnMatchesTarget(this.activeTurn, record.target_turn_id || record.client_nonce)) {
      commandsConsumedTotal.labels("input_reply", "no_active_turn").inc();
      await this.commandBus.markFailed(record, new Error("input reply target turn is not active"));
      return;
    }
    if (!this.needsInputProviderItemIDs.has(targetProviderItemID)) {
      commandsConsumedTotal.labels("input_reply", "not_waiting_for_input").inc();
      await this.commandBus.markFailed(record, new Error("input reply target is not waiting for input"));
      return;
    }
    if (this.pendingInputReplies.has(targetProviderItemID)) {
      commandsConsumedTotal.labels("input_reply", "duplicate").inc();
      await this.commandBus.markFailed(record, new Error("input reply already pending for target"));
      return;
    }

    const pending: PendingInputReply = {
      record,
      stopCommandHeartbeat: this.commandBus.startCommandHeartbeat(record),
    };
    this.pendingInputReplies.set(targetProviderItemID, pending);
    this.userQueue.push(buildInputReplyMessage(targetProviderItemID, text));
  }

  // acceptTurn normalizes the client nonce and assembles the in-memory
  // pending-turn record. Boundary events (user_message.created,
  // turn.submitted) are durably written by the backend when the user
  // POSTed the turn — the runner does not republish them. Returns null
  // when the command payload is malformed (the caller marks failed).
  private acceptTurn(
    text: string,
    rawClientNonce: unknown,
    commandRecord?: SessionCommandRecord,
  ): PendingTurn | null {
    const clientNonce = normalizeClientNonce(rawClientNonce);
    if (!clientNonce) {
      console.error("claude command rejected: client_nonce is required");
      return null;
    }
    return {
      turnID: turnIDForClientNonce(clientNonce),
      clientNonce,
      text,
      started: false,
      interrupted: false,
      terminalEmitted: false,
      ...(commandRecord ? { commandRecord } : {}),
    };
  }

  private async ensureActiveTurn(event: ClaudeProviderEvent): Promise<PendingTurn | null> {
    if (!this.activeTurn && this.pendingTurns.length > 0 && startsClaudeTurn(event)) {
      this.activeTurn = this.pendingTurns.shift() ?? null;
      if (this.activeTurn && !this.activeTurn.started) {
        this.activeTurn.started = true;
        recordTurnStart(this.activeTurn.turnID);
        await dispatch(
          this.sink,
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

  private async interruptActiveTurn(
    reason: "client_interrupt" | "runner_shutdown",
    targetTurnID = "",
  ): Promise<InterruptOutcome> {
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (!turn || turn.terminalEmitted) return "not_found";
    if (!this.turnMatchesTarget(turn, targetTurnID)) {
      return "not_found";
    }
    turn.interrupted = true;
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.interrupted",
        reason,
      }),
    );
    if (!dispatched) {
      turn.interrupted = false;
      return "publish_failed";
    }
    turn.terminalEmitted = true;
    if (turn.commandRecord) {
      await this.markCommandTerminal(turn, "turn.interrupted");
    }
    return "interrupted";
  }

  private turnMatchesTarget(turn: Pick<PendingTurn, "turnID" | "clientNonce">, targetTurnID = ""): boolean {
    return !targetTurnID || targetTurnID === turn.turnID || targetTurnID === turn.clientNonce;
  }

  private async markCommandTerminal(
    turn: PendingTurn,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ): Promise<void> {
    const outcome = type === "turn.completed" ? "completed" : type === "turn.failed" ? "failed" : "interrupted";
    recordTurnTerminal(turn.turnID, outcome);
    await this.failPendingInputRepliesForTurn(turn, new Error(type));
    if (!turn.commandRecord) return;
    const record = turn.commandRecord;
    turn.stopCommandHeartbeat?.();
    turn.stopCommandHeartbeat = undefined;
    turn.commandRecord = undefined;
    try {
      await this.commandBus.markCompleted(record);
    } catch (err) {
      console.error("session command terminal mark failed:", err);
    }
  }

  private async markInputReplyCompleted(providerItemID: string): Promise<void> {
    const pending = this.pendingInputReplies.get(providerItemID);
    if (!pending) return;
    this.pendingInputReplies.delete(providerItemID);
    pending.stopCommandHeartbeat?.();
    try {
      await this.commandBus.markCompleted(pending.record);
    } catch (err) {
      console.error("input reply terminal mark failed:", err);
    }
  }

  private async failPendingInputRepliesForTurn(
    turn: Pick<PendingTurn, "turnID" | "clientNonce">,
    err: unknown,
  ): Promise<void> {
    for (const [providerItemID, pending] of [...this.pendingInputReplies.entries()]) {
      if (!this.turnMatchesTarget(turn, pending.record.target_turn_id || pending.record.client_nonce)) {
        continue;
      }
      this.pendingInputReplies.delete(providerItemID);
      pending.stopCommandHeartbeat?.();
      try {
        await this.commandBus.markFailed(pending.record, err);
      } catch (markErr) {
        console.error("input reply failure mark failed:", markErr);
      }
    }
  }

  private async failActiveCommandTurn(err: unknown): Promise<void> {
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (!turn?.commandRecord) return;
    if (!turn.terminalEmitted) {
      const dispatched = await dispatch(
        this.sink,
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
      turn.terminalEmitted = true;
    }
    await this.markCommandTerminal(turn, "turn.failed").catch((markErr) =>
      console.error("session command failure mark failed:", markErr, "original:", err),
    );
  }

  private scheduleWakeup(req: WakeupRequest): void {
    const delayMs = Math.max(0, req.delayMs);
    pendingWakeupsGauge.inc();
    setTimeout(() => {
      pendingWakeupsGauge.dec();
      void this.commandBus
        .enqueueWakeupSubmitTurn({
          prompt: req.prompt,
          clientNonce: `schedule_wakeup-${randomUUID()}`,
        })
        .catch((err) => console.error("schedule wakeup fire failed:", err));
    }, delayMs);
  }

  private async finalizeCommandIfAlreadyTerminal(
    record: SessionCommandRecord,
    clientNonce: string,
  ): Promise<boolean> {
    const terminal = await this.sink.findTurnTerminal(turnIDForClientNonce(clientNonce));
    if (!terminal) return false;
    await this.commandBus.markCompleted(record);
    return true;
  }

  private async failCommandRecord(record: SessionCommandRecord, err: unknown): Promise<void> {
    const prompt = String(record.prompt ?? "").trim();
    const pendingTurn = this.acceptTurn(prompt, commandClientNonce(record), record);
    if (!pendingTurn) {
      await this.commandBus.markFailed(record, err);
      return;
    }
    pendingTurn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(record);
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: pendingTurn.turnID,
        clientNonce: pendingTurn.clientNonce,
        source: "claude",
        type: "turn.failed",
        reason: "session_command_attempts_exceeded",
        error: err instanceof Error ? err.message : String(err),
      }),
    );
    if (dispatched) {
      pendingTurn.terminalEmitted = true;
      await this.markCommandTerminal(pendingTurn, "turn.failed");
    }
  }
}
