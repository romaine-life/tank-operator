import type { TankConversationEvent, UserMessageDisplay } from "../../runner-shared/conversation.js";

export type ConversationRunStatus =
  | "ready"
  | "submitted"
  | "streaming"
  | "needs_input"
  | "stopping"
  | "stopped"
  | "error";

export type ConversationItemStatus = "started" | "completed" | "failed";
export type ConversationTurnTerminalStatus = "completed" | "failed" | "interrupted";
export type ConversationBackgroundTaskStatus =
  | "running"
  | "completed"
  | "failed"
  | "stopped"
  | "unknown";

export interface ConversationMessage {
  id: string;
  role: "user" | "assistant" | "system";
  text: string;
  turnId?: string;
  clientNonce?: string;
  display?: UserMessageDisplay;
  orderKey?: string;
  sourceEventId?: string;
  createdAt?: string;
  // Originating tank-operator session id for user messages authored by
  // a sibling session via the mcp-tank-operator send_prompt /
  // spawn_run_session handoff path. Drives the user-bubble avatar in the
  // renderer: when set, the parent session's deterministic avatar
  // replaces the human owner's Gravatar so the handoff reads as
  // agent-authored. Absent for normal human-typed turns.
  originSessionId?: string;
  // Severity tag for system-role messages. session.status:failed events
  // with the extended payload (failure_scope=provider) carry this so
  // the renderer can style the bubble as an error rather than a
  // neutral system notice (e.g. "Session is ready."). undefined means
  // "info" — the default.
  severity?: "info" | "error";
  // Optional actionable affordance ("Re-sign-in to Codex" → /...).
  // Present on session.status:failed events that include an action in
  // their durable payload; the renderer surfaces it as a button on the
  // system-role message bubble.
  action?: { label: string; href: string };
}

export interface ConversationItem {
  id: string;
  turnId?: string;
  parentId?: string;
  providerItemId?: string;
  actor: "assistant" | "system" | "tool" | "runner";
  kind: string;
  status: ConversationItemStatus;
  title?: string;
  text?: string;
  payload?: Record<string, unknown>;
  orderKey?: string;
  sourceEventId?: string;
  createdAt?: string;
  startedAt?: string;
  completedAt?: string;
}

export interface ConversationInterruptRequest {
  id: string;
  turnId: string;
  clientNonce?: string;
  orderKey?: string;
  time: string;
}

export interface ConversationTurnTerminal {
  turnId: string;
  status: ConversationTurnTerminalStatus;
  clientNonce?: string;
  orderKey?: string;
  time: string;
  sourceEventId: string;
  // Unwrapped error text for failed/command_failed turns. Drives the
  // transcript meta line that renders at the turn's terminal order_key.
  // Undefined on completed/interrupted turns.
  detail?: string;
}

export interface ConversationBackgroundTask {
  id: string;
  taskId: string;
  turnId?: string;
  providerItemId?: string;
  toolUseId?: string;
  status: ConversationBackgroundTaskStatus;
  summary?: string;
  description?: string;
  lastToolName?: string;
  command?: string;
  cwd?: string;
  processId?: string;
  output?: string;
  exitCode?: number;
  durationMs?: number;
  rawItem?: unknown;
  error?: unknown;
  orderKey?: string;
  sourceEventId?: string;
  createdAt?: string;
  startedAt?: string;
  updatedAt?: string;
  completedAt?: string;
}

export interface ConversationReducerState {
  seenEventIds: string[];
  seenClientNonces: string[];
  messages: ConversationMessage[];
  items: ConversationItem[];
  interruptRequests: ConversationInterruptRequest[];
  turnTerminals: Record<string, ConversationTurnTerminal>;
  backgroundTasks: ConversationBackgroundTask[];
  runStatus: ConversationRunStatus;
  activeTurnId: string | null;
  activeItemId: string | null;
  needsInput: boolean;
  failed: boolean;
  lastError: string | null;
  lastUsage: unknown | null;
  lastOrderKey: string | null;
}

export const initialConversationState: ConversationReducerState = {
  seenEventIds: [],
  seenClientNonces: [],
  messages: [],
  items: [],
  interruptRequests: [],
  turnTerminals: {},
  backgroundTasks: [],
  runStatus: "ready",
  activeTurnId: null,
  activeItemId: null,
  needsInput: false,
  failed: false,
  lastError: null,
  lastUsage: null,
  lastOrderKey: null,
};

export function conversationReducer(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  if (state.seenEventIds.includes(event.event_id)) return state;

  let next: ConversationReducerState = {
    ...state,
    seenEventIds: [...state.seenEventIds, event.event_id],
    lastOrderKey: event.order_key ?? state.lastOrderKey,
  };

  switch (event.type) {
    case "user_message.created":
      return applyUserMessage(next, event);
    case "turn.submitted":
      return {
        ...next,
        runStatus: "submitted",
        activeTurnId: event.turn_id ?? next.activeTurnId,
        failed: false,
        lastError: null,
      };
    case "turn.started":
      return {
        ...next,
        runStatus: "streaming",
        activeTurnId: event.turn_id ?? next.activeTurnId,
        failed: false,
        lastError: null,
      };
    case "turn.completed":
      return {
        ...applyTurnTerminal(next, event, "completed"),
        runStatus: "ready",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: false,
        lastError: null,
        lastUsage: event.payload?.usage ?? next.lastUsage,
      };
    case "turn.failed":
      return {
        ...applyTurnTerminal(next, event, "failed"),
        runStatus: "error",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: true,
        lastError: errorText(event),
        lastUsage: event.payload?.usage ?? next.lastUsage,
      };
    case "turn.command_failed":
      return {
        ...applyTurnTerminal(next, event, "failed"),
        runStatus: "error",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: true,
        lastError: errorText(event),
      };
    case "turn.interrupt_requested":
      return applyInterruptRequested(next, event);
    case "turn.interrupted":
      return {
        ...applyTurnTerminal(next, event, "interrupted"),
        runStatus: "stopped",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: false,
        lastError: null,
      };
    case "session.status":
      return applySessionStatusMessage(next, event);
    case "item.started":
      return upsertItem(next, event, "started");
    case "item.completed":
      return upsertItem(
        { ...next, activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId },
        event,
        completedItemStatus(event),
      );
    case "item.failed":
      // item.failed marks ONE tool call as errored — it does NOT change
      // session run state. The agent will usually recover and continue;
      // flipping runStatus to "error" on every tool error left the pill
      // pinned red for healthy mid-turn sessions. Session-level error
      // comes from turn.failed / turn.command_failed (durable turn
      // terminal events). The per-item error indicator in the transcript
      // continues to render off the item's "failed" status set here.
      // Mirrors backend sessionactivity.DeriveActivitySummary — both
      // consumers treat item.failed as item-scoped, not session-scoped.
      return upsertItem(
        {
          ...next,
          activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId,
        },
        event,
        "failed",
      );
    case "shell_task.started":
      return upsertBackgroundTask(next, event, "running");
    case "shell_task.updated":
      return upsertBackgroundTask(next, event, backgroundTaskStatus(event));
    case "shell_task.exited":
      return upsertBackgroundTask(next, event, backgroundTaskTerminalStatus(event));
    case "tool.approval_requested":
      return upsertItem(
        {
          ...next,
          runStatus: "needs_input",
          needsInput: true,
          activeTurnId: event.turn_id ?? next.activeTurnId,
        },
        event,
        "started",
      );
    case "tool.approval_resolved":
      return upsertItem(
        {
          ...next,
          runStatus: next.activeTurnId ? "streaming" : "ready",
          activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId,
          needsInput: false,
        },
        event,
        completedItemStatus(event),
      );
  }
}

export function reduceConversationEvents(
  events: readonly TankConversationEvent[],
  seed: ConversationReducerState = initialConversationState,
): ConversationReducerState {
  return events.reduce(conversationReducer, seed);
}

function applyTurnTerminal(
  state: ConversationReducerState,
  event: TankConversationEvent,
  status: ConversationTurnTerminalStatus,
): ConversationReducerState {
  if (!event.turn_id) return state;
  const detail = status === "failed" ? errorText(event) ?? undefined : undefined;
  return {
    ...state,
    turnTerminals: {
      ...state.turnTerminals,
      [event.turn_id]: {
        turnId: event.turn_id,
        status,
        clientNonce: event.client_nonce,
        orderKey: event.order_key,
        time: event.created_at,
        sourceEventId: event.event_id,
        ...(detail ? { detail } : {}),
      },
    },
  };
}

function applyInterruptRequested(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  if (!event.turn_id) return state;
  const request: ConversationInterruptRequest = {
    id: event.event_id,
    turnId: event.turn_id,
    clientNonce: event.client_nonce,
    orderKey: event.order_key,
    time: event.created_at,
  };
  // Only transition runStatus when the turn is genuinely mid-flight. Late
  // arrivals (request lands after turn.completed / failed / interrupted)
  // append the chip for transparency but do not downgrade a terminal state.
  const transitioning =
    state.runStatus === "submitted" ||
    state.runStatus === "streaming" ||
    state.runStatus === "needs_input";
  return {
    ...state,
    interruptRequests: [...state.interruptRequests, request],
    runStatus: transitioning ? "stopping" : state.runStatus,
  };
}

function applyUserMessage(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  if (event.client_nonce && state.seenClientNonces.includes(event.client_nonce)) {
    return state;
  }
  const text = stringPayload(event, "text") ?? stringPayload(event, "message") ?? "";
  if (!event.timeline_id || !event.turn_id || !event.client_nonce || !text) return state;
  const originSessionId = stringTopLevel(event, "origin_session_id");
  const message: ConversationMessage = {
    id: event.timeline_id,
    role: "user",
    text,
    turnId: event.turn_id,
    clientNonce: event.client_nonce,
    display: userMessageDisplay(event),
    orderKey: event.order_key,
    sourceEventId: event.event_id,
    createdAt: event.created_at,
    ...(originSessionId ? { originSessionId } : {}),
  };
  return {
    ...state,
    seenClientNonces: event.client_nonce
      ? [...state.seenClientNonces, event.client_nonce]
      : state.seenClientNonces,
    messages: [...state.messages, message],
  };
}

function applySessionStatusMessage(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  const text = stringPayload(event, "text") ?? "";
  if (!event.timeline_id || !text) return state;
  const status = stringPayload(event, "status");
  const severity: "info" | "error" = status === "failed" ? "error" : "info";
  const action = sessionStatusAction(event);
  const message: ConversationMessage = {
    id: event.timeline_id,
    role: "system",
    text,
    orderKey: event.order_key,
    sourceEventId: event.event_id,
    createdAt: event.created_at,
    severity,
    ...(action ? { action } : {}),
  };
  // session.status:failed banners share their timeline_id across
  // transitions (e.g. failed -> ready on Codex auth recovery). Replace
  // the prior entry rather than appending so a recovery doesn't leave
  // the stale failed banner in the rendered transcript. Other
  // session.status messages (loading / ready boot notices) keep their
  // own unique timeline_ids and never collide here.
  const existingIndex = state.messages.findIndex((m) => m.id === event.timeline_id);
  if (existingIndex >= 0) {
    const next = [...state.messages];
    next[existingIndex] = message;
    return { ...state, messages: next };
  }
  return {
    ...state,
    messages: [...state.messages, message],
  };
}

function sessionStatusAction(
  event: TankConversationEvent,
): { label: string; href: string } | undefined {
  const payload = event.payload;
  if (!payload || typeof payload !== "object") return undefined;
  const raw = (payload as Record<string, unknown>).action;
  if (!raw || typeof raw !== "object") return undefined;
  const label = (raw as { label?: unknown }).label;
  const href = (raw as { href?: unknown }).href;
  if (typeof label === "string" && label.length > 0 &&
      typeof href === "string" && href.length > 0) {
    return { label, href };
  }
  return undefined;
}

function upsertItem(
  state: ConversationReducerState,
  event: TankConversationEvent,
  status: ConversationItemStatus,
): ConversationReducerState {
  if (isCodexUserMessageEchoEvent(event)) return state;
  if (!event.timeline_id || !event.turn_id) return state;
  const id = event.timeline_id;
  const existing = state.items.find((item) => item.id === id);
  const text = stringPayload(event, "text");
  const preserveTerminal =
    status === "started" && existing && isTerminalItemStatus(existing.status);
  const resolvedStatus = preserveTerminal ? existing.status : status;
  const payload = preserveTerminal
    ? { ...(event.payload ?? {}), ...(existing.payload ?? {}) }
    : { ...(existing?.payload ?? {}), ...(event.payload ?? {}) };
  const isResolvedTerminalStatus = isTerminalItemStatus(resolvedStatus);
  const item: ConversationItem = {
    id,
    turnId: event.turn_id,
    parentId: preserveTerminal ? existing.parentId ?? event.parent_id : event.parent_id,
    providerItemId: preserveTerminal
      ? existing.providerItemId ?? event.provider_item_id
      : event.provider_item_id,
    actor: preserveTerminal ? existing.actor : event.actor === "user" ? "runner" : event.actor,
    kind: preserveTerminal
      ? existing.kind
      : stringPayload(event, "kind") ?? existing?.kind ?? defaultItemKind(event),
    status: resolvedStatus,
    title: preserveTerminal
      ? existing.title ?? stringPayload(event, "title")
      : stringPayload(event, "title") ?? existing?.title,
    text: preserveTerminal ? existing.text ?? text : text ?? existing?.text,
    payload,
    orderKey: preserveTerminal ? existing.orderKey ?? event.order_key : event.order_key ?? existing?.orderKey,
    sourceEventId: preserveTerminal ? existing.sourceEventId : event.event_id,
    createdAt: preserveTerminal ? existing.createdAt || event.created_at : event.created_at || existing?.createdAt,
    startedAt: status === "started" && !preserveTerminal
      ? event.created_at
      : existing?.startedAt ?? existing?.createdAt ?? event.created_at,
    completedAt: isResolvedTerminalStatus
      ? preserveTerminal
        ? existing.completedAt ?? existing.createdAt ?? event.created_at
        : event.created_at
      : existing?.completedAt,
  };
  const items = existing
    ? state.items.map((candidate) => (candidate.id === id ? item : candidate))
    : [...state.items, item];
  return {
    ...state,
    items,
    activeItemId:
      resolvedStatus === "started" ? id : state.activeItemId === id ? null : state.activeItemId,
  };
}

function upsertBackgroundTask(
  state: ConversationReducerState,
  event: TankConversationEvent,
  status: ConversationBackgroundTaskStatus,
): ConversationReducerState {
  const taskId = backgroundTaskId(event);
  if (!taskId || !event.timeline_id || !event.turn_id) return state;
  const existing = state.backgroundTasks.find((task) => task.id === event.timeline_id);
  const existingTerminal = existing ? isTerminalBackgroundTaskStatus(existing.status) : false;
  const nextStatus =
    existing && existingTerminal && status === "running" ? existing.status : status;
  const toolUseId = stringPayload(event, "tool_use_id");
  const command = stringPayload(event, "command");
  const task: ConversationBackgroundTask = {
    id: event.timeline_id,
    taskId,
    turnId: event.turn_id,
    providerItemId: event.provider_item_id ?? existing?.providerItemId,
    toolUseId: toolUseId ?? existing?.toolUseId,
    status: nextStatus,
    summary: stringPayload(event, "summary") ?? command ?? existing?.summary,
    description: stringPayload(event, "description") ?? existing?.description,
    lastToolName: stringPayload(event, "last_tool_name") ?? existing?.lastToolName,
    command: command ?? existing?.command,
    cwd: stringPayload(event, "cwd") ?? existing?.cwd,
    processId:
      stringPayload(event, "process_id") ??
      stringPayload(event, "processId") ??
      existing?.processId,
    output: stringPayload(event, "output") ?? existing?.output,
    exitCode:
      numericPayload(event, "exit_code") ??
      numericPayload(event, "exitCode") ??
      existing?.exitCode,
    durationMs:
      numericPayload(event, "duration_ms") ??
      numericPayload(event, "durationMs") ??
      existing?.durationMs,
    rawItem: event.payload?.raw_item ?? existing?.rawItem,
    error: event.payload?.error ?? existing?.error,
    orderKey: existing?.orderKey ?? event.order_key,
    sourceEventId: event.event_id,
    createdAt: existing?.createdAt ?? event.created_at,
    startedAt:
      event.type === "shell_task.started"
        ? event.created_at
        : existing?.startedAt ?? existing?.createdAt ?? event.created_at,
    updatedAt: event.created_at || existing?.updatedAt,
    completedAt: isTerminalBackgroundTaskStatus(nextStatus)
      ? existing?.completedAt ?? event.created_at
      : existing?.completedAt,
  };
  const backgroundTasks = existing
    ? state.backgroundTasks.map((candidate) => (candidate.id === task.id ? task : candidate))
    : [...state.backgroundTasks, task];
  return {
    ...state,
    backgroundTasks,
  };
}

function backgroundTaskId(event: TankConversationEvent): string | undefined {
  if (typeof event.task_id === "string" && event.task_id) return event.task_id;
  return stringPayload(event, "task_id");
}

function backgroundTaskStatus(event: TankConversationEvent): ConversationBackgroundTaskStatus {
  return normalizeBackgroundTaskStatus(stringPayload(event, "status"));
}

function backgroundTaskTerminalStatus(event: TankConversationEvent): ConversationBackgroundTaskStatus {
  const status = normalizeBackgroundTaskStatus(stringPayload(event, "status"));
  return status === "running" || status === "unknown" ? "completed" : status;
}

function normalizeBackgroundTaskStatus(status: string | undefined): ConversationBackgroundTaskStatus {
  switch ((status ?? "").toLowerCase()) {
    case "running":
    case "started":
    case "pending":
    case "in_progress":
    case "in-progress":
    case "updated":
      return "running";
    case "completed":
    case "complete":
    case "success":
    case "succeeded":
    case "exited":
      return "completed";
    case "failed":
    case "error":
      return "failed";
    case "stopped":
    case "cancelled":
    case "canceled":
      return "stopped";
    default:
      return "unknown";
  }
}

function isTerminalBackgroundTaskStatus(status: ConversationBackgroundTaskStatus): boolean {
  return status === "completed" || status === "failed" || status === "stopped";
}

function isTerminalItemStatus(status: ConversationItemStatus): boolean {
  return status === "completed" || status === "failed";
}

function matchingActiveItem(
  state: ConversationReducerState,
  event: TankConversationEvent,
): boolean {
  return Boolean(event.timeline_id && state.activeItemId === event.timeline_id);
}

function defaultItemKind(event: TankConversationEvent): string {
  if (event.type.startsWith("tool.")) return "approval";
  if (event.actor === "assistant") return "message";
  return event.actor;
}

function stringPayload(event: TankConversationEvent, key: string): string | undefined {
  const value = event.payload?.[key];
  return typeof value === "string" ? value : undefined;
}

function numericPayload(event: TankConversationEvent, key: string): number | undefined {
  const value = event.payload?.[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && /^-?\d+(\.\d+)?$/.test(value)) return Number(value);
  return undefined;
}

function completedItemStatus(event: TankConversationEvent): ConversationItemStatus {
  const outcome = event.payload?.outcome;
  if (outcome && typeof outcome === "object" && !Array.isArray(outcome)) {
    return (outcome as { kind?: unknown }).kind === "result_failed" ? "failed" : "completed";
  }
  return nonzeroExitCode(event.payload?.exit_code) || nonzeroExitCode(rawPayload(event)?.exit_code)
    ? "failed"
    : "completed";
}

function rawPayload(event: TankConversationEvent): Record<string, unknown> | undefined {
  const raw = event.payload?.raw_item;
  return raw && typeof raw === "object" && !Array.isArray(raw) ? raw as Record<string, unknown> : undefined;
}

function isCodexUserMessageEchoEvent(event: TankConversationEvent): boolean {
  if (event.source !== "codex") return false;
  if (
    event.type !== "item.started" &&
    event.type !== "item.completed" &&
    event.type !== "item.failed"
  ) {
    return false;
  }
  const raw = rawPayload(event);
  return (
    isUserMessageEchoKind(event.payload?.kind) ||
    isUserMessageEchoKind(event.payload?.title) ||
    isUserMessageEchoKind(raw?.type)
  );
}

function isUserMessageEchoKind(value: unknown): boolean {
  return value === "userMessage" || value === "user_message";
}

function nonzeroExitCode(value: unknown): boolean {
  if (typeof value === "number" && Number.isInteger(value)) return value !== 0;
  if (typeof value === "string" && /^-?\d+$/.test(value)) return Number(value) !== 0;
  return false;
}

// stringTopLevel reads a top-level (envelope) string field from a Tank event.
// Used for fields like `origin_session_id` that ride on the envelope rather
// than inside `payload`, mirroring how `email` and `tank_session_id` are
// stamped server-side. The TankConversationEvent type has
// `[key: string]: unknown` so the lookup is well-typed.
function stringTopLevel(event: TankConversationEvent, key: string): string | undefined {
  const value = (event as Record<string, unknown>)[key];
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

function userMessageDisplay(event: TankConversationEvent): UserMessageDisplay | undefined {
  const display = event.payload?.display;
  if (!display || typeof display !== "object" || Array.isArray(display)) return undefined;
  const record = display as Record<string, unknown>;
  if (record.kind === "plain") return { kind: "plain" };
  if (record.kind !== "skill_invocation") return undefined;
  if (typeof record.skill_name !== "string") return undefined;
  if (
    record.supplemental_text !== undefined &&
    typeof record.supplemental_text !== "string"
  ) {
    return undefined;
  }
  return {
    kind: "skill_invocation",
    skill_name: record.skill_name,
    ...(typeof record.supplemental_text === "string"
      ? { supplemental_text: record.supplemental_text }
      : {}),
  };
}

function errorText(event: TankConversationEvent): string | null {
  const error = event.payload?.error;
  if (typeof error === "string") return error;
  if (error && typeof error === "object" && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string") return message;
  }
  const reason = stringPayload(event, "reason");
  return reason ?? null;
}
