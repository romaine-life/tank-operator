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
  type CanUseTool,
  type EffortLevel,
  type PermissionResult,
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
  askUserQuestionPendingGauge,
  askUserQuestionWaitSeconds,
  commandsConsumedTotal,
  natsPublishFailureTotal,
  optionsOverrideIgnoredTotal,
  optionsPinnedTotal,
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

// logUnhandledSdkMessage emits a structured JSON log line for SDK messages
// whose `type` is not one the adapter converts into Tank conversation
// events. canonicalEventsForClaudeMessage handles only "assistant", "user",
// and "result"; "stream_event" is the partial-typing surface and is
// intentionally noisy, so we skip it too. Everything else — task lifecycle
// (system/task_started, system/task_progress, system/task_notification,
// system/task_updated), hooks, status changes, plugin installs — currently
// has no observable surface in tank's session_events or the UI. This log
// is the cheapest path to learning what the SDK is reporting that we drop:
// once a Monitor-stuck session reproduces, `kubectl logs -c agent-runner
// | jq 'select(.msg=="sdk_message_unhandled")'` shows the SDK's verdict
// (e.g. subtype=task_notification, status=failed) without a schema change.
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

export function logUnhandledSdkMessage(message: SDKMessage): void {
  const m = message as Record<string, unknown> & { type?: unknown };
  const type = typeof m.type === "string" ? m.type : "";
  if (
    type === "assistant" ||
    type === "user" ||
    type === "result" ||
    type === "stream_event"
  ) {
    return;
  }
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

// PendingInputReply tracks a single in-flight AskUserQuestion call. Either
// side of the round-trip can arrive first:
//
//   1. SDK fires `canUseTool` for AskUserQuestion → we create the entry
//      with `resolve`, `reject`, and `input` (the original tool_use input
//      that carries the questions[] schema). The SDK awaits the returned
//      Promise.
//   2. Durable JetStream `input_reply` command arrives → we look up the
//      entry, resolve canUseTool with `{behavior:"allow", updatedInput}`
//      that carries the user's selections, and attach the JetStream
//      record + heartbeat for ack on `tool.approval_resolved`.
//
// On `tool.approval_resolved` emission (markInputReplyCompleted) we ack
// the durable command and remove the entry. On turn abort or runner
// shutdown we reject any still-pending canUseTool resolvers so the SDK
// surfaces a denied tool_result instead of hanging the turn forever.
interface PendingInputReply {
  resolve?: (result: PermissionResult) => void;
  reject?: (err: unknown) => void;
  // Original AskUserQuestion tool input (the `{questions: [...]}` shape).
  // Needed when resolving canUseTool because the SDK's
  // mapToolResultToToolResultBlockParam reads `questions` + `answers` +
  // `annotations` off updatedInput to format the canonical tool_result.
  input?: Record<string, unknown>;
  record?: SessionCommandRecord;
  stopCommandHeartbeat?: () => void;
  // Time the canUseTool callback fired, used to observe wait-time when
  // the input_reply arrives.
  requestedAtMs?: number;
  // Tracks which turn the AskUserQuestion belongs to so we can fail-fast
  // pending replies on turn interrupt without scanning every record.
  turnID?: string;
  clientNonce?: string;
}

type InterruptOutcome = "interrupted" | "not_found" | "publish_failed";

export function inputReplyTargetProviderItemID(record: SessionCommandRecord): string {
  return String(record.target_provider_item_id ?? "").trim();
}

// inputReplyAnswers reads the durable command's per-question selections.
// Single-select questions arrive as one-element arrays; multi-select as
// arrays of the checked labels. Empty entries are dropped so the SDK
// updatedInput.answers map only contains question→non-empty-string.
export function inputReplyAnswers(record: SessionCommandRecord): Record<string, string[]> {
  const raw = record.answers;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Record<string, string[]> = {};
  for (const [question, value] of Object.entries(raw as Record<string, unknown>)) {
    const trimmedQuestion = String(question).trim();
    if (!trimmedQuestion) continue;
    if (!Array.isArray(value)) continue;
    const cleaned = value
      .map((label) => String(label ?? "").trim())
      .filter((label) => label.length > 0);
    if (cleaned.length > 0) out[trimmedQuestion] = cleaned;
  }
  return out;
}

interface InputReplyAnnotation {
  preview?: string;
  notes?: string;
}

export function inputReplyAnnotations(
  record: SessionCommandRecord,
): Record<string, InputReplyAnnotation> {
  const raw = record.annotations;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Record<string, InputReplyAnnotation> = {};
  for (const [question, value] of Object.entries(raw as Record<string, unknown>)) {
    const trimmedQuestion = String(question).trim();
    if (!trimmedQuestion) continue;
    if (!value || typeof value !== "object") continue;
    const ann = value as { preview?: unknown; notes?: unknown };
    const cleaned: InputReplyAnnotation = {};
    if (typeof ann.preview === "string" && ann.preview.trim()) {
      cleaned.preview = ann.preview.trim();
    }
    if (typeof ann.notes === "string" && ann.notes.trim()) {
      cleaned.notes = ann.notes.trim();
    }
    if (cleaned.preview || cleaned.notes) out[trimmedQuestion] = cleaned;
  }
  return out;
}

// joinAnswersForSDK converts the durable shape (question → labels[]) into
// the shape the Claude Agent SDK's AskUserQuestion tool expects on
// `updatedInput.answers` (question → comma-joined string). The SDK's own
// zod preprocess does the same join for arrays:
//   `Array.isArray(H) && H.every($=>typeof $==="string") ? H.join(", ") : H`
// — so we match that contract directly.
export function joinAnswersForSDK(answers: Record<string, string[]>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [question, labels] of Object.entries(answers)) {
    out[question] = labels.join(", ");
  }
  return out;
}

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
const DEFAULT_MODEL = "claude-opus-4-7";
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

export class Runner {
  private readonly sink: SessionEventSink;
  private readonly commandBus: SessionCommandBus;
  private readonly userQueue = new AsyncQueue<SDKUserMessage>();
  private readonly pendingTurns: PendingTurn[] = [];
  private readonly needsInputProviderItemIDs = new Set<string>();
  private readonly pendingInputReplies = new Map<string, PendingInputReply>();
  // resolvedInputReplies carries the answers/annotations that resolved
  // each AskUserQuestion call. The adapter reads + drains this map when
  // it builds the matching `tool.approval_resolved` event so the durable
  // payload mirrors what the user actually selected. Process-local state
  // is acceptable here because the canUseTool resolver is also
  // process-local — both vanish together on runner restart, and the
  // SDK's `continue: true` replays the unanswered tool_use so a fresh
  // canUseTool fires.
  private readonly resolvedInputReplies = new Map<
    string,
    { answers: Record<string, string[]>; annotations: Record<string, InputReplyAnnotation> }
  >();
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
      this.failAllPendingInputReplies(new Error("runner_shutdown"));
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
    // Claude SDK events are adapter inputs, not bus content. The adapter
    // converts them into Tank conversation events; only those reach the
    // durable session bus. Anything the adapter does not understand
    // (task lifecycle, hooks, status, etc.) is logged via
    // logUnhandledSdkMessage so the diagnostic surface exists in kubectl
    // logs without growing the durable event schema.
    logUnhandledSdkMessage(message);
    const providerEvent = message as ClaudeProviderEvent;
    const activeTurn = await this.ensureActiveTurn(providerEvent);

    for (const event of canonicalEventsForClaudeMessage(
      this.cfg,
      activeTurn,
      providerEvent,
      this.needsInputProviderItemIDs,
      this.resolvedInputReplies,
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
        if (isInputReplyCommand(record)) {
          await this.acceptInputReply(record);
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
    // Pin model + effort from the first submit_turn and construct the SDK
    // query() lazily so the user's dropdown pick is what actually drives
    // the model running in this pod. Second-and-onward calls are a no-op
    // here (the override is logged + counted inside ensureSdkQuery). MUST
    // happen before pushing onto userQueue: query() is what consumes the
    // queue, and a message landing while sdkQuery is still null would sit
    // unread until something else triggers ensureSdkQuery.
    this.ensureSdkQuery(record);
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
      // Await the SDK's interrupt() — it sends a {subtype: "interrupt"}
      // control-plane request to the CLI subprocess and returns when the
      // CLI has acked. Dropping the promise (the prior shape) meant we
      // marked the command "completed" before the CLI had stopped, and
      // any async rejection became an unhandled rejection instead of
      // hitting the providerErrorTotal counter.
      try {
        await this.sdkQuery?.interrupt();
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

  // canUseTool is the SDK-blessed answer-injection point. The SDK calls
  // this for every tool the model wants to invoke. Non-AskUserQuestion
  // tools auto-allow (preserving the prior `bypassPermissions` posture).
  // AskUserQuestion calls block until the durable `input_reply` command
  // arrives, then resolve with `{behavior:"allow", updatedInput}` where
  // `updatedInput.answers` carries the user's selections. The SDK then
  // calls the tool's own `call()` to produce the canonical tool_result —
  // we never synthesize one ourselves.
  private readonly canUseTool: CanUseTool = (toolName, input, { toolUseID, signal }) => {
    if (toolName !== "AskUserQuestion") {
      return Promise.resolve({ behavior: "allow", updatedInput: input } satisfies PermissionResult);
    }
    if (!toolUseID) {
      return Promise.resolve({
        behavior: "deny",
        message: "AskUserQuestion missing tool_use_id; cannot route input_reply",
      } satisfies PermissionResult);
    }

    // The adapter also tracks this set independently (it consumes raw
    // tool_use events). We populate it here too so durable command
    // delivery can detect "AskUserQuestion is waiting" without racing
    // the assistant message stream.
    this.needsInputProviderItemIDs.add(toolUseID);

    return new Promise<PermissionResult>((resolve, reject) => {
      const existing = this.pendingInputReplies.get(toolUseID);
      if (existing?.record) {
        // The durable command arrived before canUseTool fired (rare —
        // implies the SDK delayed firing canUseTool past command
        // delivery). Resolve immediately using the queued record.
        const answers = inputReplyAnswers(existing.record);
        const annotations = inputReplyAnnotations(existing.record);
        this.resolvedInputReplies.set(toolUseID, { answers, annotations });
        this.observeInputReplyResolution(Date.now());
        resolve({
          behavior: "allow",
          updatedInput: {
            ...input,
            answers: joinAnswersForSDK(answers),
            ...(Object.keys(annotations).length > 0 ? { annotations } : {}),
          },
        });
        return;
      }
      const entry: PendingInputReply = {
        ...(existing ?? {}),
        resolve,
        reject,
        input,
        requestedAtMs: Date.now(),
        turnID: this.activeTurn?.turnID,
        clientNonce: this.activeTurn?.clientNonce,
      };
      this.pendingInputReplies.set(toolUseID, entry);
      askUserQuestionPendingGauge.inc();

      // If the SDK aborts the operation (turn interrupt, runner
      // shutdown), reject with deny so the SDK records a denied
      // tool_result and the turn unwinds cleanly.
      const onAbort = () => {
        const stillPending = this.pendingInputReplies.get(toolUseID);
        if (!stillPending || stillPending.resolve !== resolve) return;
        this.pendingInputReplies.delete(toolUseID);
        this.needsInputProviderItemIDs.delete(toolUseID);
        askUserQuestionPendingGauge.dec();
        if (stillPending.record) {
          void this.commandBus.markFailed(stillPending.record, new Error("turn interrupted"));
        }
        resolve({ behavior: "deny", message: "session interrupted", interrupt: true });
      };
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  };

  private async acceptInputReply(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("input_reply", "accepted").inc();
    const targetProviderItemID = inputReplyTargetProviderItemID(record);
    const answers = inputReplyAnswers(record);
    if (!targetProviderItemID || Object.keys(answers).length === 0) {
      commandsConsumedTotal.labels("input_reply", "invalid").inc();
      await this.commandBus.markFailed(
        record,
        new Error("input reply missing target or answers"),
      );
      return;
    }
    if (
      !this.activeTurn ||
      !this.turnMatchesTarget(this.activeTurn, record.target_turn_id || record.client_nonce)
    ) {
      commandsConsumedTotal.labels("input_reply", "no_active_turn").inc();
      await this.commandBus.markFailed(record, new Error("input reply target turn is not active"));
      return;
    }
    const pending = this.pendingInputReplies.get(targetProviderItemID);
    if (!pending || !pending.resolve) {
      // canUseTool hasn't fired yet (or the AskUserQuestion call already
      // resolved). Stash the record so a late canUseTool can consume it,
      // but only if the adapter has confirmed the item is awaiting
      // input. Otherwise this is a stale reply for an already-resolved
      // tool call.
      if (!this.needsInputProviderItemIDs.has(targetProviderItemID)) {
        commandsConsumedTotal.labels("input_reply", "not_waiting_for_input").inc();
        await this.commandBus.markFailed(
          record,
          new Error("input reply target is not waiting for input"),
        );
        return;
      }
      const entry: PendingInputReply = {
        ...(pending ?? {}),
        record,
        stopCommandHeartbeat: this.commandBus.startCommandHeartbeat(record),
      };
      this.pendingInputReplies.set(targetProviderItemID, entry);
      return;
    }
    if (pending.record) {
      commandsConsumedTotal.labels("input_reply", "duplicate").inc();
      await this.commandBus.markFailed(record, new Error("input reply already pending for target"));
      return;
    }

    const annotations = inputReplyAnnotations(record);
    pending.record = record;
    pending.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(record);
    this.resolvedInputReplies.set(targetProviderItemID, { answers, annotations });
    this.observeInputReplyResolution(pending.requestedAtMs);

    const resolve = pending.resolve;
    pending.resolve = undefined;
    pending.reject = undefined;
    askUserQuestionPendingGauge.dec();
    resolve({
      behavior: "allow",
      updatedInput: {
        ...(pending.input ?? {}),
        answers: joinAnswersForSDK(answers),
        ...(Object.keys(annotations).length > 0 ? { annotations } : {}),
      },
    });
  }

  private observeInputReplyResolution(requestedAtMs?: number): void {
    if (!requestedAtMs) return;
    askUserQuestionWaitSeconds.observe(Math.max(0, Date.now() - requestedAtMs) / 1000);
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
    this.needsInputProviderItemIDs.delete(providerItemID);
    pending.stopCommandHeartbeat?.();
    if (pending.resolve) {
      // Defensive: tool.approval_resolved fired before canUseTool was
      // resolved (e.g., model produced a tool_result without taking the
      // permission path). Resolve the dangling promise with deny so the
      // SDK doesn't leak it.
      askUserQuestionPendingGauge.dec();
      pending.resolve({ behavior: "deny", message: "tool resolved without input reply" });
    }
    if (!pending.record) return;
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
      const turnMatchesPending = pending.turnID
        ? this.turnMatchesTarget(turn, pending.turnID)
        : pending.record
        ? this.turnMatchesTarget(turn, pending.record.target_turn_id || pending.record.client_nonce)
        : true;
      if (!turnMatchesPending) continue;
      this.pendingInputReplies.delete(providerItemID);
      this.needsInputProviderItemIDs.delete(providerItemID);
      pending.stopCommandHeartbeat?.();
      if (pending.resolve) {
        askUserQuestionPendingGauge.dec();
        pending.resolve({
          behavior: "deny",
          message: err instanceof Error ? err.message : String(err),
          interrupt: true,
        });
      }
      if (pending.record) {
        try {
          await this.commandBus.markFailed(pending.record, err);
        } catch (markErr) {
          console.error("input reply failure mark failed:", markErr);
        }
      }
    }
  }

  // failAllPendingInputReplies is called on runner shutdown. Unlike the
  // per-turn variant it does not call into the async session bus
  // (`markFailed` is fire-and-forget) because shutdown is synchronous
  // and we must release SDK Promises before query.interrupt() returns.
  private failAllPendingInputReplies(err: unknown): void {
    for (const [providerItemID, pending] of [...this.pendingInputReplies.entries()]) {
      this.pendingInputReplies.delete(providerItemID);
      this.needsInputProviderItemIDs.delete(providerItemID);
      pending.stopCommandHeartbeat?.();
      if (pending.resolve) {
        askUserQuestionPendingGauge.dec();
        pending.resolve({
          behavior: "deny",
          message: err instanceof Error ? err.message : String(err),
          interrupt: true,
        });
      }
      if (pending.record) {
        void this.commandBus.markFailed(pending.record, err).catch((markErr) => {
          console.error("input reply shutdown mark failed:", markErr);
        });
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
