import type { TankConversationEvent, UserMessageDisplay } from "../../runner-shared/conversation.js";

export type ConversationRunStatus =
  | "ready"
  | "submitted"
  | "streaming"
  | "needs_input"
  | "stopped"
  | "error";

export type ConversationItemStatus = "started" | "completed" | "failed";

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
}

export interface ConversationReducerState {
  seenEventIds: string[];
  seenClientNonces: string[];
  messages: ConversationMessage[];
  items: ConversationItem[];
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
        ...next,
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
        ...next,
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
        ...next,
        runStatus: "error",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: true,
        lastError: errorText(event),
      };
    case "turn.interrupted":
      return {
        ...next,
        runStatus: "stopped",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: false,
        lastError: null,
      };
    case "item.started":
      return upsertItem(next, event, "started");
    case "item.completed":
      return upsertItem(
        { ...next, activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId },
        event,
        "completed",
      );
    case "item.failed":
      return upsertItem(
        {
          ...next,
          runStatus: "error",
          failed: true,
          activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId,
          lastError: errorText(event),
        },
        event,
        "failed",
      );
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
        "completed",
      );
  }
}

export function reduceConversationEvents(
  events: readonly TankConversationEvent[],
  seed: ConversationReducerState = initialConversationState,
): ConversationReducerState {
  return events.reduce(conversationReducer, seed);
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
  };
  return {
    ...state,
    seenClientNonces: event.client_nonce
      ? [...state.seenClientNonces, event.client_nonce]
      : state.seenClientNonces,
    messages: [...state.messages, message],
  };
}

function upsertItem(
  state: ConversationReducerState,
  event: TankConversationEvent,
  status: ConversationItemStatus,
): ConversationReducerState {
  if (!event.timeline_id || !event.turn_id) return state;
  const id = event.timeline_id;
  const existing = state.items.find((item) => item.id === id);
  const text = stringPayload(event, "text");
  const payload = { ...(existing?.payload ?? {}), ...(event.payload ?? {}) };
  const item: ConversationItem = {
    id,
    turnId: event.turn_id,
    parentId: event.parent_id,
    providerItemId: event.provider_item_id,
    actor: event.actor === "user" ? "runner" : event.actor,
    kind: stringPayload(event, "kind") ?? existing?.kind ?? defaultItemKind(event),
    status,
    title: stringPayload(event, "title") ?? existing?.title,
    text: text ?? existing?.text,
    payload,
    orderKey: event.order_key ?? existing?.orderKey,
    sourceEventId: event.event_id,
    createdAt: event.created_at || existing?.createdAt,
  };
  const items = existing
    ? state.items.map((candidate) => (candidate.id === id ? item : candidate))
    : [...state.items, item];
  return {
    ...state,
    items,
    activeItemId: status === "started" ? id : state.activeItemId,
  };
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
