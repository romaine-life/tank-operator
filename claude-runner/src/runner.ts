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

import { readFileSync } from "node:fs";
import {
  createSdkMcpServer,
  query,
  type EffortLevel,
  type McpServerConfig,
  type Query,
  type SDKMessage,
  type SDKUserMessage,
  type Options,
  tool,
} from "@anthropic-ai/claude-agent-sdk";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import {
  canonicalEventsForClaudeMessage,
  claudeQuestionsToTankShape,
  claudeTaskIdentifiers,
  claudeTerminalBackgroundTask,
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
  askUserQuestionHandoffEvents,
  itemEvent,
  itemTimelineID,
  shellTaskEvent,
  stampTankEvent,
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
  backgroundTaskWakeTotal,
  commandsConsumedTotal,
  eventTruncatedTotal,
  inputReplyAnswerShapeTotal,
  interruptOutcomeTotal,
  natsPublishFailureTotal,
  optionsOverrideIgnoredTotal,
  optionsPinnedTotal,
  providerApiRetryTotal,
  providerControlTotal,
  providerErrorTotal,
  providerFailureClassTotal,
  providerRateLimitDecisionTotal,
  providerRateLimitEventTotal,
  recordTurnPreStartLatency,
  recordTurnStart,
  recordTurnTerminal,
  scheduledWakeupRegisterTotal,
  toolPermissionDeniedTotal,
  unmappedProviderEventTotal,
  terminalPublishDeferredTotal,
  inputReplyRecoveryTotal,
  askUserQuestionDismissedTotal,
} from "./metrics.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";
import { registerScheduledWakeup } from "../../runner-shared/scheduledWakeup.js";
import {
  cancelBackgroundTaskWake,
  fetchUnresolvedBackgroundTasks,
  registerBackgroundTaskWake,
} from "../../runner-shared/backgroundTaskWake.js";

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
  attempts = DURABLE_PUBLISH_ATTEMPTS,
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
    const severity = sizeGuard.payloadDropped
      ? "payload-dropped"
      : "strings-truncated";
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
  // Bounded in-place retry (issue #1078, "non-terminal durable events are
  // fire-once"): a transient publish failure used to hole the ledger
  // silently for every non-terminal event. Ordering is safe — the order_key
  // was stamped above, so a retried event still sorts where it was created
  // even if a later event lands first. Terminal publishes pass attempts=1
  // because publishTerminalWithRetry owns their (longer) retry schedule.
  for (let attempt = 0; ; attempt++) {
    try {
      await sink.upsert(sizeGuard.event as unknown as StampedTankEvent);
      return true;
    } catch (err) {
      console.error("session bus publish failed:", err);
      natsPublishFailureTotal.inc();
      if (attempt >= attempts - 1) return false;
      const delay = DURABLE_PUBLISH_BACKOFF_MS * 2 ** attempt;
      await new Promise((resolve) => setTimeout(resolve, delay));
    }
  }
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
  if (
    m.includes("rate limit") ||
    m.includes("rate_limit") ||
    m.includes(" 429")
  ) {
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

// classifyApiRetryError maps a Claude SDK system/api_retry frame's `error`
// field to a closed, low-cardinality set for providerApiRetryTotal. The SDK
// emits short tokens ("rate_limit", "overloaded", …); anything unrecognized
// folds to "other" so a provider upgrade can't blow up the label set.
export function classifyApiRetryError(
  raw: unknown,
): "rate_limit" | "overloaded" | "api_error" | "other" {
  const m = String(raw ?? "").toLowerCase();
  if (
    m.includes("rate_limit") ||
    m.includes("rate limit") ||
    m.includes("429")
  ) {
    return "rate_limit";
  }
  if (m.includes("overload")) return "overloaded";
  if (
    m.includes("api_error") ||
    m.includes("api error") ||
    m.includes("500") ||
    m.includes("502") ||
    m.includes("503") ||
    m.includes("529")
  ) {
    return "api_error";
  }
  return "other";
}

// pickContextWindowFromModelUsage extracts the model's context window from the
// Claude Agent SDK `result` message's per-model usage map
// (SDKResultMessage.modelUsage: Record<string, ModelUsage>, where each
// ModelUsage carries `contextWindow: number`). Returns the max positive finite
// window across all entries, or null when the map is missing/empty or every
// entry's window is missing/zero/negative/non-finite — callers must treat null
// as "don't report", never as a zero window. Kept as a pure exported function
// so the extraction is unit-tested without driving the SDK (see runner.test.ts).
//
// Why ModelUsage and not the Anthropic Models API: `GET /v1/models/{model}`
// returns HTTP 401 under the session's subscription-OAuth proxy, so Claude
// sessions never got a window from that path. The SDK's `system`/`init` message
// (SDKSystemMessage, subtype "init") carries model/tools/skills but NO
// context-window field; `contextWindow` only appears on per-turn ModelUsage
// attached to the `result` message — observed, no HTTP/auth round-trip.
export function pickContextWindowFromModelUsage(
  modelUsage: Record<string, { contextWindow?: number }> | undefined,
): number | null {
  if (!modelUsage || typeof modelUsage !== "object") return null;
  let best = 0;
  for (const usage of Object.values(modelUsage)) {
    const raw = usage?.contextWindow;
    const n = typeof raw === "number" ? raw : Number.NaN;
    if (Number.isFinite(n) && n > best) best = n;
  }
  if (best <= 0) return null;
  return Math.floor(best);
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
    (type === "system" &&
      (subtype === "init" || subtype === "compact_boundary"))
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

const CLAUDE_RATE_LIMIT_INFO_KEYS = [
  "provider",
  "status",
  "rateLimitType",
  "resetsAt",
  "utilization",
  "overageStatus",
  "overageResetsAt",
  "overageDisabledReason",
  "isUsingOverage",
  "surpassedThreshold",
  "uuid",
  "session_id",
] as const;

export function claudeRateLimitInfo(
  message: ClaudeProviderEvent,
): Record<string, unknown> | null {
  const raw = message.rate_limit_info;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }
  const source = raw as Record<string, unknown>;
  const out: Record<string, unknown> = { provider: "claude" };
  for (const key of CLAUDE_RATE_LIMIT_INFO_KEYS) {
    const value = source[key];
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (trimmed) out[key] = trimmed.slice(0, 512);
    } else if (typeof value === "number" && Number.isFinite(value)) {
      out[key] = value;
    } else if (typeof value === "boolean") {
      out[key] = value;
    }
  }
  if (typeof message.uuid === "string" && message.uuid.trim()) {
    out.uuid = message.uuid.trim().slice(0, 512);
  }
  if (typeof message.session_id === "string" && message.session_id.trim()) {
    out.session_id = message.session_id.trim().slice(0, 512);
  }
  return Object.keys(out).length > 1 ? out : null;
}

function normalizedLimitStatus(value: unknown): string {
  return typeof value === "string" ? value.trim().toLowerCase().replace(/[\s-]+/g, "_") : "";
}

export function claudeRateLimitEventIsTerminal(message: ClaudeProviderEvent): boolean {
  const rateLimitInfo = claudeRateLimitInfo(message);
  const status = normalizedLimitStatus(rateLimitInfo?.status);
  if (status === "allowed" || status === "ok") {
    return false;
  }
  if (
    status.includes("reject") ||
    status.includes("exhaust") ||
    status.includes("limit") ||
    status.includes("exceed") ||
    status.includes("block")
  ) {
    return true;
  }
  for (const key of ["retry_after_ms", "retry_after_seconds", "message", "error", "summary", "description"]) {
    const value = message[key];
    if ((typeof value === "string" && value.trim()) || (typeof value === "number" && Number.isFinite(value))) {
      return true;
    }
  }
  return !rateLimitInfo;
}

function claudeRateLimitError(message: ClaudeProviderEvent): string {
  const parts = ["Claude provider emitted rate_limit_event"];
  for (const key of [
    "message",
    "error",
    "summary",
    "description",
    "retry_after_ms",
    "retry_after_seconds",
  ]) {
    const value = message[key];
    if (typeof value === "string" && value.trim()) {
      parts.push(`${key}=${value.trim()}`);
    } else if (typeof value === "number" && Number.isFinite(value)) {
      parts.push(`${key}=${value}`);
    }
  }
  const rateLimitInfo = claudeRateLimitInfo(message);
  if (rateLimitInfo) {
    for (const key of [
      "status",
      "rateLimitType",
      "resetsAt",
      "overageStatus",
      "overageResetsAt",
    ]) {
      const value = rateLimitInfo[key];
      if (
        typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean"
      ) {
        parts.push(`${key}=${value}`);
      }
    }
  }
  return parts.join("; ");
}

function redactSdkStderrLine(line: string): string {
  return line
    .replace(/\bsk-ant-[A-Za-z0-9_-]+/g, "[redacted-api-key]")
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, "Bearer [redacted]")
    .replace(
      /("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|authorization)"?\s*[:=]\s*)("[^"]+"|[^\s,;]+)/gi,
      "$1[redacted]",
    );
}

function inputReplyKey(
  turnID: string,
  timelineID: string,
  providerItemID: string,
): string {
  return `${turnID}\x1f${timelineID}\x1f${providerItemID}`;
}

function answersForClaudeInput(
  answers: Record<string, string[]> | undefined,
  annotations: Record<string, { notes?: string }> | undefined,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [question, labels] of Object.entries(answers ?? {})) {
    const cleanLabels = labels
      .map((label) => String(label).trim())
      .filter(Boolean);
    const note = String(annotations?.[question]?.notes ?? "").trim();
    const semanticLabels = note
      ? cleanLabels.filter((label) => label.toLowerCase() !== "other")
      : cleanLabels;
    inputReplyAnswerShapeTotal
      .labels(inputReplyAnswerShape(semanticLabels, note))
      .inc();
    const value =
      semanticLabels.length > 0 && note
        ? `${semanticLabels.join(", ")}\n\n${note}`
        : semanticLabels.length > 0
          ? semanticLabels.join(", ")
          : note;
    if (value) out[question] = value;
  }
  return out;
}

function askUserQuestionToolResult(
  answers: Record<string, string>,
): CallToolResult {
  const lines = Object.entries(answers).map(
    ([question, answer]) => `${question}\n${answer}`,
  );
  return {
    content: [
      {
        type: "text",
        text:
          lines.length > 0
            ? `User answered:\n\n${lines.join("\n\n")}`
            : "User answered with no selected options or notes.",
      },
    ],
    structuredContent: { answers },
    _meta: { tankAskUserQuestion: true },
  };
}

type InputReplyAnswerShape =
  | "selection_only"
  | "free_form_only"
  | "selection_with_notes"
  | "empty";

function inputReplyAnswerShape(
  labels: string[],
  note: string,
): InputReplyAnswerShape {
  if (labels.length > 0 && note) return "selection_with_notes";
  if (note) return "free_form_only";
  if (labels.length > 0) return "selection_only";
  return "empty";
}

export interface PendingTurn {
  turnID: string;
  clientNonce: string;
  text: string;
  commandCreatedAtMs?: number;
  claimedAtMs?: number;
  started: boolean;
  interrupted: boolean;
  terminalEmitted: boolean;
  finalAnswer?: ClaudeTurnContext["finalAnswer"];
  commandRecord?: SessionCommandRecord;
  stopCommandHeartbeat?: () => void;
  // pendingTerminal parks a turn terminal whose publish exhausted retries
  // (issue #1078 item 1). The submit_turn command is NAK'd; on JetStream
  // redelivery acceptCommandTurn's reattach path retries the PUBLISH of
  // this exact event instead of re-running the prompt.
  pendingTerminal?: TankConversationEvent;
  pendingTerminalType?: "turn.completed" | "turn.failed" | "turn.interrupted";
  // Identities this turn answered to before AskUserQuestion rotations
  // (issue #1078 item 6): a redelivery of the ORIGINAL submit_turn must
  // still find the rotated turn instead of re-running the prompt.
  priorIdentities?: string[];
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

type PendingInputReply = {
  turn: PendingTurn;
  providerItemID: string;
  timelineID: string;
  // Question-shell identity (issue #1078 item 2): the SPA's Stop targets
  // activity.active_turn_id, which the fold points at the QUESTION turn
  // while a question is pending — interrupts must be resolvable from these
  // ids back to the asking turn.
  questionTurnID: string;
  questionClientNonce: string;
  resolve: (result: CallToolResult) => void;
};

// ParkedInputReply holds a durable answer that arrived while no question
// pause was registered (issue #1078 item 3: runner restart → the
// redelivered submit_turn replays the turn for minutes before the SDK
// re-asks). The JetStream heartbeat keeps the control command alive —
// parking must NOT burn the control plane's max_deliver budget the way
// the old nak(1s) loop did. pauseTurnForInput drains matching entries the
// moment a pause registers; the expiry timer is the bounded failure path.
type ParkedInputReply = {
  record: SessionCommandRecord;
  targetTurnID: string;
  receivedAtMs: number;
  stopCommandHeartbeat: () => void;
  expireTimer: ReturnType<typeof setTimeout>;
};

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
const TANK_MCP_SERVER_NAME = "tank";
const TANK_ASK_USER_QUESTION_TOOL = "AskUserQuestion";
const TANK_ASK_USER_QUESTION_TOOL_ALIAS = `mcp__${TANK_MCP_SERVER_NAME}__${TANK_ASK_USER_QUESTION_TOOL}`;

const askUserQuestionInputSchema = {
  question: z
    .string()
    .optional()
    .describe("Single question text when not using the questions array."),
  questions: z
    .array(
      z
        .object({
          question: z.string().describe("Question text shown to the user."),
          options: z
            .array(
              z
                .object({
                  label: z.string().describe("Selectable answer label."),
                  description: z.string().optional(),
                  preview: z.string().optional(),
                })
                .passthrough(),
            )
            .optional(),
          allowFreeForm: z.boolean().optional(),
        })
        .passthrough(),
    )
    .optional()
    .describe("One or more questions to ask the user."),
  options: z
    .array(
      z
        .object({
          label: z.string().describe("Selectable answer label."),
          description: z.string().optional(),
          preview: z.string().optional(),
        })
        .passthrough(),
    )
    .optional()
    .describe("Options for the single-question shorthand."),
  allowFreeForm: z
    .boolean()
    .optional()
    .describe("Whether the user may answer with free-form text."),
};

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

// PROVIDER_RETRY_STALL_MS bounds how long the runner tolerates a Claude SDK
// api_retry{error:"rate_limit"} storm with zero turn progress before forcing a
// durable turn.failed{reason:"provider_rate_limit"} terminal. The SDK retries
// 429s internally and, when the retries don't converge, never surfaces a
// terminal rate_limit_event — so without this bound the turn sits "claimed"
// with the user seeing dead air (session 638, 2026-06-06: 35+ minutes, no
// terminal). Sized above a normal slow-but-progressing first response (the
// api-proxy serves even ~1M-token requests in well under two minutes), so only
// a genuinely stuck retry loop trips it. Any real provider output (turn.started
// or a mapped canonical event) resets the window; status/thinking_tokens
// heartbeats do NOT, since they are part of the stuck retry cycle.
const PROVIDER_RETRY_STALL_MS = parsePositiveEnvInt(
  process.env.SESSION_PROVIDER_RETRY_STALL_MS,
  240_000,
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

// DURABLE_PUBLISH_* bound dispatch()'s in-place retry for ordinary durable
// events (issue #1078: fire-once publishes silently holed the ledger on
// transient NATS blips). Short schedule: streaming dispatches are awaited
// sequentially, so the worst-case added latency during an outage is
// attempts × backoff on the hot path. Terminals use TERMINAL_PUBLISH_*
// via publishTerminalWithRetry instead.
const DURABLE_PUBLISH_ATTEMPTS = parsePositiveEnvInt(
  process.env.SESSION_DURABLE_PUBLISH_ATTEMPTS,
  3,
);
const DURABLE_PUBLISH_BACKOFF_MS = parsePositiveEnvInt(
  process.env.SESSION_DURABLE_PUBLISH_BACKOFF_MS,
  200,
);

// How long a NAK'd submit_turn whose terminal publish was deferred waits
// before JetStream redelivers it for a republish attempt (issue #1078
// item 1). Long enough for a NATS blip to clear; short enough that the
// UI's wedged "streaming" state resolves promptly.
const TERMINAL_REDELIVERY_NAK_MS = parsePositiveEnvInt(
  process.env.SESSION_TERMINAL_REDELIVERY_NAK_MS,
  5_000,
);

// Parked input_reply bounds (issue #1078 item 3): a durable answer that
// arrives while no question pause is registered (runner restart replay)
// waits under heartbeat for the re-asked pause instead of burning the
// control plane's max_deliver budget in seconds. The expiry is generous
// because the redelivered submit_turn replays the whole turn before the
// SDK re-asks.
const PARKED_INPUT_REPLY_MS = parsePositiveEnvInt(
  process.env.SESSION_PARKED_INPUT_REPLY_MS,
  15 * 60_000,
);
const MAX_PARKED_INPUT_REPLIES = parsePositiveEnvInt(
  process.env.SESSION_MAX_PARKED_INPUT_REPLIES,
  8,
);

// Caps for the per-process background-task ownership maps (issue #1078,
// "unbounded in-process maps"): a long-lived pod that runs thousands of
// background tasks must not grow these forever. Oldest-first eviction;
// an evicted owner only means a very late lifecycle frame for an ancient
// task falls back to the unbound-frame log path.
const MAX_TRACKED_BACKGROUND_TASKS = parsePositiveEnvInt(
  process.env.SESSION_MAX_TRACKED_BACKGROUND_TASKS,
  1024,
);
const MAX_FIRED_BACKGROUND_WAKES = parsePositiveEnvInt(
  process.env.SESSION_MAX_FIRED_BACKGROUND_WAKES,
  4096,
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

function parsePositiveEnvInt(
  value: string | undefined,
  fallback: number,
): number {
  const parsed = Number.parseInt((value ?? "").trim(), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

// boundedMapSet / boundedSetAdd keep the per-process tracking collections
// finite on long-lived pods (issue #1078, "unbounded in-process maps").
// JS Maps/Sets iterate in insertion order, so deleting the first key is
// oldest-first eviction.
export function boundedMapSet<K, V>(
  map: Map<K, V>,
  key: K,
  value: V,
  max: number,
): void {
  map.delete(key);
  map.set(key, value);
  while (map.size > max) {
    const oldest = map.keys().next();
    if (oldest.done) break;
    map.delete(oldest.value);
  }
}

export function boundedSetAdd<T>(set: Set<T>, value: T, max: number): void {
  set.add(value);
  while (set.size > max) {
    const oldest = set.values().next();
    if (oldest.done) break;
    set.delete(oldest.value);
  }
}

function loadConfiguredMcpServers(path: string): Record<string, McpServerConfig> {
  const trimmed = path.trim();
  if (!trimmed) return {};
  try {
    const parsed = JSON.parse(readFileSync(trimmed, "utf8")) as {
      mcpServers?: Record<string, McpServerConfig>;
    };
    return parsed.mcpServers && typeof parsed.mcpServers === "object"
      ? parsed.mcpServers
      : {};
  } catch (err) {
    const code = (err as { code?: string } | null)?.code;
    if (code !== "ENOENT") throw err;
    return {};
  }
}

function isClaudePermissionDeniedEvent(
  event: ClaudeProviderEvent,
): event is ClaudeProviderEvent & {
  type: "system";
  subtype: "permission_denied";
  tool_name?: string;
  tool_use_id?: string;
  agent_id?: string;
  decision_reason_type?: string;
  decision_reason?: string;
  message?: string;
  uuid?: string;
} {
  return event.type === "system" && event.subtype === "permission_denied";
}

function permissionDeniedLabels(event: {
  tool_name?: string;
  agent_id?: string;
  decision_reason_type?: string;
}): {
  agentKind: "parent" | "subagent";
  toolFamily: "mcp" | "local" | "other";
  server: string;
  decision: string;
} {
  const toolName = String(event.tool_name ?? "");
  const mcpMatch = /^mcp__([^_]+(?:_[^_]+)*)__(.+)$/.exec(toolName);
  const toolFamily = mcpMatch ? "mcp" : toolName ? "local" : "other";
  const server = mcpMatch?.[1] ?? "none";
  const decision = String(event.decision_reason_type ?? "unknown").trim();
  return {
    agentKind: event.agent_id ? "subagent" : "parent",
    toolFamily,
    server: server || "none",
    decision: decision || "unknown",
  };
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
  private readonly pendingInputReplies = new Map<string, PendingInputReply>();
  // Durable answers waiting for their question pause to (re)register —
  // see ParkedInputReply. Bounded by MAX_PARKED_INPUT_REPLIES.
  private readonly parkedInputReplies: ParkedInputReply[] = [];
  // Claude background shell tasks report their lifecycle through system
  // task_* frames. The start frame is turn-scoped, but progress and final
  // notification can arrive after the foreground turn has already completed.
  // Keep the owning turn by task_id and by originating tool_use_id so those
  // late frames still land on the durable session transcript.
  private readonly backgroundTaskTurns = new Map<string, ClaudeTurnContext>();
  private readonly backgroundToolUseTurns = new Map<
    string,
    ClaudeTurnContext
  >();
  // Task ids we've already registered a background-task wake for this process,
  // so a repeated terminal frame for the same task does not re-POST. The
  // backend Register is idempotent (ON CONFLICT wake_id), so this Set is a
  // re-POST optimization, not the correctness boundary (it is intentionally
  // lost on runner restart, like the other in-process task maps above).
  private readonly firedBackgroundTaskWakes = new Set<string>();
  private activeTurn: PendingTurn | null = null;
  private sdkQuery: Query | null = null;
  private tankAskUserQuestionSequence = 0;
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
  private sdkStderrBuffer = "";
  // reportedContextWindowTokens latches the per-turn ModelUsage context
  // window so the runner POSTs it to the orchestrator exactly once per
  // process (the backend is first-observed-wins). Mirrors the codex-runner
  // app-server transport's first-observed latch. Stays null until the first
  // `result` message carries a usable window.
  private reportedContextWindowTokens: number | null = null;
  // providerRetryStall tracks an in-flight Claude SDK api_retry{error:"rate_limit"}
  // storm against the turn it is stalling. It is armed on the first such frame
  // for a turn and cleared by resetProviderRetryStall() the moment the turn
  // makes real progress (turn.started or a mapped canonical event). When the
  // window exceeds providerRetryStallMs with no progress the runner forces a
  // durable terminal so the command queue drains. See PROVIDER_RETRY_STALL_MS.
  private providerRetryStall: {
    turnKey: string;
    firstAtMs: number;
    count: number;
  } | null = null;
  private providerRetryStallMs = PROVIDER_RETRY_STALL_MS;
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
    // Two independent JetStream consumers: data plane (submit_turn —
    // serial, ack-after-terminal) and control plane (interrupt_turn,
    // stop_background_task — low-latency, never blocked by an in-flight turn).
    // See runner-shared/sessionBus.js for the consumer config split and
    // docs/tank-conversation-protocol.md → "Durable turn interruption"
    // for the contract. Don't fold these back into one consumer; that's
    // exactly the regression the split fixes.
    const stopConsumer = this.startCommandConsumer(signal);
    const stopControl = this.startControlConsumer(signal);
    // Close background tasks orphaned by a runner restart, honestly. The SDK
    // task registry is process state: after a restart no lifecycle frame for
    // a pre-restart run_in_background task will ever arrive, so its durable
    // lifecycle would stay open forever and its promised report would never
    // happen (the counted silent-stranding class). Each orphan gets a
    // corrective shell_task.exited{status:unknown, completion_source:
    // runner_restart} on its originating turn plus a wake that tells the
    // agent observability was lost and demands it verify and report.
    void this.adoptOrphanedBackgroundTasks().catch((err) => {
      console.error("background task re-adoption failed:", err);
    });
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
    if (!signal.aborted) {
      // Issue #1078 item 5: the SDK query loop is dead but the open NATS
      // socket would keep Node alive — a permanently inert runner whose
      // consumers are stopped and whose sdkQuery is never rebuilt; every
      // later turn would sit unconsumed until the stranded-turn sweep
      // false-failed it hours later. Drain in-flight turns to durable
      // failures and exit: the kubelet restarts the runner container
      // (session pods default restartPolicy=Always; pod death — the
      // terminal-by-design case — is not this) and `continue: true`
      // resumes the on-disk JSONL transcript.
      await this.drainTurnsForFatalExit();
      console.error(
        "claude-runner: SDK query loop ended while not shutting down; exiting so the container restarts",
      );
      this.exitProcess(1);
    }
  }

  // exitProcess is a seam for tests; production always process.exit(1)s.
  exitProcess: (code: number) => void = (code) => process.exit(code);

  private async drainTurnsForFatalExit(): Promise<void> {
    const turns = [this.activeTurn, ...this.pendingTurns];
    for (const turn of turns) {
      if (!turn || turn.terminalEmitted || !turn.commandRecord) continue;
      await this.publishTurnTerminalOrDefer(
        turn,
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "claude",
          type: "turn.failed",
          reason: "sdk_loop_dead",
          error:
            "the SDK query loop died; the runner restarted and this turn was not re-run",
        }),
        "turn.failed",
      ).catch((err) =>
        console.error("fatal-exit turn drain failed:", err),
      );
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
        msg: "claude-runner pinning SDK options from first turn",
        model,
        effort,
        source_command_id: record.id,
      }),
    );

    const mcpServers = {
      ...loadConfiguredMcpServers(this.cfg.mcpConfig),
      [TANK_MCP_SERVER_NAME]: this.createTankMcpServer(),
    };
    const options: Options = {
      cwd: this.cfg.workspace,
      // The api-proxy injects OAuth from KV when the placeholder bearer
      // is seen — both the SDK and the raw CLI go through this path.
      permissionMode: "bypassPermissions",
      allowDangerouslySkipPermissions: true,
      toolAliases: {
        [TANK_ASK_USER_QUESTION_TOOL]: TANK_ASK_USER_QUESTION_TOOL_ALIAS,
      },
      // Resume an on-disk JSONL if one exists from a prior process
      // life (e.g., claude-runner restart within the same pod).
      // First boot with no JSONL: no-op.
      continue: true,
      // include_partial_messages keeps the typewriter effect — the SPA
      // renders stream_event deltas live and snapshots to the canonical
      // assistant message when it arrives.
      includePartialMessages: true,
      mcpServers,
      // Bare mode would skip CLAUDE.md / skills / hooks; we want those.
      model,
      effort,
      stderr: (data: string) => this.handleSdkStderr(data),
    };

    this.sdkQuery = this.launchSdkQuery(options);
    void reportRuntimeConfig(this.cfg, { model, effort }).catch((err) => {
      console.warn("runtime config report failed:", err);
    });
    // The model's context window is reported later, from the first SDK
    // `result` message's per-model ModelUsage (see maybeReportContextWindow).
    // The Anthropic Models API path was removed: `GET /v1/models/{model}`
    // returns HTTP 401 under the subscription-OAuth proxy, so Claude sessions
    // never got a window from it.
    this.resolveSdkReady();
  }

  private handleSdkStderr(data: string): void {
    this.sdkStderrBuffer += String(data ?? "");
    const lines = this.sdkStderrBuffer.split(/\r?\n/);
    this.sdkStderrBuffer = lines.pop() ?? "";
    for (const line of lines) {
      this.logSdkStderrLine(line);
    }
    if (this.sdkStderrBuffer.length > 4096) {
      this.logSdkStderrLine(this.sdkStderrBuffer);
      this.sdkStderrBuffer = "";
    }
  }

  private logSdkStderrLine(line: string): void {
    const text = redactSdkStderrLine(line.trim()).slice(0, 2000);
    if (!text) return;
    console.warn(JSON.stringify({ msg: "claude_sdk_stderr", text }));
  }

  // maybeReportContextWindow reads the model's context window from a Claude
  // Agent SDK `result` message's per-model usage map
  // (SDKResultMessage.modelUsage → ModelUsage.contextWindow) and POSTs it to
  // the orchestrator so the composer can render a used/window fraction (parity
  // with the codex-runner, which reports the app-server's modelContextWindow).
  // Observed, no HTTP/auth: the prior Anthropic Models API path returned 401
  // under the subscription-OAuth proxy and is removed.
  //
  // Latched via reportedContextWindowTokens so it POSTs once per process (the
  // backend is first-observed-wins) instead of on every result. Fire-and-forget
  // and best-effort — never throws out of the turn loop. We only ever report a
  // positive integer.
  private maybeReportContextWindow(message: SDKMessage): void {
    if (this.reportedContextWindowTokens !== null) return;
    const result = message as {
      modelUsage?: Record<string, { contextWindow?: number }>;
    };
    const window = pickContextWindowFromModelUsage(result.modelUsage);
    if (window === null) return;
    this.reportedContextWindowTokens = window;
    void reportRuntimeConfig(this.cfg, {
      contextWindowTokens: window,
      contextWindowSource: "claude_sdk_model_usage",
    }).catch(console.warn);
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

  private createTankMcpServer(): McpServerConfig {
    return createSdkMcpServer({
      name: TANK_MCP_SERVER_NAME,
      version: "1.0.0",
      instructions:
        "Use AskUserQuestion when you need a blocking answer from the Tank user before proceeding.",
      alwaysLoad: true,
      tools: [
        tool(
          TANK_ASK_USER_QUESTION_TOOL,
          "Ask the Tank user one or more blocking questions and wait for the answer.",
          askUserQuestionInputSchema,
          async (input) => this.handleTankAskUserQuestion(input),
          { alwaysLoad: true },
        ),
      ],
    });
  }

  private handleTankAskUserQuestion(input: unknown): Promise<CallToolResult> {
    const turn = this.activeTurn;
    if (!turn) {
      return Promise.resolve({
        isError: true,
        content: [
          {
            type: "text",
            text: "AskUserQuestion cannot pause the turn because no active Tank turn exists.",
          },
        ],
      });
    }
    const providerItemID = this.nextTankAskUserQuestionProviderItemID(turn);
    const questions = claudeQuestionsToTankShape(input);
    return this.pauseTurnForInput(turn, questions, providerItemID);
  }

  private nextTankAskUserQuestionProviderItemID(turn: PendingTurn): string {
    this.tankAskUserQuestionSequence += 1;
    return `tank_ask_user_question_${turn.turnID}_${this.tankAskUserQuestionSequence}`;
  }

  private async handleEvent(message: SDKMessage): Promise<void> {
    const providerEvent = message as ClaudeProviderEvent;
    // The per-turn `result` message carries ModelUsage with the provider's
    // context window. Read it before any early-return branching below so the
    // window still latches even when the active turn was already interrupted.
    if (providerEvent.type === "result") {
      this.maybeReportContextWindow(message);
    }
    const activeTurn = await this.ensureActiveTurn(providerEvent);
    if (isClaudePermissionDeniedEvent(providerEvent)) {
      await this.handlePermissionDenied(providerEvent, activeTurn);
      return;
    }
    if (
      activeTurn?.terminalEmitted &&
      !isClaudeTaskLifecycleMessage(providerEvent)
    ) {
      if (providerEvent.type === "result" && this.activeTurn === activeTurn) {
        this.activeTurn = null;
      }
      return;
    }
    if (isClaudeRateLimitEvent(providerEvent)) {
      if (!claudeRateLimitEventIsTerminal(providerEvent)) {
        providerRateLimitEventTotal.inc();
        providerRateLimitDecisionTotal.labels(activeTurn ? "observed_allowed_active" : "observed_allowed_idle").inc();
        this.reportProviderRateLimitInfo(providerEvent);
        return;
      }
      if (activeTurn) {
        await this.failTurnForProviderRateLimit(activeTurn, providerEvent);
        return;
      }
      providerRateLimitEventTotal.inc();
      providerRateLimitDecisionTotal.labels("terminal_without_active_turn").inc();
      this.reportProviderRateLimitInfo(providerEvent);
      logUnhandledSdkMessage(message);
      return;
    }
    if (
      providerEvent.type === "system" &&
      providerEvent.subtype === "api_retry"
    ) {
      // The Claude SDK's internal HTTP-retry signal. A sustained
      // error=rate_limit storm with no turn progress used to fall through to
      // logUnhandledSdkMessage and strand the turn (session 638); classify it
      // and force a durable terminal once the no-progress window elapses.
      await this.handleProviderApiRetry(providerEvent);
      return;
    }
    const adapterTurn = this.turnContextForProviderEvent(
      providerEvent,
      activeTurn,
    );
    if (isClaudeTaskLifecycleMessage(providerEvent) && !adapterTurn) {
      const { taskID, toolUseID } = claudeTaskIdentifiers(providerEvent);
      console.log(
        JSON.stringify({
          msg: "sdk_task_lifecycle_unbound",
          type: providerEvent.type,
          subtype: providerEvent.subtype,
          task_id: taskID,
          tool_use_id: toolUseID,
        }),
      );
    }

    const canonicalEvents = canonicalEventsForClaudeMessage(
      this.cfg,
      adapterTurn,
      providerEvent,
    );
    if (canonicalEvents.length === 0) {
      logUnhandledSdkMessage(message);
    }

    let observedShellTaskExitEventID = "";
    for (const event of canonicalEvents) {
      this.rememberClaudeTaskOwner(event, adapterTurn ?? activeTurn);
      const terminalType =
        event.type === "turn.completed" ||
        event.type === "turn.failed" ||
        event.type === "turn.interrupted"
          ? event.type
          : null;
      if (terminalType && activeTurn) {
        // Natural terminals ride the bounded-retry + park-for-redelivery
        // path (issue #1078 item 1). The pre-fix shape used plain dispatch:
        // one failed publish meant markCommandTerminal never ran, the
        // working() heartbeat extended the un-acked submit_turn forever,
        // and max_ack_pending=1 silently blocked every future turn until
        // pod restart.
        await this.publishTurnTerminalOrDefer(activeTurn, event, terminalType);
        continue;
      }
      const dispatched = terminalType
        ? await this.publishTerminalWithRetry(event)
        : await dispatch(this.sink, event);
      if (dispatched && event.type === "shell_task.exited") {
        // The durable observation identity of this terminal — the wake
        // registration's re-arm discriminator.
        observedShellTaskExitEventID = String(
          (event as { event_id?: unknown }).event_id ?? "",
        );
      }
    }
    if (canonicalEvents.length > 0) {
      // Real provider output means the turn is progressing, so clear any armed
      // api_retry stall window. status/thinking_tokens heartbeats are NOT
      // progress and intentionally do not reset it.
      this.resetProviderRetryStall();
    }
    if (providerEvent.type === "result" && this.activeTurn === activeTurn) {
      this.activeTurn = null;
    }

    const wakeup = extractWakeup(message);
    if (wakeup) {
      await this.registerWakeup(wakeup, activeTurn?.turnID ?? "");
    }

    await this.maybeRegisterBackgroundTaskWake(
      providerEvent,
      observedShellTaskExitEventID,
    );
  }

  private async handlePermissionDenied(
    event: ClaudeProviderEvent & {
      tool_name?: string;
      tool_use_id?: string;
      agent_id?: string;
      decision_reason_type?: string;
      decision_reason?: string;
      message?: string;
      uuid?: string;
    },
    turn: PendingTurn | null,
  ): Promise<void> {
    const labels = permissionDeniedLabels(event);
    toolPermissionDeniedTotal
      .labels(
        labels.agentKind,
        labels.toolFamily,
        labels.server,
        labels.decision,
      )
      .inc();
    console.warn(
      JSON.stringify({
        msg: "claude_tool_permission_denied",
        agent_kind: labels.agentKind,
        tool_family: labels.toolFamily,
        server: labels.server,
        decision: labels.decision,
        tool_name: event.tool_name ?? "",
        tool_use_id: event.tool_use_id ?? "",
        agent_id: event.agent_id ?? "",
      }),
    );
    if (!turn || turn.terminalEmitted) return;
    await dispatch(
      this.sink,
      itemEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        source: "claude",
        type: "item.failed",
        providerItemID:
          typeof event.tool_use_id === "string" && event.tool_use_id
            ? event.tool_use_id
            : `permission_denied_${Date.now()}`,
        actor: "tool",
        providerEventID: event.uuid,
        payload: {
          kind: "tool",
          title: event.tool_name ?? "Tool",
          name: event.tool_name ?? "tool",
          error: event.message ?? "Permission denied",
          outcome: { kind: "execution_failed", reason: "provider_item_error" },
          permission_denied: {
            agent_kind: labels.agentKind,
            decision: labels.decision,
            decision_reason: event.decision_reason,
          },
        },
      }),
    );
  }

  private reportProviderRateLimitInfo(message: ClaudeProviderEvent): Record<string, unknown> | null {
    const rateLimitInfo = claudeRateLimitInfo(message);
    if (rateLimitInfo) {
      void reportRuntimeConfig(this.cfg, {
        providerRateLimitInfo: rateLimitInfo,
      }).catch((err) => {
        console.warn("provider rate-limit info report failed:", err);
      });
    }
    return rateLimitInfo;
  }

  private async failTurnForProviderRateLimit(
    turn: PendingTurn,
    message: ClaudeProviderEvent,
  ): Promise<void> {
    providerRateLimitEventTotal.inc();
    providerRateLimitDecisionTotal.labels("failed_turn").inc();
    this.reportProviderRateLimitInfo(message);
    if (turn.terminalEmitted) return;
    const error = claudeRateLimitError(message);
    providerFailureClassTotal.labels("rate_limit").inc();
    const published = await this.publishTurnTerminalOrDefer(
      turn,
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
      "turn.failed",
    );
    this.signalStopToSdk();
    if (!published) return;
    if (this.activeTurn === turn) {
      this.activeTurn = null;
    }
  }

  // handleProviderApiRetry classifies a Claude SDK system/api_retry frame (the
  // SDK's internal HTTP-retry signal) and, for a sustained error=rate_limit
  // storm with no turn progress, forces the in-flight turn to a durable
  // terminal so the command queue drains. Without this the SDK retries 429s
  // indefinitely and never surfaces a terminal rate_limit_event, leaving the
  // turn "claimed" with the user seeing dead air (session 638). Non-rate_limit
  // retries (overloaded / api_error) are transient and the SDK recovers on its
  // own, so they are observed but never forced to a terminal.
  private async handleProviderApiRetry(
    message: ClaudeProviderEvent,
  ): Promise<void> {
    const error = classifyApiRetryError(message.error);
    providerApiRetryTotal.labels(error).inc();
    const turn = this.activeTurn ?? this.pendingTurns[0] ?? null;
    if (error !== "rate_limit" || !turn || turn.terminalEmitted) {
      return;
    }
    const now = Date.now();
    if (
      !this.providerRetryStall ||
      this.providerRetryStall.turnKey !== turn.turnID
    ) {
      this.providerRetryStall = {
        turnKey: turn.turnID,
        firstAtMs: now,
        count: 1,
      };
      return;
    }
    this.providerRetryStall.count += 1;
    if (now - this.providerRetryStall.firstAtMs < this.providerRetryStallMs) {
      return;
    }
    const stalledMs = now - this.providerRetryStall.firstAtMs;
    const count = this.providerRetryStall.count;
    this.resetProviderRetryStall();
    await this.failTurnForProviderRetryStall(turn, message, stalledMs, count);
  }

  // failTurnForProviderRetryStall resolves a turn wedged in a provider
  // rate-limit retry loop to the same durable terminal a terminal
  // rate_limit_event would (turn.failed{reason:"provider_rate_limit"}), aborts
  // the stuck SDK request, drains the command, and removes the turn from the
  // pending queue so a late SDK frame cannot re-promote an already-terminal
  // turn. Mirrors failTurnForProviderRateLimit; the distinct decision label
  // (retry_stall_failed) keeps the "SDK never surfaced a terminal frame" case
  // separable from an ordinary rejected primary quota.
  private async failTurnForProviderRetryStall(
    turn: PendingTurn,
    message: ClaudeProviderEvent,
    stalledMs: number,
    retryCount: number,
  ): Promise<void> {
    providerRateLimitDecisionTotal.labels("retry_stall_failed").inc();
    if (turn.terminalEmitted) return;
    providerFailureClassTotal.labels("rate_limit").inc();
    const error =
      `provider rate-limit retry stall: ${retryCount} api_retry{error:rate_limit} frames over ` +
      `${Math.round(stalledMs / 1000)}s with no turn progress ` +
      `(no turn.started, no provider output)`;
    const published = await this.publishTurnTerminalOrDefer(
      turn,
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
      "turn.failed",
    );
    this.signalStopToSdk();
    if (!published) return;
    this.removePendingTurn(turn);
    if (this.activeTurn === turn) {
      this.activeTurn = null;
    }
  }

  private removePendingTurn(turn: PendingTurn): void {
    const idx = this.pendingTurns.indexOf(turn);
    if (idx >= 0) this.pendingTurns.splice(idx, 1);
  }

  private resetProviderRetryStall(): void {
    this.providerRetryStall = null;
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
    if (
      event.type === "item.started" &&
      event.actor === "tool" &&
      event.provider_item_id
    ) {
      boundedMapSet(
        this.backgroundToolUseTurns,
        String(event.provider_item_id),
        this.snapshotTurnContext(turn),
        MAX_TRACKED_BACKGROUND_TASKS,
      );
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
    if (taskID) {
      boundedMapSet(
        this.backgroundTaskTurns,
        taskID,
        owner,
        MAX_TRACKED_BACKGROUND_TASKS,
      );
    }
    const toolUseID =
      typeof event.payload?.tool_use_id === "string"
        ? event.payload.tool_use_id
        : typeof event.provider_item_id === "string" &&
            event.provider_item_id !== taskID
          ? event.provider_item_id
          : "";
    if (toolUseID) {
      boundedMapSet(
        this.backgroundToolUseTurns,
        toolUseID,
        owner,
        MAX_TRACKED_BACKGROUND_TASKS,
      );
    }
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
        // Interrupts and stop_background_task MUST arrive via startControlConsumer
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
      .catch((err) =>
        console.error("session bus command consumer crashed:", err),
      );
    return () => {
      void stopConsumer?.();
    };
  }

  // startControlConsumer drives the control-plane JetStream consumer.
  // Today: interrupt_turn + stop_background_task. Future low-latency
  // control signals (resume, cancel-with-reason, etc.) should land here as
  // additional branches, not on the data-plane consumer. These are
  // control-plane because they must preempt an already-running submit_turn
  // that is, by construction, holding the data plane's single
  // max_ack_pending slot — see backend-go/internal/sessionbus/subjects.go
  // → SubjectForCommand for the publish-side reasoning that pairs with
  // this consumer branch.
  private startControlConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startControlConsumer(async (record) => {
        if (isInputReplyCommand(record)) {
          await this.acceptInputReply(record);
          return;
        }
        if (isInterruptCommand(record)) {
          await this.acceptInterrupt(record);
          return;
        }
        if (isStopBackgroundTaskCommand(record)) {
          commandsConsumedTotal
            .labels("stop_background_task", "unsupported")
            .inc();
          await this.commandBus.markFailed(
            record,
            new Error(
              "background task stop is not supported by the Claude runner",
            ),
          );
          return;
        }
        // Unknown control command type. Ack to clear the slot; log so
        // the producer-side surprise is visible. No retry — a
        // backend-only command type a runner doesn't recognise will
        // never start working on retry.
        commandsConsumedTotal.labels("control_unknown", "dropped").inc();
        console.warn("session bus control consumer: unknown command type", {
          type: record.type,
          command_id: record.id,
        });
        await this.commandBus.markCompleted(record);
      }, signal)
      .then((stop) => {
        stopConsumer = stop;
      })
      .catch((err) =>
        console.error("session bus control consumer crashed:", err),
      );
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
      await this.commandBus.markFailed(
        record,
        new Error("submit command missing prompt"),
      );
      return;
    }
    if (await this.finalizeCommandIfAlreadyTerminal(record, clientNonce)) {
      commandsConsumedTotal.labels("submit_turn", "already_terminal").inc();
      // A terminal landed through another path (interrupt fallback) while a
      // natural terminal sat parked — drop the stale parked state.
      this.discardParkedTerminal(clientNonce);
      return;
    }
    // Redelivery of a turn this process already owns (issue #1078 items 1+6):
    // retry the parked terminal publish, or reattach a lapsed delivery to the
    // still-running turn. Never re-feed the prompt to the SDK.
    const existing = this.findTurnForKey(clientNonce);
    if (existing) {
      commandsConsumedTotal.labels("submit_turn", "redelivered").inc();
      await this.reattachRedeliveredCommand(existing, record);
      return;
    }
    if (this.commandBus.attemptsExceeded(record)) {
      commandsConsumedTotal.labels("submit_turn", "attempts_exceeded").inc();
      await this.failCommandRecord(
        record,
        new Error(
          `session command exceeded ${record.attempt_count ?? "unknown"} claim attempts`,
        ),
      );
      return;
    }
    const pendingTurn = this.acceptTurn(prompt, clientNonce, record);
    if (!pendingTurn) {
      commandsConsumedTotal.labels("submit_turn", "invalid").inc();
      await this.commandBus.markFailed(
        record,
        new Error("session command was not accepted"),
      );
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
    pendingTurn.stopCommandHeartbeat =
      this.commandBus.startCommandHeartbeat(record);
    this.pendingTurns.push(pendingTurn);
    await this.publishTurnClaimed(pendingTurn);
    if (
      pendingTurn.interruptOnStart &&
      pendingTurn.interruptOnStart.length > 0
    ) {
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
    const targetKey = String(
      record.target_turn_id ?? record.client_nonce ?? "",
    ).trim();
    if (!targetKey) {
      // Backend bug — the /interrupt handler MUST send target_turn_id
      // (it copies the URL path value into both target_turn_id and
      // client_nonce). Drop with a visible failure rather than the
      // pre-#532 silent ack so the regression surfaces.
      interruptOutcomeTotal.labels("invalid_target").inc();
      await this.commandBus.markFailed(
        record,
        new Error(
          "interrupt_turn missing both target_turn_id and client_nonce",
        ),
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
    // Stop during AskUserQuestion (issue #1078 item 2): while a question is
    // pending the fold points activity.active_turn_id at the QUESTION turn,
    // so the SPA's Stop targets an id no PendingTurn carries. Resolve
    // question identifiers to the asking turn — the turn that is actually
    // paused. Pre-fix this buffered → 30s orphan timer → bogus
    // turn.failed{interrupt_orphaned} on the question shell while the
    // asking turn's provider pause stayed wedged until pod restart.
    for (const entry of this.pendingInputReplies.values()) {
      if (key === entry.questionTurnID || key === entry.questionClientNonce) {
        return entry.turn;
      }
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
  private bufferInterrupt(
    record: SessionCommandRecord,
    targetKey: string,
  ): void {
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

  private async expireBufferedInterrupt(
    record: SessionCommandRecord,
  ): Promise<void> {
    const idx = this.pendingInterrupts.findIndex(
      (buf) => buf.record === record,
    );
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
    // Ledger already-terminal check (issue #1078: codex had this, claude
    // didn't): a Stop racing a just-completed turn must not write a
    // contradictory second terminal onto a turn the ledger already closed.
    try {
      const terminal = await this.sink.findTurnTerminal(syntheticTurnID);
      if (terminal) {
        interruptOutcomeTotal.labels("turn_already_terminal").inc();
        await this.commandBus.markCompleted(record);
        return;
      }
    } catch (err) {
      console.warn(
        "findTurnTerminal failed for orphan check; falling through to interrupt_orphaned:",
        err,
      );
    }
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
    // Settle any pending AskUserQuestion pause on this turn FIRST (issue
    // #1078 item 2): resolve the provider callback so the SDK's canUseTool
    // promise unwinds instead of holding the turn paused forever, and close
    // the question shell durably — turnAwaitingQuestionTarget treats any
    // terminal on the question turn as not-awaiting, so the card stops
    // accepting answers at the backend boundary.
    await this.dismissPendingQuestionsForTurn(turn);
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
        .labels(
          reason === "client_interrupt_before_start"
            ? "terminated_pre_sdk"
            : "terminated_via_sdk",
        )
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

  // dismissPendingQuestionsForTurn settles every pending AskUserQuestion
  // pause on `turn` without an answer (issue #1078 item 2). The resolve
  // unblocks the SDK's canUseTool promise (the interrupt then unwinds the
  // turn normally); the question shell gets a durable turn.interrupted so
  // the sweep, the answer handler's 409 arm, and the transcript all see a
  // closed turn instead of a forever-awaiting shell.
  private async dismissPendingQuestionsForTurn(turn: PendingTurn): Promise<void> {
    for (const [key, entry] of [...this.pendingInputReplies]) {
      if (entry.turn !== turn) continue;
      this.pendingInputReplies.delete(key);
      askUserQuestionDismissedTotal.inc();
      entry.resolve({
        isError: true,
        content: [
          {
            type: "text",
            text: "The user stopped the turn; this question was dismissed without an answer.",
          },
        ],
      });
      const published = await this.publishTerminalWithRetry(
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: entry.questionTurnID,
          clientNonce: entry.questionClientNonce,
          source: "claude",
          type: "turn.interrupted",
          reason: "question_dismissed_by_stop",
        }),
      );
      if (!published) {
        console.error(
          "question shell terminal publish failed after retry:",
          JSON.stringify({ question_turn_id: entry.questionTurnID }),
        );
      }
    }
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
          console.error(
            "sdkQuery.interrupt() failed after Stop terminal was emitted:",
            err,
          );
        });
      } catch (err) {
        providerErrorTotal.labels("interrupt").inc();
        console.error(
          "sdkQuery.interrupt() failed; continuing with durable Stop terminal:",
          err,
        );
      }
    };

    const backgroundTasks = (
      sdkQuery as Query & {
        backgroundTasks?: (toolUseId?: string) => Promise<boolean>;
      }
    ).backgroundTasks;
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
      void backgroundPromise
        .then((backgrounded) => {
          clearTimeout(timer);
          providerControlTotal
            .labels("background_tasks", backgrounded ? "backgrounded" : "none")
            .inc();
          sendInterrupt(
            backgrounded ? "after_background" : "no_foreground_tasks",
          );
        })
        .catch((err) => {
          clearTimeout(timer);
          providerControlTotal.labels("background_tasks", "failed").inc();
          providerErrorTotal.labels("background_tasks").inc();
          console.error(
            "sdkQuery.backgroundTasks() failed before Stop interrupt:",
            err,
          );
          sendInterrupt("background_failed");
        });
    } catch (err) {
      clearTimeout(timer);
      providerControlTotal.labels("background_tasks", "failed").inc();
      providerErrorTotal.labels("background_tasks").inc();
      console.error(
        "sdkQuery.backgroundTasks() failed before Stop interrupt:",
        err,
      );
      sendInterrupt("background_failed");
    }
  }

  private async publishTerminalWithRetry(
    event: TankConversationEvent,
  ): Promise<boolean> {
    for (let attempt = 0; attempt < TERMINAL_PUBLISH_ATTEMPTS; attempt++) {
      if (attempt > 0) {
        // Exponential backoff. JetStream client-side publish failures
        // (max_payload, etc.) are deterministic so retries don't help
        // there; transient connection blips do recover within a few
        // hundred ms.
        const delay = TERMINAL_PUBLISH_BACKOFF_MS * 2 ** (attempt - 1);
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      // attempts=1: this loop owns the terminal retry schedule; nesting
      // dispatch()'s own in-place retry would multiply the backoffs.
      if (await dispatch(this.sink, event, 1)) return true;
    }
    return false;
  }

  // pauseTurnForInput publishes durable turn.awaiting_input for the Tank-owned
  // AskUserQuestion MCP tool and resolves only when input_reply arrives.
  // Mirrors applyInterruptToTurn's durable-first posture: publish with retry,
  // and fall back to turn.failed if the pause publish ultimately fails so
  // the turn never strands without a terminal.
  private async pauseTurnForInput(
    turn: PendingTurn,
    questions: unknown,
    providerItemID: string,
  ): Promise<CallToolResult> {
    if (turn.terminalEmitted) {
      return {
        isError: true,
        content: [
          {
            type: "text",
            text: "Turn already ended before AskUserQuestion could pause.",
          },
        ],
      };
    }
    const timelineID = itemTimelineID(turn.turnID, providerItemID);
    const handoff = askUserQuestionHandoffEvents({
      sessionID: this.cfg.sessionId,
      askingTurnID: turn.turnID,
      askingClientNonce: turn.clientNonce,
      source: "claude",
      providerItemID,
      providerTimelineID: timelineID,
      questions: questions as unknown[],
    });
    const replyKey = inputReplyKey(turn.turnID, timelineID, providerItemID);
    const waitForReply = new Promise<CallToolResult>((resolve) => {
      this.pendingInputReplies.set(replyKey, {
        turn,
        providerItemID,
        timelineID,
        questionTurnID: handoff.questionTurnID,
        questionClientNonce: handoff.questionClientNonce,
        resolve,
      });
    });
    const published =
      (await dispatch(this.sink, handoff.invocation)) &&
      (await dispatch(this.sink, handoff.questionMessage)) &&
      (await dispatch(this.sink, handoff.questionSubmitted)) &&
      (await this.publishTerminalWithRetry(handoff.awaitingInput));
    if (published) {
      // A durable answer may already be parked from before a restart —
      // deliver it now that the pause exists (issue #1078 item 3).
      void this.drainParkedInputRepliesFor(turn).catch((err) =>
        console.error("parked input_reply drain failed:", err),
      );
      return waitForReply;
    }
    this.pendingInputReplies.delete(replyKey);
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
    return {
      isError: true,
      content: [
        {
          type: "text",
          text: "Failed to persist AskUserQuestion pause.",
        },
      ],
    };
  }

  private async acceptInputReply(record: SessionCommandRecord): Promise<void> {
    commandsConsumedTotal.labels("input_reply", "accepted").inc();
    const outcome = await this.deliverInputReply(record);
    if (outcome === "no_pending") {
      // Runner restart (issue #1078 item 3): the durable answer arrived
      // while no question pause is registered — the redelivered submit_turn
      // is replaying the whole turn (minutes) before the SDK re-asks. The
      // old nak(1s) loop burned the control plane's max_deliver budget in
      // ~10s and the answer was lost forever. Park under heartbeat instead;
      // pauseTurnForInput drains the park the moment a pause registers.
      this.parkInputReply(record);
    }
  }

  // deliverInputReply matches a durable answer to a registered question
  // pause and resolves it. Exact (turn, timeline, item) key first; then the
  // restart fallback — after a restart the SDK re-asks with a NEW provider
  // item id, so the recreated pause is keyed by ids the durable answer has
  // never seen, while the ASKING turn id is stable across redelivery.
  private async deliverInputReply(
    record: SessionCommandRecord,
  ): Promise<"delivered" | "failed" | "no_pending"> {
    const targetTurnID = String(record.target_turn_id ?? "").trim();
    const targetTimelineID = String(record.target_timeline_id ?? "").trim();
    const targetProviderItemID = String(
      record.target_provider_item_id ?? "",
    ).trim();
    const exactKey = inputReplyKey(
      targetTurnID,
      targetTimelineID,
      targetProviderItemID,
    );
    let pendingKey = exactKey;
    let pending = this.pendingInputReplies.get(exactKey);
    if (!pending) {
      for (const [candidateKey, candidate] of this.pendingInputReplies) {
        if (
          this.turnMatchesTarget(candidate.turn, targetTurnID) ||
          candidate.questionTurnID === targetTurnID
        ) {
          pending = candidate;
          pendingKey = candidateKey;
          inputReplyRecoveryTotal.labels("fallback_matched").inc();
          break;
        }
      }
    }
    if (!pending) return "no_pending";
    if (pending.turn.terminalEmitted) {
      commandsConsumedTotal.labels("input_reply", "not_found").inc();
      await this.commandBus.markFailed(
        record,
        new Error("input_reply target is not awaiting input"),
      );
      return "failed";
    }
    this.pendingInputReplies.delete(pendingKey);
    await this.rotateTurnForInputReply(pending.turn, record);
    pending.resolve(
      askUserQuestionToolResult(
        answersForClaudeInput(record.answers, record.annotations),
      ),
    );
    await this.commandBus.markCompleted(record);
    if (
      pending.providerItemID &&
      targetProviderItemID &&
      pending.providerItemID !== targetProviderItemID
    ) {
      // Fallback match across a re-ask: the user answered the ORIGINAL
      // card; the re-asked question shell would otherwise sit awaiting
      // forever. Close it as superseded — best-effort.
      const closed = await this.publishTerminalWithRetry(
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: pending.questionTurnID,
          clientNonce: pending.questionClientNonce,
          source: "claude",
          type: "turn.interrupted",
          reason: "superseded_by_answer",
        }),
      );
      if (!closed) {
        console.error(
          "superseded question shell close failed:",
          JSON.stringify({ question_turn_id: pending.questionTurnID }),
        );
      }
    }
    return "delivered";
  }

  private parkInputReply(record: SessionCommandRecord): void {
    const targetTurnID = String(record.target_turn_id ?? "").trim();
    inputReplyRecoveryTotal.labels("parked").inc();
    commandsConsumedTotal.labels("input_reply", "parked").inc();
    while (this.parkedInputReplies.length >= MAX_PARKED_INPUT_REPLIES) {
      const evicted = this.parkedInputReplies.shift();
      if (!evicted) break;
      clearTimeout(evicted.expireTimer);
      evicted.stopCommandHeartbeat();
      inputReplyRecoveryTotal.labels("evicted").inc();
      void this.commandBus
        .markFailed(
          evicted.record,
          new Error("parked input_reply evicted by newer replies"),
        )
        .catch((err) =>
          console.error("parked input_reply eviction mark failed:", err),
        );
    }
    const stopHeartbeat = this.commandBus.startCommandHeartbeat(record);
    const expireTimer = setTimeout(() => {
      void this.expireParkedInputReply(record).catch((err) =>
        console.error("expireParkedInputReply failed:", err),
      );
    }, PARKED_INPUT_REPLY_MS);
    if (typeof (expireTimer as { unref?: () => void }).unref === "function") {
      (expireTimer as { unref: () => void }).unref();
    }
    this.parkedInputReplies.push({
      record,
      targetTurnID,
      receivedAtMs: Date.now(),
      stopCommandHeartbeat: stopHeartbeat,
      expireTimer,
    });
  }

  private async expireParkedInputReply(
    record: SessionCommandRecord,
  ): Promise<void> {
    const idx = this.parkedInputReplies.findIndex(
      (parked) => parked.record === record,
    );
    if (idx < 0) return; // already drained into a registered pause
    const parked = this.parkedInputReplies[idx]!;
    this.parkedInputReplies.splice(idx, 1);
    parked.stopCommandHeartbeat();
    inputReplyRecoveryTotal.labels("expired").inc();
    await this.commandBus.markFailed(
      record,
      new Error(
        "input_reply never matched a question pause within the parking window",
      ),
    );
  }

  // drainParkedInputRepliesFor delivers parked answers whose target matches
  // the turn that just (re)registered a question pause. Called by
  // pauseTurnForInput after the pause is durably published.
  private async drainParkedInputRepliesFor(turn: PendingTurn): Promise<void> {
    const matching: ParkedInputReply[] = [];
    const remaining: ParkedInputReply[] = [];
    for (const parked of this.parkedInputReplies) {
      if (this.turnMatchesTarget(turn, parked.targetTurnID)) {
        matching.push(parked);
      } else {
        remaining.push(parked);
      }
    }
    if (matching.length === 0) return;
    this.parkedInputReplies.length = 0;
    this.parkedInputReplies.push(...remaining);
    for (const parked of matching) {
      clearTimeout(parked.expireTimer);
      parked.stopCommandHeartbeat();
      inputReplyRecoveryTotal.labels("unparked").inc();
      const outcome = await this.deliverInputReply(parked.record);
      if (outcome === "no_pending") {
        // The pause vanished between drain selection and delivery (terminal
        // race) — re-park rather than dropping the durable answer.
        this.parkInputReply(parked.record);
      }
    }
  }

  private async rotateTurnForInputReply(
    turn: PendingTurn,
    record: SessionCommandRecord,
  ): Promise<void> {
    const continuationClientNonce = normalizeClientNonce(record.client_nonce);
    if (!continuationClientNonce) {
      throw new Error("input_reply missing continuation client_nonce");
    }
    const previousTurnID = turn.turnID;
    turn.priorIdentities = [
      ...(turn.priorIdentities ?? []),
      turn.turnID,
      turn.clientNonce,
    ];
    turn.clientNonce = continuationClientNonce;
    turn.turnID = turnIDForClientNonce(continuationClientNonce);
    turn.started = true;
    console.info("claude AskUserQuestion continuation turn", {
      previous_turn_id: previousTurnID,
      continuation_turn_id: turn.turnID,
    });
    await this.publishTurnClaimed(turn);
    recordTurnStart(turn.turnID);
    recordTurnPreStartLatency("claimed_to_started", turn.claimedAtMs);
    await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.started",
      }),
    );
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
      commandCreatedAtMs: parseOptionalTimestampMs(commandRecord?.created_at),
      started: false,
      interrupted: false,
      terminalEmitted: false,
      ...(commandRecord ? { commandRecord } : {}),
    };
  }

  private async ensureActiveTurn(
    event: ClaudeProviderEvent,
  ): Promise<PendingTurn | null> {
    if (
      !this.activeTurn &&
      this.pendingTurns.length > 0 &&
      startsClaudeTurn(event)
    ) {
      this.activeTurn = this.pendingTurns.shift() ?? null;
      if (this.activeTurn && !this.activeTurn.started) {
        this.activeTurn.started = true;
        recordTurnStart(this.activeTurn.turnID);
        recordTurnPreStartLatency(
          "claimed_to_started",
          this.activeTurn.claimedAtMs,
        );
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
        this.resetProviderRetryStall();
      }
    }
    return this.activeTurn;
  }

  private async publishTurnClaimed(turn: PendingTurn): Promise<void> {
    if (turn.terminalEmitted) return;
    const claimedAtMs = Date.now();
    const dispatched = await dispatch(
      this.sink,
      turnEvent({
        sessionID: this.cfg.sessionId,
        turnID: turn.turnID,
        clientNonce: turn.clientNonce,
        source: "claude",
        type: "turn.claimed",
      }),
    );
    if (!dispatched) return;
    turn.claimedAtMs = claimedAtMs;
    recordTurnPreStartLatency(
      "command_created_to_claimed",
      turn.commandCreatedAtMs,
      claimedAtMs,
    );
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

  private turnMatchesTarget(
    turn: Pick<PendingTurn, "turnID" | "clientNonce" | "priorIdentities">,
    targetTurnID = "",
  ): boolean {
    return (
      !targetTurnID ||
      targetTurnID === turn.turnID ||
      targetTurnID === turn.clientNonce ||
      (turn.priorIdentities?.includes(targetTurnID) ?? false)
    );
  }

  // publishTurnTerminalOrDefer is the single path every turn terminal that
  // settles a session command takes (issue #1078 item 1). Success: mark the
  // turn terminal and ack the command. Exhausted retries: park the event on
  // the turn, stop the heartbeat, and NAK the command so JetStream
  // redelivery retries the PUBLISH (acceptCommandTurn → reattach), never
  // the prompt.
  private async publishTurnTerminalOrDefer(
    turn: PendingTurn,
    event: TankConversationEvent,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ): Promise<boolean> {
    if (turn.terminalEmitted) return true;
    const published = await this.publishTerminalWithRetry(event);
    if (published) {
      turn.terminalEmitted = true;
      turn.pendingTerminal = undefined;
      turn.pendingTerminalType = undefined;
      if (turn.commandRecord) {
        await this.markCommandTerminal(turn, type);
      }
      return true;
    }
    this.deferTerminalForRedelivery(turn, event, type);
    return false;
  }

  private deferTerminalForRedelivery(
    turn: PendingTurn,
    event: TankConversationEvent,
    type: "turn.completed" | "turn.failed" | "turn.interrupted",
  ): void {
    terminalPublishDeferredTotal.inc();
    console.error(
      "turn terminal publish exhausted retries; parking for redelivery",
      JSON.stringify({ turn_id: turn.turnID, terminal_type: type }),
    );
    turn.pendingTerminal = event;
    turn.pendingTerminalType = type;
    turn.stopCommandHeartbeat?.();
    turn.stopCommandHeartbeat = undefined;
    const record = turn.commandRecord;
    turn.commandRecord = undefined;
    if (record) {
      try {
        record.nak(TERMINAL_REDELIVERY_NAK_MS);
      } catch (err) {
        console.error("terminal-deferral NAK failed:", err);
      }
    }
  }

  // reattachRedeliveredCommand handles a submit_turn redelivery that matches
  // a turn this process already knows (issue #1078 items 1 + 6). Two cases:
  // a parked terminal (the publish failed earlier — retry it now), or a
  // genuinely in-flight turn whose ack_wait lapsed (blocked event loop, NATS
  // blip) — reattach the fresh delivery so the eventual terminal acks it,
  // instead of double-executing the prompt.
  private async reattachRedeliveredCommand(
    turn: PendingTurn,
    record: SessionCommandRecord,
  ): Promise<void> {
    if (turn.pendingTerminal && turn.pendingTerminalType) {
      turn.commandRecord = record;
      const event = turn.pendingTerminal;
      const type = turn.pendingTerminalType;
      const published = await this.publishTerminalWithRetry(event);
      if (published) {
        turn.terminalEmitted = true;
        turn.pendingTerminal = undefined;
        turn.pendingTerminalType = undefined;
        await this.markCommandTerminal(turn, type);
        this.removePendingTurn(turn);
        if (this.activeTurn === turn) this.activeTurn = null;
        return;
      }
      this.deferTerminalForRedelivery(turn, event, type);
      return;
    }
    // Mid-flight redelivery: supersede the lapsed delivery with the fresh
    // one. Acking the new delivery acks the message (same stream sequence).
    turn.stopCommandHeartbeat?.();
    turn.commandRecord = record;
    turn.stopCommandHeartbeat = this.commandBus.startCommandHeartbeat(record);
  }

  // discardParkedTerminal cleans in-memory parked state when a redelivered
  // command's ledger check found a terminal that landed through another
  // path (e.g. an interrupt fallback) while the natural terminal was parked.
  private discardParkedTerminal(targetKey: string): void {
    const turn = this.findTurnForKey(targetKey);
    if (!turn || !turn.pendingTerminal) return;
    turn.pendingTerminal = undefined;
    turn.pendingTerminalType = undefined;
    turn.terminalEmitted = true;
    this.removePendingTurn(turn);
    if (this.activeTurn === turn) this.activeTurn = null;
  }

  private async markCommandTerminal(
    turn: PendingTurn,
    type:
      | "turn.completed"
      | "turn.failed"
      | "turn.interrupted"
      | "turn.awaiting_input",
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
      await this.publishTurnTerminalOrDefer(
        turn,
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID: turn.turnID,
          clientNonce: turn.clientNonce,
          source: "claude",
          type: "turn.failed",
          reason: "provider_failure",
          error: message,
        }),
        "turn.failed",
      );
      return;
    }
    await this.markCommandTerminal(turn, "turn.failed").catch((markErr) =>
      console.error(
        "session command failure mark failed:",
        markErr,
        "original:",
        err,
      ),
    );
  }

  private async registerWakeup(
    req: WakeupRequest,
    scheduledTurnID: string,
  ): Promise<void> {
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

  // maybeRegisterBackgroundTaskWake closes the silent-stranding gap where a
  // run_in_background task finishes while the session is idle: a task-lifecycle
  // SDK frame never starts a turn, so without this the "re-invokes you when it
  // exits" follow-up is lost. We register a durable backend wake on a NATURAL
  // terminal; the orchestrator owns the fire decision (Active + not
  // awaiting-input) and submits the turn through the same boundary as a user
  // turn. We skip when a turn is active because that turn already receives the
  // bound shell_task.exited in-turn and can act on it — only an idle terminal
  // needs a wake. The backend Register is idempotent, so the worst case of the
  // idle race (a pending turn about to start) is one harmless extra wake.
  private async maybeRegisterBackgroundTaskWake(
    event: ClaudeProviderEvent,
    observedEventID: string,
  ): Promise<void> {
    const terminal = claudeTerminalBackgroundTask(event);
    if (!terminal) return;
    if (this.activeTurn && !this.activeTurn.terminalEmitted) {
      // The completion was delivered INTO this active turn — the model has
      // the result in hand and can act on it now. No wake is needed, and any
      // PENDING wake for the same task (armed by an earlier observation, or
      // re-adopted across a restart) would be a duplicate notification once
      // this turn ends; retire it.
      try {
        await cancelBackgroundTaskWake(this.cfg, {
          taskID: terminal.taskID,
          reason: "delivered_mid_turn",
        });
      } catch (err) {
        console.error("background task wake cancel failed:", err);
      }
      return;
    }
    // Dedupe by observation identity, not task id: a task-id-only key blocked
    // the corrective registration after a premature fire forever (the
    // once-only wake burn). The backend dedupes same-observation re-registers
    // durably; this set only avoids repeat HTTP calls.
    const dedupeKey = `${terminal.taskID}${observedEventID}`;
    if (this.firedBackgroundTaskWakes.has(dedupeKey)) return;
    boundedSetAdd(
      this.firedBackgroundTaskWakes,
      dedupeKey,
      MAX_FIRED_BACKGROUND_WAKES,
    );
    try {
      const registered = await registerBackgroundTaskWake(this.cfg, {
        ...terminal,
        observedEventID,
      });
      backgroundTaskWakeTotal
        .labels(registered ? "registered" : "disabled")
        .inc();
    } catch (err) {
      backgroundTaskWakeTotal.labels("failed").inc();
      console.error("background task wake register failed:", err);
    }
  }

  // adoptOrphanedBackgroundTasks closes the durable lifecycle of tasks a
  // runner restart orphaned. Exposed with an injectable task list for tests;
  // production fetches from the orchestrator's unresolved endpoint.
  async adoptOrphanedBackgroundTasks(
    tasks?: Array<{
      taskID: string;
      turnID: string;
      description: string;
      summary: string;
      startedEventID: string;
    }>,
  ): Promise<number> {
    const unresolved = tasks ?? (await fetchUnresolvedBackgroundTasks(this.cfg));
    let closed = 0;
    for (const task of unresolved) {
      if (!task.taskID || !task.turnID) continue;
      const exited = claudeRestartClosureEvent(this.cfg, task);
      const dispatched = await dispatch(this.sink, exited).catch((err) => {
        console.error("restart closure publish failed:", err);
        return false;
      });
      if (!dispatched) continue;
      closed += 1;
      try {
        const registered = await registerBackgroundTaskWake(this.cfg, {
          taskID: task.taskID,
          status: "unknown",
          description: task.description ?? "",
          summary: task.summary ?? "",
          lastToolName: "Bash",
          error:
            "The runner restarted while this task was running; its output is no longer retrievable through BashOutput/TaskOutput. Verify the task's effects directly.",
          // Stable per orphaned run: repeated restarts re-derive the same
          // observation from the same started event, so they dedupe instead
          // of stacking wake generations.
          observedEventID: String(
            (exited as { event_id?: unknown }).event_id ?? "",
          ),
        });
        backgroundTaskWakeTotal
          .labels(registered ? "registered" : "disabled")
          .inc();
      } catch (err) {
        backgroundTaskWakeTotal.labels("failed").inc();
        console.error("restart closure wake register failed:", err);
      }
    }
    return closed;
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
    record: SessionCommandRecord,
    err: unknown,
  ): Promise<void> {
    const prompt = String(record.prompt ?? "").trim();
    const pendingTurn = this.acceptTurn(
      prompt,
      commandClientNonce(record),
      record,
    );
    if (!pendingTurn) {
      await this.commandBus.markFailed(record, err);
      return;
    }
    pendingTurn.stopCommandHeartbeat =
      this.commandBus.startCommandHeartbeat(record);
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

function parseOptionalTimestampMs(value: unknown): number | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

// claudeRestartClosureEvent builds the corrective shell_task.exited that
// closes a task orphaned by a runner restart. status=unknown — the restart
// severed the SDK task registry, so completion was never observed and must
// not be claimed. The event id is deterministic in the orphaned run's
// started_event_id, so repeated restarts re-derive the SAME observation and
// the wake registration dedupes instead of stacking generations.
export function claudeRestartClosureEvent(
  cfg: Config,
  task: { taskID: string; turnID: string; startedEventID: string },
): TankConversationEvent {
  return shellTaskEvent({
    sessionID: cfg.sessionId,
    turnID: task.turnID,
    source: "claude",
    type: "shell_task.exited",
    taskID: task.taskID,
    status: "unknown",
    payload: {
      status: "unknown",
      completion_source: "runner_restart",
      started_event_id: task.startedEventID,
      error:
        "runner restarted while this task was running; completion was never observed",
    },
  }) as TankConversationEvent;
}
