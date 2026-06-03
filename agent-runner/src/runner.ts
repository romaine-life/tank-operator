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
// ScheduleWakeup tool_use calls are registered with the backend, which owns
// durable timer state and submits the later turn through handlers_turns.go.
//
// On error: log and keep running. Single-turn failures shouldn't kill the
// runner; persistent failures will surface via session-bus publish errors.

import {
  query,
  type CanUseTool,
  type EffortLevel,
  type PermissionResult,
  type Query,
  type SDKMessage,
  type SDKUserMessage,
  type Options,
} from "@anthropic-ai/claude-agent-sdk";
import {
  canonicalEventsForClaudeMessage,
  claudeQuestionsToTankShape,
  claudeTaskIdentifiers,
  isClaudeTaskLifecycleMessage,
  startsClaudeTurn,
  type ClaudeProviderEvent,
  type ClaudeTurnContext,
} from "./adapters/claude.js";
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
  turnEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import {
  SessionCommandBus,
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
  interruptOutcomeTotal,
  natsPublishFailureTotal,
  optionsOverrideIgnoredTotal,
  optionsPinnedTotal,
  providerControlTotal,
  providerErrorTotal,
  providerFailureClassTotal,
  providerRateLimitEventTotal,
  recordTurnStart,
  recordTurnTerminal,
  scheduledWakeupRegisterTotal,
  unmappedProviderEventTotal,
} from "./metrics.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";
import { registerScheduledWakeup } from "../../runner-shared/scheduledWakeup.js";

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
  // Stage 3 of #532: keep Tank events under the transport budget so a
  // single oversized tool_result.output (Read of a large file, Bash with
  // a massive stdout) doesn't throw `payload max_payload size exceeded`
  // and silently lose the event. The truncation utility replaces big
  // string fields with a typed marker that preserves the schema shape;
  // see runner-shared/sessionBus.js for the contract.
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
    return false;
  }
  return true;
}

// logUnhandledSdkMessage emits a structured JSON log line for SDK messages
// whose `type` is not one the adapter converts into Tank conversation
// events. canonicalEventsForClaudeMessage handles assistant/user/result
// frames plus Claude's background task lifecycle. "stream_event" is the
// partial-typing surface and is intentionally noisy, so we skip it too.
// Everything else — hooks, status changes, plugin installs, future SDK
// types — stays discoverable in kubectl logs instead of silently vanishing.
// Fields included are the small set of identifying ones that show up
// across SDK message variants; the full payload is still in the on-disk
// JSONL transcript for deeper digs.
const UNHANDLED_LOG_FIELDS = [
  "subtype",
  "task_id",
  "tool_use_id",
  "status",
  "summary",
  "description",
  "last_tool_name",
  "error",
  "patch",
  "uuid",
] as const;

// classifyProviderFailure maps an upstream Anthropic/SDK error message to
// one of a fixed, closed set of classes for providerFailureClassTotal.
// The match table is intentionally signature-based (substring on the
// stable parts of the error text) rather than HTTP-status-based, because
// several distinct 400s share a status but mean very different things and
// the operator question is "which provider failure mode is firing?".
//
// `thinking_block_modified` is the load-bearing class: it pins the
// extended-thinking resume bug behind session 340 (a long
// interleaved-thinking turn replayed on resume with a mutated
// thinking/redacted_thinking block, rejected by the API). It must stay at
// zero after the @anthropic-ai/claude-agent-sdk ^0.3.158 bump
// (romaine-life/tank-operator#743); a later non-zero rate is a regression.
export type ProviderFailureClass =
  | "thinking_block_modified"
  | "overloaded"
  | "rate_limit"
  | "context_length"
  | "auth"
  | "other";

export function classifyProviderFailure(message: string): ProviderFailureClass {
  const m = message.toLowerCase();
  // The API phrases this as: `thinking` or `redacted_thinking` blocks in
  // the latest assistant message cannot be modified.
  if (m.includes("thinking") && m.includes("cannot be modified")) {
    return "thinking_block_modified";
  }
  if (m.includes("overloaded")) return "overloaded";
  if (m.includes("rate limit") || m.includes("rate_limit") || m.includes(" 429")) {
    return "rate_limit";
  }
  if (
    m.includes("prompt is too long") ||
    m.includes("maximum context length") ||
    m.includes("context_length_exceeded")
  ) {
    return "context_length";
  }
  if (
    m.includes("authentication") ||
    m.includes("unauthorized") ||
    m.includes(" 401") ||
    m.includes(" 403")
  ) {
    return "auth";
  }
  return "other";
}

export function logUnhandledSdkMessage(message: SDKMessage): void {
  const m = message as Record<string, unknown> & { type?: unknown };
  const type = typeof m.type === "string" ? m.type : "";
  const subtype = typeof m.subtype === "string" ? m.subtype : "";
  if (
    type === "assistant" ||
    type === "user" ||
    type === "result" ||
    isClaudeTaskLifecycleMessage(m as ClaudeProviderEvent) ||
    type === "stream_event" ||
    // system/init is session-setup metadata; system/compact_boundary is now
    // mapped by the Claude adapter to context.compacted. Both are explicitly
    // ignored here so they don't inflate the unmapped-drop counter.
    (type === "system" && (subtype === "init" || subtype === "compact_boundary"))
  ) {
    return;
  }
  // Anything still here is a provider event the adapter neither mapped nor
  // explicitly ignored — the silent-drop class that hid context compaction.
  // Count it (bounded type/subtype labels) so the next semantically-significant
  // provider event surfaces in metrics instead of vanishing from the ledger.
  unmappedProviderEventTotal.labels(type || "unknown", subtype || "none").inc();
  const fields: Record<string, unknown> = {
    msg: "sdk_message_unhandled",
    type,
  };
  for (const key of UNHANDLED_LOG_FIELDS) {
    const v = m[key];
    if (v !== undefined) fields[key] = v;
  }
  console.log(JSON.stringify(fields));
}

function isClaudeRateLimitEvent(message: ClaudeProviderEvent): boolean {
  return message.type === "rate_limit_event";
}

function claudeRateLimitError(message: ClaudeProviderEvent): string {
  const parts = ["Claude provider emitted rate_limit_event"];
  for (const key of ["message", "error", "summary", "description", "retry_after_ms", "retry_after_seconds"]) {
    const value = message[key];
    if (typeof value === "string" && value.trim()) {
      parts.push(`${key}=${value.trim()}`);
    } else if (typeof value === "number" && Number.isFinite(value)) {
      parts.push(`${key}=${value}`);
    }
  }
  return parts.join("; ");
}

export interface PendingTurn {
  turnID: string;
  clientNonce: string;
  text: string;
  started: boolean;
  interrupted: boolean;
  terminalEmitted: boolean;
  finalAnswer?: ClaudeTurnContext["finalAnswer"];
  commandRecord?: SessionCommandRecord;
  stopCommandHeartbeat?: () => void;
  // interruptOnStart carries any interrupt_turn record(s) that landed on
  // the control consumer before the matching submit_turn had been
  // dispatched on the runner. acceptCommandTurn drains pendingInterrupts
  // against the freshly-built PendingTurn at submit time and parks the
  // record(s) here; the SDK is never fed the prompt, and the dispatch
  // path emits `turn.interrupted{reason:"client_interrupt_before_start"}`
  // synthetically. See romaine-life/tank-operator#532 for the race the
  // pre-#532 silent `return "not_found"` exposed.
  interruptOnStart?: SessionCommandRecord[];
}

// BufferedInterrupt tracks an interrupt_turn command that arrived at the
// control consumer with no matching active/pending turn yet on the
// runner. The buffer's purpose is the post-#511 / pre-#532 race: the
// control consumer can deliver interrupt_turn arbitrarily before the
// data-plane consumer dispatches the matching submit_turn (the planes
// don't synchronize past JetStream-level delivery). Pre-#532 the
// runner returned "not_found" silently from `interruptActiveTurn`; the
// SDK never got an interrupt and no durable terminal landed — the UI
// hung in "stopping" forever.
//
// Buffered records hold their JetStream message un-acked via the
// heartbeat below so a runner crash redelivers them. The orphanTimer
// guarantees the buffer always drains within INTERRUPT_BUFFER_MS — if
// no matching submit_turn arrives, we synthesize a durable terminal
// (turn.failed{interrupt_orphaned}) so the UI resolves out of
// "stopping" and the user sees an honest failure rather than a hang.
interface BufferedInterrupt {
  record: SessionCommandRecord;
  // Lookup key: target_turn_id || client_nonce. acceptTurn matches
  // against both PendingTurn.turnID and PendingTurn.clientNonce so the
  // bare-uuid vs. "turn_"-prefixed shapes (which both flow over the
  // wire for legacy reasons) resolve the same way.
  targetKey: string;
  receivedAtMs: number;
  // Holds the JetStream delivery alive until applyInterruptToTurn or
  // the orphan timer takes ownership of the ack.
  stopCommandHeartbeat: () => void;
  orphanTimer: ReturnType<typeof setTimeout>;
}

type InterruptOutcome = "interrupted" | "not_found" | "publish_failed";

// Defaults for the model + extended-thinking effort pinned into SDK
// Options when the first submit_turn arrives. The frontend's run-pane
// dropdown sends user-chosen values; these are the fallback for older
// clients (or programmatic submissions) that omit them. The model and
// effort enum live with the Anthropic SDK — the allowlist is enforced
// upstream in backend-go's middleware.validateEffort, so this layer
// trusts what lands on the wire and only applies a default when the
// field is empty.
//
// Keep in lockstep with:
//   - frontend/src/App.tsx CLAUDE_MODELS / CLAUDE_EFFORTS (UI surface)
//   - backend-go/cmd/tank-operator/middleware.go allowedClaudeEfforts
//     (server-side allowlist)
const DEFAULT_MODEL = "claude-opus-4-8";
const DEFAULT_EFFORT: EffortLevel = "high";

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

// INTERRUPT_BUFFER_MS bounds how long an interrupt_turn record can sit
// in pendingInterrupts waiting for a matching submit_turn before the
// runner gives up and emits turn.failed{interrupt_orphaned}. Sized well
// above worst-case data-plane queueing (one full in-flight turn) so a
// legitimate user-clicked-Stop-during-prior-turn race resolves
// naturally, and short enough that a genuinely orphaned interrupt
// surfaces as a durable failure inside a typical user attention window.
// 30s mirrors the JetStream control consumer's ack_wait headroom.
const INTERRUPT_BUFFER_MS = parsePositiveEnvInt(
  process.env.SESSION_INTERRUPT_BUFFER_MS,
  30_000,
);

// TERMINAL_PUBLISH_* bound how hard `applyInterruptToTurn` retries the
// durable terminal publish before falling back to
// turn.failed{publish_interrupt_failed}. The body of either event is
// tiny so the retry is cheap; max_payload_exceeded is deterministic, so
// retries don't help there and we want to surface the failure quickly.
// Transient JetStream connectivity blips do recover within a few hundred
// ms; the backoff is generous enough to span those.
const TERMINAL_PUBLISH_ATTEMPTS = parsePositiveEnvInt(
  process.env.SESSION_TERMINAL_PUBLISH_ATTEMPTS,
  3,
);
const TERMINAL_PUBLISH_BACKOFF_MS = parsePositiveEnvInt(
  process.env.SESSION_TERMINAL_PUBLISH_BACKOFF_MS,
  500,
);

// Before interrupting a Claude turn, ask the SDK to background any
// in-flight foreground Bash/subagent work. This mirrors Claude Code's Ctrl+B
// boundary: the active agent turn stops, but long-running shell work remains
// a visible session-level task. The deadline keeps Stop from waiting on the
// provider control plane.
const STOP_BACKGROUND_GRACE_MS = parsePositiveEnvInt(
  process.env.SESSION_STOP_BACKGROUND_GRACE_MS,
  250,
);

function parsePositiveEnvInt(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt((value ?? "").trim(), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export class Runner {
  private readonly sink: SessionEventSink;
  private readonly commandBus: SessionCommandBus;
  private readonly userQueue = new AsyncQueue<SDKUserMessage>();
  private readonly pendingTurns: PendingTurn[] = [];
  // pendingInterrupts holds interrupt_turn records that landed before
  // the matching submit_turn was dispatched on this runner. See the
  // BufferedInterrupt docstring for the race this buffer fixes.
  // Linear-scan; expected depth is 0–1 in steady state.
  private readonly pendingInterrupts: BufferedInterrupt[] = [];
  // Claude background shell tasks report their lifecycle through system
  // task_* frames. The start frame is turn-scoped, but progress and final
  // notification can arrive after the foreground turn has already completed.
  // Keep the owning turn by task_id and by originating tool_use_id so those
  // late frames still land on the durable session transcript.
  private readonly backgroundTaskTurns = new Map<string, ClaudeTurnContext>();
  private readonly backgroundToolUseTurns = new Map<string, ClaudeTurnContext>();
  private activeTurn: PendingTurn | null = null;
  private sdkQuery: Query | null = null;
  // Model + effort are pinned at pod boot from the first submit_turn
  // that arrives, with the DEFAULT_* fallbacks above for empty fields.
  // Once set, both are sealed for the runner's lifetime — the SDK's
  // Options object is consumed by query() at construction and cannot
  // be re-keyed without tearing the iterator down. Subsequent commands
  // whose model/effort differ are honored only for "what did the user
  // pick" metrics (optionsOverrideIgnoredTotal) and otherwise ignored.
  // The dropdown lock in the SPA reflects this contract so users don't
  // expect a mid-session switch to take effect.
  private pinnedModel: string | null = null;
  private pinnedEffort: EffortLevel | null = null;
  // sdkReady gates run()'s for-await loop on the first submit_turn
  // arriving so we can pin model/effort from that command's payload
  // before constructing query(). resolveSdkReady is called exactly once
  // by ensureSdkQuery; second-and-onward submit_turns hit the no-op
  // early-return.
  private readonly sdkReady: Promise<void>;
  private resolveSdkReady: () => void = () => {};

  constructor(private readonly cfg: Config) {
    this.sink = new SessionEventSink(cfg);
    this.commandBus = new SessionCommandBus(cfg, "claude");
    this.sdkReady = new Promise<void>((resolve) => {
      this.resolveSdkReady = resolve;
    });
  }

  // Run forever (or until externally aborted). Drives the SDK against
  // the user queue and fans events out to both sinks.
  async run(signal: AbortSignal): Promise<void> {
    // Two independent JetStream consumers: data plane (submit_turn,
    // input_reply — serial, ack-after-terminal) and control plane
    // (interrupt_turn — low-latency, never blocked by an in-flight turn).
    // See runner-shared/sessionBus.js for the consumer config split and
    // docs/tank-conversation-protocol.md → "Durable turn interruption"
    // for the contract. Don't fold these back into one consumer; that's
    // exactly the regression the split fixes.
    const stopConsumer = this.startCommandConsumer(signal);
    const stopControl = this.startControlConsumer(signal);
    const onAbort = () => {
      // Unblock sdkReady so the await below returns even if no turn ever
      // arrived. The signal.aborted check after the wait short-circuits
      // before query() is touched.
      this.resolveSdkReady();
      this.userQueue.close();
      this.sdkQuery?.interrupt();
    };
    signal.addEventListener("abort", onAbort, { once: true });
    try {
      // Block until the first submit_turn arrives (ensureSdkQuery resolves
      // sdkReady after pinning options and constructing query()), or until
      // the signal aborts. Without this, query() would launch with the
      // hardcoded defaults and the user's very-first model/effort pick
      // would be ignored — defeating the whole purpose of the dropdown.
      await this.sdkReady;
      if (signal.aborted || !this.sdkQuery) {
        return;
      }
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
      stopControl();
      if (signal.aborted) {
        await this.interruptActiveTurn("runner_shutdown");
      }
      this.userQueue.close();
    }
  }

  // ensureSdkQuery is the one-time pinning point for model + effort.
  // First call: read the command's model/effort (with DEFAULT_* fallback
  // when empty), build SDK Options, construct query(), and unblock
  // run()'s for-await loop. Subsequent calls: compare the incoming
  // values against the pinned ones and bump optionsOverrideIgnoredTotal
  // when they differ — the override is intentionally a no-op because
  // Options is sealed by the running query iterator.
  private ensureSdkQuery(record: SessionCommandRecord): void {
    const requestedModel = String(record.model ?? "").trim();
    const requestedEffort = String(record.effort ?? "").trim();
    if (this.sdkQuery !== null) {
      if (requestedModel && requestedModel !== this.pinnedModel) {
        optionsOverrideIgnoredTotal.labels("model").inc();
        console.warn(
          "session command requested model override; ignoring (model is pinned for the runner's lifetime)",
          { requested: requestedModel, pinned: this.pinnedModel },
        );
      }
      if (requestedEffort && requestedEffort !== this.pinnedEffort) {
        optionsOverrideIgnoredTotal.labels("effort").inc();
        console.warn(
          "session command requested effort override; ignoring (effort is pinned for the runner's lifetime)",
          { requested: requestedEffort, pinned: this.pinnedEffort },
        );
      }
      return;
    }

    const model = requestedModel || DEFAULT_MODEL;
    // Effort allowlist is enforced upstream in middleware.validateEffort;
    // any string that arrives here either matches EffortLevel or is the
    // empty string. The cast is therefore safe in the happy path, and a
    // wire-shape regression would surface as the SDK rejecting the value
    // (visible via providerErrorTotal{kind="query"}).
    const effort = (requestedEffort || DEFAULT_EFFORT) as EffortLevel;
    this.pinnedModel = model;
    this.pinnedEffort = effort;
    optionsPinnedTotal.labels(model, effort).inc();
    console.log(
      JSON.stringify({
        msg: "agent-runner pinning SDK options from first turn",
        model,
        effort,
        source_command_id: record.id,
      }),
    );

    const options: Options = {
      cwd: this.cfg.workspace,
      // The api-proxy injects OAuth from KV when the placeholder bearer
      // is seen — both the SDK and the raw CLI go through this path.
      //
      // permissionMode is `default` (not `bypassPermissions`) because
      // `canUseTool` is only invoked under permission-prompting modes;
      // `bypassPermissions` short-circuits the entire permission system
      // and means AskUserQuestion can never reach our gate. The
      // `canUseTool` callback below auto-allows everything except
      // AskUserQuestion, so non-AskUserQuestion tools retain the same
      // zero-friction shape as before.
      permissionMode: "default",
      // canUseTool gates AskUserQuestion on a durable input_reply
      // command. All other tools pass through unconditionally — see the
      // callback for the policy.
      canUseTool: this.canUseTool,
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
      model,
      effort,
    };

    this.sdkQuery = this.launchSdkQuery(options);
    void reportRuntimeConfig(this.cfg, { model, effort }).catch((err) => {
      console.warn("runtime config report failed:", err);
    });
    this.resolveSdkReady();
  }

  // launchSdkQuery wraps the SDK's query() construction in a method so
  // runner.test.ts can substitute a stub iterator without spawning the
  // real claude binary. The split has no observable runtime effect — the
  // production path is a single method call with no extra allocation.
  // Keep the method body trivial; the pinning + Options construction
  // belong in ensureSdkQuery so tests of *that* logic see the same code
  // path as production.
  private launchSdkQuery(options: Options): Query {
    return query({ prompt: this.userQueue, options });
  }

  private async handleEvent(message: SDKMessage): Promise<void> {
    const providerEvent = message as ClaudeProviderEvent;
    const activeTurn = await this.ensureActiveTurn(providerEvent);
    if (activeTurn?.terminalEmitted && !isClaudeTaskLifecycleMessage(providerEvent)) {
      if (providerEvent.type === "result" && this.activeTurn === activeTurn) {
        this.activeTurn = null;
      }
      return;
    }
    if (isClaudeRateLimitEvent(providerEvent)) {
      if (activeTurn) {
        await this.failTurnForProviderRateLimit(activeTurn, providerEvent);
        return;
      }
      providerRateLimitEventTotal.inc();
      logUnhandledSdkMessage(message);
      return;
    }
    const adapterTurn = this.turnContextForProviderEvent(providerEvent, activeTurn);
    if (isClaudeTaskLifecycleMessage(providerEvent) && !adapterTurn) {
      const { taskID, toolUseID } = claudeTaskIdentifiers(providerEvent);
      console.log(JSON.stringify({
        msg: "sdk_task_lifecycle_unbound",
        type: providerEvent.type,
        subtype: providerEvent.subtype,
        task_id: taskID,
        tool_use_id: toolUseID,
      }));
    }

    const canonicalEvents = canonicalEventsForClaudeMessage(
      this.cfg,
      adapterTurn,
      providerEvent,
    );
    if (canonicalEvents.length === 0) {
      logUnhandledSdkMessage(message);
    }

    for (const event of canonicalEvents) {
      this.rememberClaudeTaskOwner(event, adapterTurn ?? activeTurn);
      const dispatched = await dispatch(this.sink, event);
      if (event.type === "turn.completed" || event.type === "turn.failed" || event.type === "turn.interrupted") {
        if (dispatched && activeTurn) {
          activeTurn.terminalEmitted = true;
          if (activeTurn.commandRecord) {
            await this.markCommandTerminal(activeTurn, event.type);
          }
        }
      }
    }
    if (providerEvent.type === "result" && this.activeTurn === activeTurn) {
      this.activeTurn = null;
    }

    const wakeup = extractWakeup(message);
    if (wakeup) {
      await this.registerWakeup(wakeup, activeTurn?.turnID ?? "");
    }
  }

  private async failTurnForProviderRateLimit(
    turn: PendingTurn,
    message: ClaudeProviderEvent,
  ): Promise<void> {
    providerRateLimitEventTotal.inc();
    if (turn.terminalEmitted) return;
    const error = claudeRateLimitError(message);
    providerFailureClassTotal.labels("rate_limit").inc();
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.failed",
        reason: "provider_rate_limit",
        error,
        providerEventID: message.uuid,
      }),
    );
    if (!dispatched) return;
    turn.terminalEmitted = true;
    this.signalStopToSdk();
    await this.markCommandTerminal(turn, "turn.failed").catch((markErr) =>
      console.error("session command rate-limit terminal mark failed:", markErr),
    );
    if (this.activeTurn === turn) {
      this.activeTurn = null;
    }
  }

  private turnContextForProviderEvent(
    event: ClaudeProviderEvent,
    activeTurn: PendingTurn | null,
  ): ClaudeTurnContext | null {
    if (!isClaudeTaskLifecycleMessage(event)) {
      return activeTurn;
    }
    const { taskID, toolUseID } = claudeTaskIdentifiers(event);
    const owner =
      (taskID ? this.backgroundTaskTurns.get(taskID) : undefined) ??
      (toolUseID ? this.backgroundToolUseTurns.get(toolUseID) : undefined) ??
      (activeTurn ? this.snapshotTurnContext(activeTurn) : null);
    if (owner && taskID) this.backgroundTaskTurns.set(taskID, owner);
    if (owner && toolUseID) this.backgroundToolUseTurns.set(toolUseID, owner);
    return owner;
  }

  private rememberClaudeTaskOwner(
    event: TankConversationEvent,
    turn: ClaudeTurnContext | null,
  ): void {
    if (!turn) return;
    if (event.type === "item.started" && event.actor === "tool" && event.provider_item_id) {
      this.backgroundToolUseTurns.set(String(event.provider_item_id), this.snapshotTurnContext(turn));
      return;
    }
    if (
      event.type !== "shell_task.started" &&
      event.type !== "shell_task.updated" &&
      event.type !== "shell_task.exited"
    ) {
      return;
    }
    const owner = this.snapshotTurnContext(turn);
    const taskID =
      typeof event.task_id === "string" && event.task_id
        ? event.task_id
        : typeof event.payload?.task_id === "string"
          ? event.payload.task_id
          : "";
    if (taskID) this.backgroundTaskTurns.set(taskID, owner);
    const toolUseID =
      typeof event.payload?.tool_use_id === "string"
        ? event.payload.tool_use_id
        : typeof event.provider_item_id === "string" && event.provider_item_id !== taskID
          ? event.provider_item_id
          : "";
    if (toolUseID) this.backgroundToolUseTurns.set(toolUseID, owner);
  }

  private snapshotTurnContext(turn: ClaudeTurnContext): ClaudeTurnContext {
    return {
      turnID: turn.turnID,
      clientNonce: turn.clientNonce,
      interrupted: turn.interrupted,
      terminalEmitted: turn.terminalEmitted,
      finalAnswer: turn.finalAnswer,
    };
  }

  private startCommandConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startCommandConsumer(async (record) => {
        // Interrupts and input_reply MUST arrive via startControlConsumer
        // (separate JetStream consumer on the control subject). The
        // data-plane consumer has max_ack_pending=1 by design; any control
        // command delivered here would block behind the in-flight
        // submit_turn for the full duration of the turn — the exact
        // regression the split fixes. Stray control commands on the data
        // subject are either pre-cutover stragglers in the JetStream
        // replay buffer or a backend regression. The shared sessionBus
        // drops them with a structured warn before they reach this
        // handler; the explicit branch here is removed to keep the
        // dispatch table honest — the only branch that should ever fire
        // on data plane is submit_turn.
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

  // startControlConsumer drives the control-plane JetStream consumer.
  // Today: interrupt_turn + input_reply. Future low-latency control
  // signals (resume, cancel-with-reason, etc.) should land here as
  // additional branches, not on the data-plane consumer. input_reply
  // is control-plane because it resolves a canUseTool gate inside an
  // already-running submit_turn that is, by construction, holding the
  // data plane's single max_ack_pending slot — see
  // backend-go/internal/sessionbus/subjects.go → SubjectForCommand for
  // the publish-side reasoning that pairs with this consumer branch.
  private startControlConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startControlConsumer(async (record) => {
        if (isInterruptCommand(record)) {
          await this.acceptInterrupt(record);
          return;
        }
        if (isStopBackgroundTaskCommand(record)) {
          commandsConsumedTotal.labels("stop_background_task", "unsupported").inc();
          await this.commandBus.markFailed(
            record,
            new Error("background task stop is not supported by the Claude runner"),
          );
          return;
        }
        // Unknown control command type. Ack to clear the slot; log so
        // the producer-side surprise is visible. No retry — a
        // backend-only command type a runner doesn't recognise will
        // never start working on retry.
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
    // Drain any pre-arrived interrupts whose target matches this turn.
    // The control consumer can deliver interrupt_turn before the
    // data-plane consumer dispatches the matching submit_turn (the
    // planes don't synchronize past JetStream-level delivery, by
    // #511's design). Pre-#532 the runner returned "not_found"
    // silently and the stop click was lost; post-#532 the buffered
    // record drains here and is applied as a pre-SDK interrupt below.
    // See romaine-life/tank-operator#532 and BufferedInterrupt's docstring.
    const bufferedInterrupts = this.drainPendingInterruptsFor(pendingTurn);
    if (bufferedInterrupts.length > 0) {
      pendingTurn.interruptOnStart = bufferedInterrupts;
    }
    // Pin model + effort from the first submit_turn and construct the SDK
    // query() lazily so the user's dropdown pick is what actually drives
    // the model running in this pod. Second-and-onward calls are a no-op
    // here (the override is logged + counted inside ensureSdkQuery). MUST
    // happen before pushing onto userQueue: query() is what consumes the
    // queue, and a message landing while sdkQuery is still null would sit
    // unread until something else triggers ensureSdkQuery.
    //
    // We still pin model/effort even on the interrupt-on-start path:
    // the user's dropdown pick remains the right choice for the pod's
    // lifetime, even though we won't actually feed THIS turn into the
    // SDK below.
    this.ensureSdkQuery(record);
    pendingTurn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(record);
    this.pendingTurns.push(pendingTurn);
    if (pendingTurn.interruptOnStart && pendingTurn.interruptOnStart.length > 0) {
      // The SDK is never fed this turn. Emit turn.interrupted
      // synthetically for each buffered interrupt record (typically
      // one; double-Stop is rare but possible) so each interrupt_turn
      // command resolves to its own durable terminal-outcome bucket.
      // applyInterruptToTurn handles the case where the same turn was
      // already marked terminal by an earlier record in the loop via
      // its turn.terminalEmitted guard.
      for (const interruptRecord of pendingTurn.interruptOnStart) {
        await this.applyInterruptToTurn(
          interruptRecord,
          pendingTurn,
          "client_interrupt_before_start",
        );
      }
      return;
    }
    this.userQueue.push({
      type: "user",
      session_id: "",
      message: { role: "user", content: prompt },
      parent_tool_use_id: null,
    } as unknown as SDKUserMessage);
  }

  // acceptInterrupt is the single entry point from the control-plane
  // consumer for interrupt_turn commands. Its contract — pinned by
  // romaine-life/tank-operator#532 — is that EVERY accepted interrupt
  // resolves to exactly one terminal-outcome increment on
  // interruptOutcomeTotal within bounded time. No silent returns, no
  // markCompleted-without-emitting-a-terminal paths. The pre-#532 shape
  // had two silent strandings (race against submit_turn dispatch, and
  // dispatch-failure on the durable terminal); the buffer-and-apply
  // path here closes both.
  private async acceptInterrupt(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("interrupt_turn", "accepted").inc();
    const targetKey = String(record.target_turn_id ?? record.client_nonce ?? "").trim();
    if (!targetKey) {
      // Backend bug — the /interrupt handler MUST send target_turn_id
      // (it copies the URL path value into both target_turn_id and
      // client_nonce). Drop with a visible failure rather than the
      // pre-#532 silent ack so the regression surfaces.
      interruptOutcomeTotal.labels("invalid_target").inc();
      await this.commandBus.markFailed(
        record,
        new Error("interrupt_turn missing both target_turn_id and client_nonce"),
      );
      return;
    }
    const turn = this.findTurnForKey(targetKey);
    if (turn) {
      await this.applyInterruptToTurn(record, turn, "client_interrupt");
      return;
    }
    // No matching turn — buffer and wait. This is the resolution for
    // the post-#511 race: the control consumer delivers interrupt_turn
    // independent of data-plane queueing, so an early-stop click can
    // land on the runner before the submit_turn it targets has been
    // dispatched. Pre-#532 this returned "not_found" silently and the
    // user's stop was simply lost.
    this.bufferInterrupt(record, targetKey);
  }

  private findTurnForKey(key: string): PendingTurn | null {
    if (this.activeTurn && this.turnMatchesTarget(this.activeTurn, key)) {
      return this.activeTurn;
    }
    for (const turn of this.pendingTurns) {
      if (this.turnMatchesTarget(turn, key)) return turn;
    }
    return null;
  }

  // bufferInterrupt parks the interrupt_turn record until either
  // (a) the matching submit_turn arrives and acceptCommandTurn drains
  // the buffer into PendingTurn.interruptOnStart, or (b) the orphan
  // timer fires and we synthesize a turn.failed terminal so the UI
  // doesn't hang in "stopping". The JetStream working() heartbeat
  // keeps the message un-acked so a runner crash redelivers it; only
  // applyInterruptToTurn or expireBufferedInterrupt take ownership of
  // the ack.
  private bufferInterrupt(record: SessionCommandRecord, targetKey: string): void {
    interruptOutcomeTotal.labels("buffered").inc();
    const stopHeartbeat = this.commandBus.startCommandHeartbeat(record);
    const orphanTimer = setTimeout(() => {
      void this.expireBufferedInterrupt(record).catch((err) =>
        console.error("expireBufferedInterrupt failed:", err),
      );
    }, INTERRUPT_BUFFER_MS);
    // Unref so a buffered interrupt doesn't hold the event loop open
    // during runner shutdown. The signal-driven abort path drains the
    // buffer explicitly during shutdown.
    if (typeof (orphanTimer as { unref?: () => void }).unref === "function") {
      (orphanTimer as { unref: () => void }).unref();
    }
    this.pendingInterrupts.push({
      record,
      targetKey,
      receivedAtMs: Date.now(),
      stopCommandHeartbeat: stopHeartbeat,
      orphanTimer,
    });
  }

  // drainPendingInterruptsFor takes ownership of every buffered
  // interrupt whose targetKey matches `turn` (matching by both
  // PendingTurn.turnID and .clientNonce, since the two shapes coexist
  // on the wire). Returns the records so acceptCommandTurn can park
  // them on PendingTurn.interruptOnStart; the heartbeats are stopped
  // and the orphan timers cleared before return.
  private drainPendingInterruptsFor(turn: PendingTurn): SessionCommandRecord[] {
    if (this.pendingInterrupts.length === 0) return [];
    const drained: SessionCommandRecord[] = [];
    const remaining: BufferedInterrupt[] = [];
    for (const buf of this.pendingInterrupts) {
      if (this.turnMatchesTarget(turn, buf.targetKey)) {
        clearTimeout(buf.orphanTimer);
        buf.stopCommandHeartbeat();
        drained.push(buf.record);
      } else {
        remaining.push(buf);
      }
    }
    this.pendingInterrupts.length = 0;
    this.pendingInterrupts.push(...remaining);
    return drained;
  }

  private async expireBufferedInterrupt(record: SessionCommandRecord): Promise<void> {
    const idx = this.pendingInterrupts.findIndex((buf) => buf.record === record);
    if (idx < 0) return; // already drained by a submit_turn
    const buf = this.pendingInterrupts[idx]!;
    this.pendingInterrupts.splice(idx, 1);
    buf.stopCommandHeartbeat();
    // Synthesize a durable terminal for the targeted turn so the UI's
    // "stopping" projection resolves. The target turn never ran on this
    // runner; the turnID we publish is the canonical SDK-side form
    // derived from the targetKey so the event sits under the same
    // turn_id the frontend's interruptRequests entry was keyed on.
    const syntheticTurnID = buf.targetKey.startsWith("turn_")
      ? buf.targetKey
      : turnIDForClientNonce(buf.targetKey);
    const published = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: syntheticTurnID,
        clientNonce: buf.targetKey,
        source: "claude",
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

  // applyInterruptToTurn is the single point where an accepted
  // interrupt actually acts on the SDK and emits a durable terminal.
  // Order is deliberate and inverted from the pre-#532 shape:
  //
  //   1. Signal sdkQuery.interrupt() immediately. The promise is not
  //      awaited: Stop's user-visible terminal boundary is owned by Tank,
  //      not by the provider deciding when to acknowledge. Late foreground
  //      SDK frames are ignored after `terminalEmitted`; background task
  //      lifecycle frames still pass through shell_task.*.
  //   2. Dispatch turn.interrupted with bounded retry. On exhaustion,
  //      fall back to turn.failed{publish_interrupt_failed} so the UI
  //      always resolves to a durable terminal.
  //
  // reason="client_interrupt_before_start" branches into the
  // synthetic-terminal path: no SDK call is needed (the SDK was never
  // fed the prompt for this turn), we just publish the terminal.
  private async applyInterruptToTurn(
    record: SessionCommandRecord,
    turn: PendingTurn,
    reason: "client_interrupt" | "client_interrupt_before_start",
  ): Promise<void> {
    if (turn.terminalEmitted) {
      // Race with the natural turn termination. The durable ledger
      // shows the natural terminal; the UI's stopping projection
      // resolves via the existing race-resolution arm. Mark the
      // interrupt command complete; nothing more to do.
      interruptOutcomeTotal.labels("turn_already_terminal").inc();
      await this.commandBus.markCompleted(record);
      return;
    }
    turn.interrupted = true;
    if (reason === "client_interrupt") {
      this.signalStopToSdk();
    }
    const published = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.interrupted",
        reason,
      }),
    );
    if (published) {
      turn.terminalEmitted = true;
      if (turn.commandRecord) {
        await this.markCommandTerminal(turn, "turn.interrupted");
      }
      interruptOutcomeTotal
        .labels(reason === "client_interrupt_before_start" ? "terminated_pre_sdk" : "terminated_via_sdk")
        .inc();
      await this.commandBus.markCompleted(record);
      return;
    }
    // Durable turn.interrupted publish failed after retry. Fall back to
    // turn.failed so the UI's "stopping" projection still resolves
    // (conversationReducer.ts handles turn.failed → "error" status).
    // If THIS publish also fails, JetStream redelivery on the
    // interrupt_turn command will retry the whole flow on the next
    // ack_wait expiry — we don't try to make this case work in-process.
    const fallback = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.failed",
        reason: "publish_interrupt_failed",
      }),
    );
    if (fallback) {
      turn.terminalEmitted = true;
      if (turn.commandRecord) {
        await this.markCommandTerminal(turn, "turn.failed");
      }
    }
    interruptOutcomeTotal.labels("publish_failed").inc();
    await this.commandBus.markFailed(
      record,
      new Error("turn.interrupted publish failed after retry"),
    );
  }

  private signalStopToSdk(): void {
    const sdkQuery = this.sdkQuery;
    if (!sdkQuery) {
      providerControlTotal.labels("interrupt", "missing_query").inc();
      return;
    }

    let interruptSent = false;
    const sendInterrupt = (outcome: string) => {
      if (interruptSent) return;
      interruptSent = true;
      providerControlTotal.labels("interrupt", outcome).inc();
      try {
        const interruptPromise = sdkQuery.interrupt();
        void interruptPromise.catch((err) => {
          providerErrorTotal.labels("interrupt").inc();
          console.error("sdkQuery.interrupt() failed after Stop terminal was emitted:", err);
        });
      } catch (err) {
        providerErrorTotal.labels("interrupt").inc();
        console.error("sdkQuery.interrupt() failed; continuing with durable Stop terminal:", err);
      }
    };

    const backgroundTasks = (sdkQuery as Query & {
      backgroundTasks?: (toolUseId?: string) => Promise<boolean>;
    }).backgroundTasks;
    if (typeof backgroundTasks !== "function") {
      providerControlTotal.labels("background_tasks", "unsupported").inc();
      sendInterrupt("without_background_api");
      return;
    }

    const timer = setTimeout(() => {
      providerControlTotal.labels("background_tasks", "timeout").inc();
      sendInterrupt("background_timeout");
    }, STOP_BACKGROUND_GRACE_MS);
    if (typeof (timer as { unref?: () => void }).unref === "function") {
      (timer as { unref: () => void }).unref();
    }

    try {
      const backgroundPromise = backgroundTasks.call(sdkQuery);
      void backgroundPromise.then((backgrounded) => {
        clearTimeout(timer);
        providerControlTotal
          .labels("background_tasks", backgrounded ? "backgrounded" : "none")
          .inc();
        sendInterrupt(backgrounded ? "after_background" : "no_foreground_tasks");
      }).catch((err) => {
        clearTimeout(timer);
        providerControlTotal.labels("background_tasks", "failed").inc();
        providerErrorTotal.labels("background_tasks").inc();
        console.error("sdkQuery.backgroundTasks() failed before Stop interrupt:", err);
        sendInterrupt("background_failed");
      });
    } catch (err) {
      clearTimeout(timer);
      providerControlTotal.labels("background_tasks", "failed").inc();
      providerErrorTotal.labels("background_tasks").inc();
      console.error("sdkQuery.backgroundTasks() failed before Stop interrupt:", err);
      sendInterrupt("background_failed");
    }
  }

  private async publishTerminalWithRetry(event: TankConversationEvent): Promise<boolean> {
    for (let attempt = 0; attempt < TERMINAL_PUBLISH_ATTEMPTS; attempt++) {
      if (attempt > 0) {
        // Exponential backoff. JetStream client-side publish failures
        // (max_payload, etc.) are deterministic so retries don't help
        // there; transient connection blips do recover within a few
        // hundred ms.
        const delay = TERMINAL_PUBLISH_BACKOFF_MS * 2 ** (attempt - 1);
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      if (await dispatch(this.sink, event)) return true;
    }
    return false;
  }

  // canUseTool ends the asking turn when the agent invokes AskUserQuestion.
  // Non-AskUserQuestion tools auto-allow (preserving the prior
  // `bypassPermissions` posture). For AskUserQuestion we publish the durable
  // `turn.awaiting_input` handoff carrying the Tank-canonical questions, then
  // resolve `{deny, interrupt:true}` so the SDK closes the tool call and the
  // turn terminates. The user's answer arrives later as a brand-new turn via
  // POST /turns/{id}/answer — there is no in-turn tool result. See
  // docs/tank-conversation-protocol.md → "AskUserQuestion".
  private readonly canUseTool: CanUseTool = (toolName, input, { toolUseID }) => {
    if (toolName !== "AskUserQuestion") {
      return Promise.resolve({ behavior: "allow", updatedInput: input } satisfies PermissionResult);
    }
    const turn = this.activeTurn;
    if (!toolUseID || !turn) {
      return Promise.resolve({
        behavior: "deny",
        message: "AskUserQuestion cannot end the turn (missing tool_use_id or active turn)",
      } satisfies PermissionResult);
    }
    const questions = claudeQuestionsToTankShape(input);
    return this.endTurnAwaitingInput(turn, questions, toolUseID).then(
      () =>
        ({
          behavior: "deny",
          message: "Awaiting your answer in a new turn.",
          interrupt: true,
        }) satisfies PermissionResult,
    );
  };

  // endTurnAwaitingInput publishes the durable turn.awaiting_input terminal
  // for an AskUserQuestion handoff and acks the asking turn's submit_turn.
  // Mirrors applyInterruptToTurn's durable-first posture: publish with retry,
  // and fall back to turn.failed if the handoff publish ultimately fails so
  // the turn never strands without a terminal.
  private async endTurnAwaitingInput(
    turn: PendingTurn,
    questions: unknown,
    providerItemID: string,
  ): Promise<void> {
    if (turn.terminalEmitted) return;
    const timelineID = itemTimelineID(turn.turnID, providerItemID);
    const published = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.awaiting_input",
        questions,
        awaitingProviderItemID: providerItemID,
        awaitingTimelineID: timelineID,
      }),
    );
    if (published) {
      turn.terminalEmitted = true;
      if (turn.commandRecord) {
        await this.markCommandTerminal(turn, "turn.awaiting_input");
      }
      return;
    }
    const fallback = await this.publishTerminalWithRetry(
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.failed",
        reason: "publish_awaiting_input_failed",
      }),
    );
    if (fallback) {
      turn.terminalEmitted = true;
      if (turn.commandRecord) {
        await this.markCommandTerminal(turn, "turn.failed");
      }
    }
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

  // interruptActiveTurn is now only used by the runner-shutdown path
  // (run()'s finally block on signal abort). Client-driven interrupts
  // flow through acceptInterrupt → applyInterruptToTurn directly so
  // the four-outcome contract on interruptOutcomeTotal applies.
  //
  // Returns the InterruptOutcome the shutdown caller doesn't actually
  // read — kept on the signature to make the existing call site
  // self-documenting. The body emits the durable terminal best-effort;
  // shutdown is synchronous past the await, so publish-failed at this
  // stage is unrecoverable in-process and falls to JetStream
  // redelivery on the next runner-process boot.
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

  private async failActiveCommandTurn(err: unknown): Promise<void> {
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (!turn?.commandRecord) return;
    if (!turn.terminalEmitted) {
      const message = err instanceof Error ? err.message : String(err);
      // Classify the provider failure before it becomes an opaque
      // turn.failed terminal. `thinking_block_modified` is the regression
      // sentinel for the extended-thinking resume bug (session 340); it
      // must stay at zero after the SDK ^0.3.158 bump.
      providerFailureClassTotal.labels(classifyProviderFailure(message)).inc();
      const dispatched = await dispatch(
        this.sink,
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "claude",
          type: "turn.failed",
          reason: "provider_failure",
          error: message,
        }),
      );
      if (!dispatched) return;
      turn.terminalEmitted = true;
    }
    await this.markCommandTerminal(turn, "turn.failed").catch((markErr) =>
      console.error("session command failure mark failed:", markErr, "original:", err),
    );
  }

  private async registerWakeup(req: WakeupRequest, scheduledTurnID: string): Promise<void> {
    try {
      const registered = await registerScheduledWakeup(this.cfg, {
        delayMs: req.delayMs,
        prompt: req.prompt,
        providerItemID: req.providerItemID,
        scheduledTurnID,
      });
      scheduledWakeupRegisterTotal.labels(registered ? "ok" : "disabled").inc();
    } catch (err) {
      scheduledWakeupRegisterTotal.labels("failed").inc();
      console.error("scheduled wakeup register failed:", err);
    }
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
