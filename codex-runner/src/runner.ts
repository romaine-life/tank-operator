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

import {
  Codex,
  type ModelReasoningEffort,
  type Thread,
  type ThreadOptions,
} from "@openai/codex-sdk";

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
  itemTimelineID,
  stampTankEvent,
  itemEvent,
  shellTaskEvent,
  turnEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import {
  SessionCommandBus,
  isInputReplyCommand,
  isInterruptCommand,
  isStopBackgroundTaskCommand,
  commandClientNonce,
  type SessionCommandRecord,
} from "./sessionCommands.js";
import { truncateEventIfOversized } from "../../runner-shared/sessionBus.js";
import { reportRuntimeConfig } from "../../runner-shared/runtimeConfig.js";
import {
  commandsConsumedTotal,
  eventTruncatedTotal,
  inputReplyAnswerShapeTotal,
  interruptOutcomeTotal,
  natsPublishFailureTotal,
  providerErrorTotal,
  recordTurnPreStartLatency,
  recordTurnStart,
  recordTurnTerminal,
} from "./metrics.js";
import {
  CodexAppServerTransport,
  type AppServerUserInputQuestion,
  type AppServerUserInputRequest,
  type AppServerUserInputResponse,
} from "./appServerTransport.js";

// INTERRUPT_BUFFER_MS bounds how long an interrupt_turn record can sit
// in orphanInterrupts waiting for a matching submit_turn before the
// runner gives up and emits turn.failed{interrupt_orphaned}. Mirrors
// agent-runner's constant of the same name; documented in
// docs/tank-conversation-protocol.md → "Four-outcome contract on the
// runner side" and romaine-life/tank-operator#532.
const INTERRUPT_BUFFER_MS = parsePositiveEnvInt(
  process.env.SESSION_INTERRUPT_BUFFER_MS,
  30_000,
);

// TERMINAL_PUBLISH_* bound how hard we retry a durable terminal publish
// (turn.interrupted, or the turn.failed{publish_interrupt_failed}
// fallback). Same defaults as agent-runner so an env-override applies
// uniformly across both runners in the same pod image. See #532.
const TERMINAL_PUBLISH_ATTEMPTS = parsePositiveEnvInt(
  process.env.SESSION_TERMINAL_PUBLISH_ATTEMPTS,
  3,
);
const TERMINAL_PUBLISH_BACKOFF_MS = parsePositiveEnvInt(
  process.env.SESSION_TERMINAL_PUBLISH_BACKOFF_MS,
  500,
);

function parsePositiveEnvInt(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt((value ?? "").trim(), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function inputReplyKey(turnID: string, timelineID: string, providerItemID: string): string {
  return `${turnID}\x1f${timelineID}\x1f${providerItemID}`;
}

function answersForCodexInput(
  answers: Record<string, string[]> | undefined,
  annotations: Record<string, { notes?: string }> | undefined,
): Record<string, { answers: string[] }> {
  const out: Record<string, { answers: string[] }> = {};
  for (const [question, labels] of Object.entries(answers ?? {})) {
    const clean = labels.map((label) => String(label).trim()).filter(Boolean);
    const note = String(annotations?.[question]?.notes ?? "").trim();
    const semanticAnswers = note
      ? clean.filter((label) => label.toLowerCase() !== "other")
      : clean;
    inputReplyAnswerShapeTotal.labels(inputReplyAnswerShape(semanticAnswers, note)).inc();
    if (note) semanticAnswers.push(note);
    if (semanticAnswers.length > 0) out[question] = { answers: semanticAnswers };
  }
  return out;
}

type InputReplyAnswerShape = "selection_only" | "free_form_only" | "selection_with_notes" | "empty";

function inputReplyAnswerShape(labels: string[], note: string): InputReplyAnswerShape {
  if (labels.length > 0 && note) return "selection_with_notes";
  if (note) return "free_form_only";
  if (labels.length > 0) return "selection_only";
  return "empty";
}

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

function parseOptionalTimestampMs(value: unknown): number | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : undefined;
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
  // Stage 3 of romaine-life/tank-operator#532: see agent-runner's dispatch
  // for the contract. Codex tool outputs can easily exceed NATS's 1 MiB
  // max_payload (large stdout, generated patches, etc.). Truncate before
  // publish so a single oversized event doesn't fail the publish and
  // hole the durable ledger.
  const sizeGuard = truncateEventIfOversized(
    stamped as unknown as Record<string, unknown>,
  );
  if (sizeGuard.truncated) {
    const severity = sizeGuard.payloadDropped ? "payload-dropped" : "strings-truncated";
    eventTruncatedTotal.labels(stamped.type, severity).inc();
    console.warn(
      "session bus event truncated:",
      JSON.stringify({
        event_type: stamped.type,
        original_bytes: sizeGuard.originalBytes,
        final_bytes: sizeGuard.finalBytes,
        fields: sizeGuard.fields,
        severity,
      }),
    );
  }
  try {
    await sink.upsert(sizeGuard.event as unknown as StampedTankEvent);
  } catch (err) {
    console.error("session bus publish failed:", err);
    natsPublishFailureTotal.inc();
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
  commandCreatedAtMs?: number;
  claimedAtMs?: number;
  interruptRecords?: SessionCommandRecord[];
  stopCommandHeartbeat?: () => void;
  // Set true when the run-loop dequeues a turn and finds a pre-arrived
  // interrupt waiting in pendingInterrupts; the AbortController is
  // pre-fired and the codex thread's runStreamed rejects without
  // emitting any turn events. Distinguishes the terminated_pre_sdk
  // counter bucket from terminated_via_sdk (interrupt arrived during
  // an in-flight thread). See romaine-life/tank-operator#532.
  interruptOnStart?: boolean;
};

type PendingInputReply = {
  turn: AcceptedTurn;
  timelineID: string;
  providerItemID: string;
  resolve: (response: AppServerUserInputResponse) => void;
};

export function threadOptionsForCommand(
  cfg: Config,
  record?: SessionCommandRecord,
): ThreadOptions {
  const model = String(record?.model ?? "").trim();
  const effort = String(record?.effort ?? "").trim();
  return {
    workingDirectory: cfg.workspace,
    // /workspace inside session pods isn't a git repo (and may never be —
    // users mount projects ad hoc). Without this flag the CLI exits with
    // "Not inside a trusted directory and --skip-git-repo-check was not
    // specified."
    skipGitRepoCheck: true,
    sandboxMode: "danger-full-access",
    approvalPolicy: "never",
    ...(model ? { model } : {}),
    ...(effort ? { modelReasoningEffort: effort as ModelReasoningEffort } : {}),
  };
}

// OrphanInterrupt parks an interrupt_turn record that arrived before
// the runner saw the matching submit_turn. The race resolution and
// terminal-outcome contract are documented in
// docs/tank-conversation-protocol.md → "Four-outcome contract on the
// runner side" and romaine-life/tank-operator#532. Sibling of agent-
// runner's BufferedInterrupt.
interface OrphanInterrupt {
  record: SessionCommandRecord;
  // target_turn_id || client_nonce on the record. Used to match against
  // the bare-uuid and "turn_"-prefixed shapes that coexist on the wire.
  targetKey: string;
  receivedAtMs: number;
  // Keeps the JetStream delivery un-acked so a runner crash redelivers
  // it. applyInterruptToTurn (via drain) or expireOrphanInterrupt take
  // ownership of the ack.
  stopCommandHeartbeat: () => void;
  orphanTimer: ReturnType<typeof setTimeout>;
}

// codexQuestionsToTankShape normalizes codex's app-server
// AppServerUserInputQuestion[] into the Tank conversation protocol's
// question shape. The frontend renders the Tank shape only — codex's
// `isOther`, `isSecret`, stable `id`, and nullable `options` field never
// reach the renderer directly.
//
//   isOther          → allowFreeForm  (codex's "say something else" flag;
//                                       in every wild codex AskUserQuestion
//                                       observed in 2026-05, isOther=true,
//                                       and the absence of this mapping
//                                       silently disabled the free-form path)
//   isSecret         → secret
//   options ?? []    → options[]      (codex permits pure free-form questions
//                                       with options=null; without
//                                       allowFreeForm support upstream, those
//                                       are unanswerable in the current UI)
//   id               → dropped at this boundary; the Tank shape keys on
//                       question text (mirrors Claude's adapter). Codex
//                       deduplicates by id internally; the runner returns
//                       answers keyed by id elsewhere.
//   multiSelect      → false (codex has no multi-select primitive today;
//                              if it grows one, route the flag through here)
export function codexQuestionsToTankShape(
  questions: AppServerUserInputQuestion[] | undefined,
): TankAskUserQuestion[] {
  if (!Array.isArray(questions)) return [];
  return questions.flatMap((q): TankAskUserQuestion[] => {
    const question = typeof q?.question === "string" ? q.question : "";
    if (!question) return [];
    const options = Array.isArray(q.options)
      ? q.options.flatMap((opt): TankAskUserQuestionOption[] => {
          if (!opt || typeof opt !== "object") return [];
          const label = typeof opt.label === "string" ? opt.label : "";
          if (!label) return [];
          return [
            {
              label,
              ...(typeof opt.description === "string" && opt.description
                ? { description: opt.description }
                : {}),
            },
          ];
        })
      : [];
    return [
      {
        question,
        ...(typeof q.header === "string" && q.header ? { header: q.header } : {}),
        multiSelect: false,
        options,
        allowFreeForm: q.isOther === true,
        secret: q.isSecret === true,
      },
    ];
  });
}

interface TankAskUserQuestionOption {
  label: string;
  description?: string;
}

interface TankAskUserQuestion {
  question: string;
  header?: string;
  multiSelect: boolean;
  options: TankAskUserQuestionOption[];
  allowFreeForm: boolean;
  secret: boolean;
}

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
  private readonly appServerTransport: CodexAppServerTransport | null;
  private readonly codexAdapter: CodexTankEventAdapter;
  private thread: Thread | null = null;
  private currentAbort: AbortController | null = null;
  private currentTurn: AcceptedTurn | null = null;
  private readonly pendingInputReplies = new Map<string, PendingInputReply>();
  private interruptRequested = false;
  private readonly pendingInterrupts: SessionCommandRecord[] = [];
  // orphanInterrupts holds interrupt_turn records whose target_turn_id
  // matches neither the current turn nor any submit_turn the data-plane
  // consumer has seen yet. The race exists because #511 split control
  // and data planes so they don't synchronize past JetStream delivery —
  // a Stop click on a freshly-submitted turn can race the data
  // consumer's handler call to trackCommandTurnTarget. Pre-#532 this
  // case silently ack'd and the user's stop was lost; #532 makes us
  // buffer the record with a heartbeat (so a runner crash redelivers)
  // and an orphan timer (so the buffer always drains to a durable
  // terminal). When trackCommandTurnTarget eventually fires for the
  // matching submit_turn, drainOrphanInterruptsFor moves the record
  // into pendingInterrupts so the existing dequeue-side interrupt
  // path picks it up.
  private readonly orphanInterrupts: OrphanInterrupt[] = [];
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
    this.appServerTransport =
      process.env.CODEX_RUNNER_TRANSPORT === "app-server"
        ? new CodexAppServerTransport({
            cwd: cfg.workspace,
            onRequestUserInput: (request, requestSignal) =>
              this.requestAppServerUserInput(request, requestSignal),
            onRuntimeConfigApplied: (threadOptions) =>
              this.reportAppliedRuntimeConfig(threadOptions),
            onRuntimeContextWindowObserved: (tokens) =>
              this.reportRuntimeContextWindow(tokens),
          })
        : null;
    this.codexAdapter = new CodexTankEventAdapter(cfg);
  }

  private reportAppliedRuntimeConfig(threadOptions: ThreadOptions): void {
    const model = String(threadOptions.model ?? "").trim();
    const effort = String(threadOptions.modelReasoningEffort ?? "").trim();
    void reportRuntimeConfig(this.cfg, { model, effort }).catch((err) => {
      console.warn("runtime config report failed:", err);
    });
  }

  private reportRuntimeContextWindow(tokens: number): void {
    void reportRuntimeConfig(this.cfg, {
      contextWindowTokens: tokens,
      contextWindowSource: "codex_app_server_token_usage",
    }).catch((err) => {
      console.warn("runtime context window report failed:", err);
    });
  }

  // Run until externally aborted. Each iteration awaits one user
  // message, runs one turn, drains its events. The thread persists
  // across iterations so codex sees the full conversation context.
  async run(signal: AbortSignal): Promise<void> {
    // Two independent JetStream consumers: data plane (submit_turn —
    // serial, ack-after-terminal) and control plane (interrupt_turn —
    // low-latency, never blocked by an in-flight turn). See
    // runner-shared/sessionBus.js and docs/tank-conversation-protocol.md →
    // "Durable turn interruption" for the contract. The split is the
    // load-bearing fix for the "Stop doesn't interrupt deep tool-use
    // loops" regression (a max_ack_pending=1 data-plane consumer held
    // interrupt_turn behind submit_turn for the full duration of the turn).
    const stopConsumer = this.startCommandConsumer(signal);
    const stopControl = this.startControlConsumer(signal);
    const onAbort = () => {
      this.userQueue.close();
      this.currentAbort?.abort();
    };
    signal.addEventListener("abort", onAbort, { once: true });
    try {
      while (!signal.aborted) {
        const next = await this.userQueue.next();
        if (next.done) break;
        const { text: input, clientNonce, commandRecord } = next.value;
        if (!this.appServerTransport && !this.thread) {
          const threadOptions = threadOptionsForCommand(this.cfg, commandRecord);
          this.thread = this.codex.startThread(threadOptions);
          this.reportAppliedRuntimeConfig(threadOptions);
        }
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
        await this.publishTurnClaimed(turn);

        recordTurnStart(turn.turnID);
        recordTurnPreStartLatency("claimed_to_started", turn.claimedAtMs);
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
          turn.interruptOnStart = true;
          this.currentAbort.abort();
        }
        // If the outer signal aborts mid-turn, also abort the in-flight
        // codex subprocess. AbortSignal.any-style propagation done manually
        // since Node 20's AbortSignal.any is stage 3.
        const onOuterAbort = () => this.currentAbort?.abort();
        signal.addEventListener("abort", onOuterAbort, { once: true });

        try {
          const events = this.appServerTransport
            ? this.appServerTransport.runTurn(
                input,
                threadOptionsForCommand(this.cfg, commandRecord),
                this.currentAbort.signal,
              )
            : (await this.thread!.runStreamed(input, {
                signal: this.currentAbort.signal,
              })).events;
          for await (const event of events) {
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
            // Per the four-outcome contract (#532), the durable
            // turn.interrupted publish is retried; on retry exhaustion
            // we fall back to turn.failed{publish_interrupt_failed} so
            // the UI's "stopping" projection always resolves. The
            // outcome counter is incremented exactly once per
            // interrupt_turn record that contributed to this terminal.
            const reason = this.interruptRequested
              ? "client_interrupt"
              : "runner_shutdown";
            const published = await this.publishTerminalWithRetry(
              turnEvent({
                sessionID: this.cfg.sessionId,
                turnID: turn.turnID,
                clientNonce: turn.clientNonce,
                source: "codex",
                type: "turn.interrupted",
                reason,
              }),
            );
            if (published) {
              await this.markCommandTerminal(turn, "turn.interrupted");
              // Count one terminal-outcome bucket per interrupt record
              // that drove this turn down. If the user double-Stop'd
              // and both records contributed, both increment.
              const interruptCount = turn.interruptRecords?.length ?? 0;
              if (interruptCount > 0 && reason === "client_interrupt") {
                const bucket = turn.interruptOnStart
                  ? "terminated_pre_sdk"
                  : "terminated_via_sdk";
                for (let i = 0; i < interruptCount; i++) {
                  interruptOutcomeTotal.labels(bucket).inc();
                }
              }
            } else {
              // Both turn.interrupted retries and the turn.failed
              // fallback below failed. Mark every contributing
              // interrupt record as failed on the command bus so
              // JetStream redelivery retries the whole flow.
              const fallback = await this.publishTerminalWithRetry(
                turnEvent({
                  sessionID: this.cfg.sessionId,
                  turnID: turn.turnID,
                  clientNonce: turn.clientNonce,
                  source: "codex",
                  type: "turn.failed",
                  reason: "publish_interrupt_failed",
                }),
              );
              if (fallback) {
                await this.markCommandTerminal(turn, "turn.failed");
              }
              const interruptCount = turn.interruptRecords?.length ?? 0;
              for (let i = 0; i < interruptCount; i++) {
                interruptOutcomeTotal.labels("publish_failed").inc();
              }
            }
            console.info("codex turn interrupted");
            continue;
          }
          providerErrorTotal.labels("query").inc();
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
      stopControl();
      await this.appServerTransport?.stop();
      this.userQueue.close();
    }
  }

  private startCommandConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startCommandConsumer(async (record) => {
        // Interrupts MUST arrive via startControlConsumer (separate
        // JetStream consumer on the control subject). The data-plane
        // consumer has max_ack_pending=1 by design, so an interrupt
        // delivered here would block behind the in-flight submit_turn
        // for the full duration of the turn — the regression the split
        // fixes. The shared sessionBus drops stray interrupts on the
        // data subject before they reach this handler.
        commandsConsumedTotal.labels("submit_turn", "accepted").inc();
        const clientNonce = commandClientNonce(record);
        const prompt = String(record.prompt ?? "").trim();
        if (!prompt) {
          commandsConsumedTotal.labels("submit_turn", "invalid").inc();
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

  private async requestAppServerUserInput(
    request: AppServerUserInputRequest,
    _signal?: AbortSignal,
  ): Promise<AppServerUserInputResponse> {
    const turn = this.currentTurn;
    if (!turn) throw new Error("request_user_input arrived with no active Codex turn");
    return this.pauseTurnForInput(turn, request);
  }

  private async pauseTurnForInput(
    turn: AcceptedTurn,
    request: AppServerUserInputRequest,
  ): Promise<AppServerUserInputResponse> {
    const timelineID = itemTimelineID(turn.turnID, request.providerItemID);
    const key = inputReplyKey(turn.turnID, timelineID, request.providerItemID);
    const waitForReply = new Promise<AppServerUserInputResponse>((resolve) => {
      this.pendingInputReplies.set(key, {
        turn,
        timelineID,
        providerItemID: request.providerItemID,
        resolve,
      });
    });
    const published = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "codex",
        type: "turn.awaiting_input",
        questions: codexQuestionsToTankShape(request.questions),
        awaitingProviderItemID: request.providerItemID,
        awaitingTimelineID: timelineID,
      }),
    );
    if (published) {
      return waitForReply;
    }
    this.pendingInputReplies.delete(key);
    throw new Error("failed to persist AskUserQuestion pause");
  }

  private async acceptInputReply(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("input_reply", "accepted").inc();
    const targetTurnID = String(record.target_turn_id ?? "").trim();
    const targetTimelineID = String(record.target_timeline_id ?? "").trim();
    const targetProviderItemID = String(record.target_provider_item_id ?? "").trim();
    const key = inputReplyKey(targetTurnID, targetTimelineID, targetProviderItemID);
    const pending = this.pendingInputReplies.get(key);
    if (!pending) {
      // Runner restart/race: the durable answer can arrive before the redelivered
      // submit_turn has recreated the app-server request. Redeliver rather than
      // failing the user's answer.
      commandsConsumedTotal.labels("input_reply", "not_ready").inc();
      record.nak(1_000);
      return;
    }
    this.pendingInputReplies.delete(key);
    pending.resolve({ answers: answersForCodexInput(record.answers, record.annotations) });
    await this.commandBus.markCompleted(record);
  }

  private async acceptStopBackgroundTask(record: SessionCommandRecord): Promise<void> {
    if (!this.appServerTransport) {
      commandsConsumedTotal.labels("stop_background_task", "unsupported").inc();
      await this.commandBus.markFailed(
        record,
        new Error("background task stop is only supported by codex app-server transport"),
      );
      return;
    }
    commandsConsumedTotal.labels("stop_background_task", "accepted").inc();
    const taskID = String(
      record.target_task_id ??
        record.target_process_id ??
        record.target_provider_item_id ??
        "",
    ).trim();
    const turnID = String(record.target_turn_id ?? record.client_nonce ?? "").trim();
    if (!taskID || !turnID) {
      commandsConsumedTotal.labels("stop_background_task", "invalid").inc();
      await this.commandBus.markFailed(record, new Error("background task stop missing target task or turn"));
      return;
    }
    const providerItemID = String(record.target_provider_item_id ?? taskID).trim() || taskID;
    try {
      await this.appServerTransport.cleanBackgroundTerminals();
      const dispatched = await dispatch(
        this.sink,
        shellTaskEvent({
          sessionID: this.cfg.sessionId,
          turnID,
          source: "codex",
          type: "shell_task.exited",
          taskID,
          status: "stopped",
          providerItemID,
          providerEventID: record.command_id,
          payload: {
            status: "stopped",
            stop_reason: "client_request",
            provider_item_id: providerItemID,
            process_id: String(record.target_process_id ?? taskID).trim() || taskID,
          },
        }),
      );
      if (!dispatched) {
        await this.commandBus.markFailed(
          record,
          new Error("background task stop terminal publish failed"),
        );
        return;
      }
      await this.commandBus.markCompleted(record);
    } catch (err) {
      await this.commandBus.markFailed(record, err instanceof Error ? err : new Error(String(err)));
    }
  }

  // startControlConsumer drives the control-plane JetStream consumer.
  // Today: interrupt_turn and background task stop. Future control signals
  // land here as added branches, never on the data-plane consumer.
  private startControlConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startControlConsumer(async (record) => {
        if (isInputReplyCommand(record)) {
          await this.acceptInputReply(record);
          return;
        }
        if (isInterruptCommand(record)) {
          commandsConsumedTotal.labels("interrupt_turn", "accepted").inc();
          await this.acceptInterrupt(record);
          return;
        }
        if (isStopBackgroundTaskCommand(record)) {
          await this.acceptStopBackgroundTask(record);
          return;
        }
        commandsConsumedTotal.labels("control_unknown", "dropped").inc();
        console.warn("session bus control consumer: unknown command type",
          { type: record.type, command_id: record.id });
        await this.commandBus.markCompleted(record);
      }, signal)
      .then((stop) => {
        stopConsumer = stop;
      })
      .catch((err) => console.error("session bus control consumer crashed:", err));
    return () => {
      void stopConsumer?.();
    };
  }

  // acceptInterrupt is the control-plane entry point. Per the
  // four-outcome contract (romaine-life/tank-operator#532), every accepted
  // interrupt MUST resolve to exactly one terminal-outcome increment on
  // interruptOutcomeTotal within bounded time. Pre-#532 the
  // `await this.commandBus.markCompleted(record)` else-branch silently
  // ack'd an interrupt that targeted a turn the runner hadn't seen yet
  // (the race window between control-plane delivery and the data-plane
  // consumer's trackCommandTurnTarget call); #532 closes that path by
  // buffering with an orphan timer.
  private async acceptInterrupt(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("interrupt_turn", "accepted").inc();
    const targetKey = String(record.target_turn_id ?? record.client_nonce ?? "").trim();
    if (!targetKey) {
      interruptOutcomeTotal.labels("invalid_target").inc();
      await this.commandBus.markFailed(
        record,
        new Error("interrupt_turn missing both target_turn_id and client_nonce"),
      );
      return;
    }
    if (
      this.currentTurn &&
      interruptTargetMatchesTurn(targetKey, this.currentTurn)
    ) {
      this.interruptRequested = true;
      this.currentTurn.interruptRecords ??= [];
      this.currentTurn.interruptRecords.push(record);
      this.currentAbort?.abort();
      // outcome counter is incremented in the run-loop's interrupted
      // catch branch — that's where publish + ack actually fire, and
      // we want to count the terminal outcome (terminated_via_sdk or
      // publish_failed) not the arrival.
      return;
    }
    if (this.pendingCommandTurnTargets.has(targetKey)) {
      // submit_turn already enqueued; takePendingInterruptForTurn picks
      // it up at dequeue time. Existing path.
      this.addPendingInterrupt(record);
      interruptOutcomeTotal.labels("buffered").inc();
      return;
    }
    // No matching turn known. Pre-#532 this silently ack'd the record;
    // #532 buffers with an orphan timer so the buffer always drains to
    // a durable terminal (either terminated_pre_sdk when the matching
    // submit_turn lands, or orphaned when it never does).
    this.bufferOrphanInterrupt(record, targetKey);
  }

  private bufferOrphanInterrupt(record: SessionCommandRecord, targetKey: string): void {
    interruptOutcomeTotal.labels("buffered").inc();
    const stopHeartbeat = this.commandBus.startCommandHeartbeat(record);
    const orphanTimer = setTimeout(() => {
      void this.expireOrphanInterrupt(record).catch((err) =>
        console.error("expireOrphanInterrupt failed:", err),
      );
    }, INTERRUPT_BUFFER_MS);
    if (typeof (orphanTimer as { unref?: () => void }).unref === "function") {
      (orphanTimer as { unref: () => void }).unref();
    }
    this.orphanInterrupts.push({
      record,
      targetKey,
      receivedAtMs: Date.now(),
      stopCommandHeartbeat: stopHeartbeat,
      orphanTimer,
    });
  }

  // drainOrphanInterruptsFor moves orphan buffer entries matching this
  // clientNonce into pendingInterrupts (the existing dequeue-side
  // buffer). Called from trackCommandTurnTarget the moment a submit_turn
  // is received, so a stop click that raced into the runner before its
  // submit gets applied as soon as the submit catches up.
  private drainOrphanInterruptsFor(clientNonce: string): void {
    if (this.orphanInterrupts.length === 0) return;
    const turnID = turnIDForClientNonce(clientNonce);
    const remaining: OrphanInterrupt[] = [];
    for (const buf of this.orphanInterrupts) {
      if (buf.targetKey === clientNonce || buf.targetKey === turnID) {
        clearTimeout(buf.orphanTimer);
        buf.stopCommandHeartbeat();
        this.addPendingInterrupt(buf.record);
      } else {
        remaining.push(buf);
      }
    }
    this.orphanInterrupts.length = 0;
    this.orphanInterrupts.push(...remaining);
  }

  private async expireOrphanInterrupt(record: SessionCommandRecord): Promise<void> {
    const idx = this.orphanInterrupts.findIndex((buf) => buf.record === record);
    if (idx < 0) return; // already drained
    const buf = this.orphanInterrupts[idx]!;
    this.orphanInterrupts.splice(idx, 1);
    buf.stopCommandHeartbeat();
    const syntheticTurnID = buf.targetKey.startsWith("turn_")
      ? buf.targetKey
      : turnIDForClientNonce(buf.targetKey);
    // Before emitting interrupt_orphaned, check the durable ledger for
    // a natural terminal on the target. The race is legitimate when a
    // stop click lands after the turn has already completed/failed in
    // a previous runner-process incarnation — the UI shows the natural
    // terminal; emitting a duplicate turn.failed here would muddy the
    // transcript. Ack the interrupt with turn_already_terminal instead.
    try {
      const terminal = await this.sink.findTurnTerminal(syntheticTurnID);
      if (terminal) {
        interruptOutcomeTotal.labels("turn_already_terminal").inc();
        await this.commandBus.markCompleted(record);
        return;
      }
    } catch (err) {
      // Durable-terminal lookup is best-effort. If it fails we fall
      // through to publishing the orphan terminal — that's safer than
      // ack'ing without any durable response.
      console.warn("findTurnTerminal failed for orphan check; falling through to interrupt_orphaned:", err);
    }
    const published = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: syntheticTurnID,
        clientNonce: buf.targetKey,
        source: "codex",
        type: "turn.failed",
        reason: "interrupt_orphaned",
      }),
    );
    if (published) {
      interruptOutcomeTotal.labels("orphaned").inc();
      await this.commandBus.markCompleted(record);
      return;
    }
    interruptOutcomeTotal.labels("publish_failed").inc();
    await this.commandBus.markFailed(
      record,
      new Error("orphaned-interrupt terminal publish failed after retry"),
    );
  }

  private async publishTerminalWithRetry(event: TankConversationEvent): Promise<boolean> {
    for (let attempt = 0; attempt < TERMINAL_PUBLISH_ATTEMPTS; attempt++) {
      if (attempt > 0) {
        const delay = TERMINAL_PUBLISH_BACKOFF_MS * 2 ** (attempt - 1);
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      if (await dispatch(this.sink, event)) return true;
    }
    return false;
  }

  private trackCommandTurnTarget(clientNonce: string | undefined): void {
    const normalized = normalizeClientNonce(clientNonce);
    if (!normalized) return;
    this.pendingCommandTurnTargets.add(normalized);
    this.pendingCommandTurnTargets.add(turnIDForClientNonce(normalized));
    // Drain any orphan-buffered interrupts whose target matches this
    // turn. The runner saw the stop click before it saw the submit_turn
    // (the control plane and data plane don't synchronize past
    // JetStream delivery — by #511's design); now that the submit is
    // tracked, the buffered interrupt moves into pendingInterrupts and
    // the existing dequeue-side path applies it. See #532.
    this.drainOrphanInterruptsFor(normalized);
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
      commandCreatedAtMs: parseOptionalTimestampMs(commandRecord?.created_at),
      ...(commandRecord ? { commandRecord } : {}),
    };
  }

  private async publishTurnClaimed(turn: AcceptedTurn): Promise<void> {
    const claimedAtMs = Date.now();
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "codex",
        type: "turn.claimed",
      }),
    );
    if (!dispatched) return;
    turn.claimedAtMs = claimedAtMs;
    recordTurnPreStartLatency("command_created_to_claimed", turn.commandCreatedAtMs, claimedAtMs);
  }

  private async markCommandTerminal(
    turn: AcceptedTurn,
    type: "turn.completed" | "turn.failed" | "turn.interrupted" | "turn.awaiting_input",
  ): Promise<void> {
    const outcome =
      type === "turn.completed"
        ? "completed"
        : type === "turn.failed"
          ? "failed"
          : type === "turn.awaiting_input"
            ? "awaiting_input"
            : "interrupted";
    recordTurnTerminal(turn.turnID, outcome);
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
