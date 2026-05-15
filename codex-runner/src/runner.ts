// Long-lived codex runner — drives one codex thread for the pod's
// lifetime via @openai/codex-sdk. Sibling of agent-runner/src/runner.ts
// with a different inner loop shape:
//
//   claude SDK: query() iterates an AsyncIterable of user messages,
//               yielding events forever. We push session commands into it; one
//               long-running iteration handles everything.
//   codex SDK:  thread.runStreamed(input) processes ONE turn and
//               returns. We pull a user message off the queue, await
//               runStreamed to completion, then loop.
//
// Multi-turn coordination is explicit: only one runStreamed in flight
// at a time. The Thread object keeps the conversation context across
// turns. Codex SDK persists threads to ~/.codex/sessions, which only helps
// runner-process restarts inside the same live session pod; session pod death
// is terminal and out of scope.
//
// Output contract: the adapter at adapters/codex.ts converts raw codex SDK
// events into Tank conversation events; the runner stamps and publishes
// those Tank events on the session bus. Raw provider events never reach
// the bus. Boundary events (user_message.created, turn.submitted) are
// owned by the backend (handlers_turns.go) — the runner does not publish
// them. On error: log and keep accepting new commands.

import { Codex, type Thread } from "@openai/codex-sdk";

import {
  CodexTankEventAdapter,
  type CodexAdapterTurn,
} from "./adapters/codex.js";
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

// AsyncQueue — one writer, one consumer. Session commands push; the
// run loop awaits the next value. Same shape as agent-runner's queue.
class AsyncQueue<T> {
  private readonly items: T[] = [];
  private waiters: ((v: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(v: T): void {
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }

  async next(): Promise<IteratorResult<T>> {
    if (this.items.length > 0) {
      return { value: this.items.shift()!, done: false };
    }
    if (this.closed) {
      return { value: undefined as unknown as T, done: true };
    }
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  close(): void {
    this.closed = true;
    for (const w of this.waiters) {
      w({ value: undefined as unknown as T, done: true });
    }
    this.waiters = [];
  }
}

// Pull the per-event dispatch out as a free function so the session-bus
// publish contract is testable without spinning up a Runner. The sink only
// accepts stamped Tank conversation events; anything else is rejected here
// by isDurableTankConversationEvent so the producer-side filter matches the
// persister-side ValidateEventMap rules.
//
// Returns true on a successful end-to-end dispatch (or when the event was
// non-durable and intentionally dropped); false when the publish failed.
interface DispatchSink {
  upsert(event: StampedTankEvent): Promise<void>;
}
export async function dispatch(
  sink: DispatchSink,
  event: TankConversationEvent,
): Promise<boolean> {
  const stamped = stampTankEvent(event);
  if (!isDurableTankConversationEvent(stamped)) {
    // Live-only or otherwise non-durable Tank events are not persisted.
    return true;
  }
  try {
    await sink.upsert(stamped);
  } catch (err) {
    console.error("session bus publish failed:", err);
    // Don't broadcast a live event we couldn't persist — the SPA's
    // history-replay would then disagree with what it saw live.
    return false;
  }
  return true;
}

function isAbortError(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  const code = (err as { code?: unknown }).code;
  return (
    err.name === "AbortError" ||
    code === "ABORT_ERR" ||
    /operation was aborted/i.test(err.message)
  );
}

export type AcceptedTurn = CodexAdapterTurn & {
  commandRecord?: SessionCommandRecord;
  interruptRecords?: SessionCommandRecord[];
  stopCommandHeartbeat?: () => void;
};

export function interruptTargetMatchesTurn(
  targetTurnID: string,
  turn: Pick<AcceptedTurn, "turnID" | "clientNonce">,
): boolean {
  return (
    !targetTurnID ||
    targetTurnID === turn.turnID ||
    targetTurnID === turn.clientNonce
  );
}

export function takePendingInterruptForTurn(
  pendingInterrupts: Array<Pick<SessionCommandRecord, "target_turn_id" | "client_nonce">>,
  turn: Pick<AcceptedTurn, "turnID" | "clientNonce">,
): Pick<SessionCommandRecord, "target_turn_id" | "client_nonce"> | null {
  const index = pendingInterrupts.findIndex((record) =>
    interruptTargetMatchesTurn(
      record.target_turn_id || record.client_nonce || "",
      turn,
    ),
  );
  if (index < 0) return null;
  return pendingInterrupts.splice(index, 1)[0] ?? null;
}

export class Runner {
  private readonly sink: SessionEventSink;
  private readonly commandBus: SessionCommandBus;
  private readonly userQueue = new AsyncQueue<{
    text: string;
    clientNonce?: string;
    commandRecord?: SessionCommandRecord;
  }>();
  private readonly codex: Codex;
  private readonly codexAdapter: CodexTankEventAdapter;
  private thread: Thread | null = null;
  private currentAbort: AbortController | null = null;
  private currentTurn: AcceptedTurn | null = null;
  private interruptRequested = false;
  private readonly pendingInterrupts: SessionCommandRecord[] = [];
  private readonly pendingCommandTurnTargets = new Set<string>();
  private turnSeq = 0;

  constructor(private readonly cfg: Config) {
    this.sink = new SessionEventSink(cfg);
    this.commandBus = new SessionCommandBus(cfg, "codex");

    // Codex SDK spawns the codex CLI subprocess; the CLI reads
    // ~/.codex/auth.json. The launcher writes placeholder subscription
    // auth and codex-api-proxy injects/rotates the real token centrally.
    // No CODEX_API_KEY needed — subscription auth path.
    this.codex = new Codex();
    this.codexAdapter = new CodexTankEventAdapter(cfg);
  }

  // Run until externally aborted. Each iteration awaits one user
  // message, runs one turn, drains its events. The thread persists
  // across iterations so codex sees the full conversation context.
  async run(signal: AbortSignal): Promise<void> {
    const stopConsumer = this.startCommandConsumer(signal);
    const onAbort = () => {
      this.userQueue.close();
      this.currentAbort?.abort();
    };
    signal.addEventListener("abort", onAbort, { once: true });
    try {
      this.thread = this.codex.startThread({
        workingDirectory: this.cfg.workspace,
        // /workspace inside session pods isn't a git repo (and may never be —
        // users mount projects ad hoc). Without this flag the CLI exits with
        // "Not inside a trusted directory and --skip-git-repo-check was not
        // specified."
        skipGitRepoCheck: true,
        sandboxMode: "danger-full-access",
        approvalPolicy: "never",
      });
      while (!signal.aborted) {
        const next = await this.userQueue.next();
        if (next.done) break;
        const { text: input, clientNonce, commandRecord } = next.value;
        const turnSeq = ++this.turnSeq;
        if (
          commandRecord &&
          clientNonce &&
          (await this.finalizeCommandIfAlreadyTerminal(
            commandRecord,
            clientNonce,
          ))
        ) {
          this.clearCommandTurnTarget(clientNonce);
          continue;
        }
        if (commandRecord && this.commandBus.attemptsExceeded(commandRecord)) {
          await this.failCommandRecord(
            clientNonce,
            turnSeq,
            commandRecord,
            new Error(
              `session command exceeded ${commandRecord.attempt_count ?? "unknown"} claim attempts`,
            ),
          );
          this.clearCommandTurnTarget(clientNonce);
          continue;
        }
        const turn = this.acceptTurn(clientNonce, turnSeq, commandRecord);
        if (!turn) {
          if (commandRecord)
            await this.commandBus.markFailed(
              commandRecord,
              new Error("session command was not accepted"),
            );
          this.clearCommandTurnTarget(clientNonce);
          continue;
        }
        if (commandRecord) {
          turn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(commandRecord);
        }

        await dispatch(
          this.sink,
          turnEvent({
            sessionID: this.cfg.sessionId,
            turnID: turn.turnID,
            clientNonce: turn.clientNonce,
            source: "codex",
            type: "turn.started",
          }),
        );

        this.currentAbort = new AbortController();
        this.currentTurn = turn;
        this.clearCommandTurnTarget(turn.clientNonce);
        const pendingInterrupt = takePendingInterruptForTurn(
          this.pendingInterrupts,
          turn,
        );
        if (pendingInterrupt) {
          this.interruptRequested = true;
          turn.interruptRecords = [pendingInterrupt as SessionCommandRecord];
          this.currentAbort.abort();
        }
        // If the outer signal aborts mid-turn, also abort the in-flight
        // codex subprocess. AbortSignal.any-style propagation done manually
        // since Node 20's AbortSignal.any is stage 3.
        const onOuterAbort = () => this.currentAbort?.abort();
        signal.addEventListener("abort", onOuterAbort, { once: true });

        try {
          const streamed = await this.thread.runStreamed(input, {
            signal: this.currentAbort.signal,
          });
          for await (const event of streamed.events) {
            if (signal.aborted) break;
            // Codex provider events are adapter inputs, not bus content. The
            // adapter converts them into Tank conversation events; only those
            // reach the durable session bus.
            for (const canonicalEvent of this.codexAdapter.canonicalEventsForCodexEvent(
              turn,
              event as { type: string; [k: string]: unknown },
            )) {
              const dispatched = await dispatch(this.sink, canonicalEvent);
              if (
                dispatched &&
                (canonicalEvent.type === "turn.completed" ||
                  canonicalEvent.type === "turn.failed" ||
                  canonicalEvent.type === "turn.interrupted")
              ) {
                await this.markCommandTerminal(turn, canonicalEvent.type);
              }
            }
          }
        } catch (err) {
          const interrupted =
            this.currentAbort.signal.aborted && isAbortError(err);
          if (interrupted) {
            const dispatched = await dispatch(
              this.sink,
              turnEvent({
                sessionID: this.cfg.sessionId,
                turnID: turn.turnID,
                clientNonce: turn.clientNonce,
                source: "codex",
                type: "turn.interrupted",
                reason: this.interruptRequested
                  ? "client_interrupt"
                  : "runner_shutdown",
              }),
            );
            if (dispatched) {
              await this.markCommandTerminal(turn, "turn.interrupted");
            }
            console.info("codex turn interrupted");
            continue;
          }
          const errMessage = err instanceof Error ? err.message : String(err);
          const dispatched = await dispatch(
            this.sink,
            turnEvent({
              sessionID: this.cfg.sessionId,
              turnID: turn.turnID,
              clientNonce: turn.clientNonce,
              source: "codex",
              type: "turn.failed",
              reason: "provider_failure",
              error: errMessage,
            }),
          );
          if (dispatched) {
            await this.markCommandTerminal(turn, "turn.failed");
          }
          console.error("codex turn failed:", err);
        } finally {
          signal.removeEventListener("abort", onOuterAbort);
          turn.stopCommandHeartbeat?.();
          turn.stopCommandHeartbeat = undefined;
          this.currentAbort = null;
          this.currentTurn = null;
          await this.completeStalePendingInterrupts(turn);
          this.interruptRequested = false;
        }
      }
    } finally {
      signal.removeEventListener("abort", onAbort);
      stopConsumer();
      this.userQueue.close();
    }
  }

  private startCommandConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startCommandConsumer(async (record) => {
        if (isInputReplyCommand(record)) {
          await this.commandBus.markFailed(
            record,
            new Error("input replies are not supported by codex"),
          );
          return;
        }
        if (isInterruptCommand(record)) {
          await this.acceptInterrupt(record);
          return;
        }
        const clientNonce = commandClientNonce(record);
        const prompt = String(record.prompt ?? "").trim();
        if (!prompt) {
          await this.commandBus.markFailed(record, new Error("submit command missing prompt"));
          return;
        }
        this.trackCommandTurnTarget(clientNonce);
        this.userQueue.push({
          text: prompt,
          clientNonce,
          commandRecord: record,
        });
      }, signal)
      .then((stop) => {
        stopConsumer = stop;
      })
      .catch((err) => console.error("session bus command consumer crashed:", err));
    return () => {
      void stopConsumer?.();
    };
  }

  private async acceptInterrupt(record: SessionCommandRecord): Promise<void> {
    const targetTurnID = record.target_turn_id || record.client_nonce || "";
    if (
      this.currentTurn &&
      interruptTargetMatchesTurn(targetTurnID, this.currentTurn)
    ) {
      this.interruptRequested = true;
      this.currentTurn.interruptRecords ??= [];
      this.currentTurn.interruptRecords.push(record);
      this.currentAbort?.abort();
      return;
    }
    if (targetTurnID && this.pendingCommandTurnTargets.has(targetTurnID)) {
      this.addPendingInterrupt(record);
      return;
    }
    await this.commandBus.markCompleted(record);
  }

  private trackCommandTurnTarget(clientNonce: string | undefined): void {
    const normalized = normalizeClientNonce(clientNonce);
    if (!normalized) return;
    this.pendingCommandTurnTargets.add(normalized);
    this.pendingCommandTurnTargets.add(turnIDForClientNonce(normalized));
  }

  private clearCommandTurnTarget(clientNonce: string | undefined): void {
    const normalized = normalizeClientNonce(clientNonce);
    if (!normalized) return;
    this.pendingCommandTurnTargets.delete(normalized);
    this.pendingCommandTurnTargets.delete(turnIDForClientNonce(normalized));
  }

  private addPendingInterrupt(record: SessionCommandRecord): void {
    if (!this.pendingInterrupts.some((candidate) => candidate.id === record.id)) {
      this.pendingInterrupts.push(record);
    }
  }

  private async completeStalePendingInterrupts(
    turn: Pick<AcceptedTurn, "turnID" | "clientNonce">,
  ): Promise<void> {
    while (true) {
      const pendingInterrupt = takePendingInterruptForTurn(
        this.pendingInterrupts,
        turn,
      );
      if (!pendingInterrupt) return;
      await this.commandBus.markCompleted(pendingInterrupt as SessionCommandRecord);
    }
  }

  // acceptTurn normalizes the client nonce and assembles the in-memory
  // turn record. Boundary events (user_message.created, turn.submitted)
  // are durably written by the backend when the user POSTed the turn —
  // the runner does not republish them. Returns null when the command
  // payload is malformed (the caller marks the command failed).
  private acceptTurn(
    rawClientNonce: unknown,
    turnSeq: number,
    commandRecord?: SessionCommandRecord,
  ): AcceptedTurn | null {
    const clientNonce = normalizeClientNonce(rawClientNonce);
    if (!clientNonce) {
      console.error("codex command rejected: client_nonce is required");
      return null;
    }
    return {
      turnID: turnIDForClientNonce(clientNonce),
      clientNonce,
      turnSeq,
      ...(commandRecord ? { commandRecord } : {}),
    };
  }

  private async markCommandTerminal(
    turn: AcceptedTurn,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ): Promise<void> {
    turn.stopCommandHeartbeat?.();
    turn.stopCommandHeartbeat = undefined;
    if (turn.commandRecord) {
      const record = turn.commandRecord;
      turn.commandRecord = undefined;
      try {
        await this.commandBus.markCompleted(record);
      } catch (err) {
        console.error("session command terminal mark failed:", err);
      }
    }
    const interruptRecords = turn.interruptRecords ?? [];
    turn.interruptRecords = undefined;
    for (const interruptRecord of interruptRecords) {
      try {
        await this.commandBus.markCompleted(interruptRecord);
      } catch (err) {
        console.error("interrupt command terminal mark failed:", err);
      }
    }
  }

  private async finalizeCommandIfAlreadyTerminal(
    record: SessionCommandRecord,
    clientNonce: string,
  ): Promise<boolean> {
    const terminal = await this.sink.findTurnTerminal(
      turnIDForClientNonce(clientNonce),
    );
    if (!terminal) return false;
    await this.commandBus.markCompleted(record);
    return true;
  }

  private async failCommandRecord(
    clientNonce: string | undefined,
    turnSeq: number,
    commandRecord: SessionCommandRecord,
    err: unknown,
  ): Promise<void> {
    const turn = this.acceptTurn(clientNonce, turnSeq, commandRecord);
    if (!turn) {
      await this.commandBus.markFailed(commandRecord, err);
      return;
    }
    turn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(commandRecord);
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "codex",
        type: "turn.failed",
        reason: "session_command_attempts_exceeded",
        error: err instanceof Error ? err.message : String(err),
      }),
    );
    if (dispatched) {
      await this.markCommandTerminal(turn, "turn.failed");
    }
  }
}
