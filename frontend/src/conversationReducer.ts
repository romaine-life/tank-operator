import type { TankConversationEvent } from "./tankConversation";

export type ConversationRunStatus =
  | "ready"
  | "submitted"
  | "streaming"
  | "needs_input"
  | "stopped"
  | "error";

export type ConversationItemStatus = "started" | "streaming" | "completed" | "failed";

export interface ConversationMessage {
  id: string;
  role: "user" | "assistant" | "system";
  text: string;
  turnId?: string;
  clientNonce?: string;
  orderKey?: string;
}

export interface ConversationItem {
  id: string;
  turnId?: string;
  parentId?: string;
  actor: "assistant" | "system" | "tool" | "runner";
  kind: string;
  status: ConversationItemStatus;
  title?: string;
  text?: string;
  orderKey?: string;
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
  lastOrderKey: string | null;
  lastReadOrderKey: string | null;
  unreadCount: number;
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
  lastOrderKey: null,
  lastReadOrderKey: null,
  unreadCount: 0,
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
    case "conversation.started":
      return next;
    case "conversation.archived":
      return { ...next, runStatus: "ready", activeTurnId: null, activeItemId: null };
    case "user_message.created":
      return applyUserMessage(next, event);
    case "turn.submitted":
      return {
        ...next,
        runStatus: "submitted",
        activeTurnId: event.turn_id ?? next.activeTurnId,
        failed: false,
      };
    case "turn.started":
      return {
        ...next,
        runStatus: "streaming",
        activeTurnId: event.turn_id ?? next.activeTurnId,
        failed: false,
      };
    case "turn.completed":
      return {
        ...next,
        runStatus: "ready",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: false,
      };
    case "turn.failed":
      return {
        ...next,
        runStatus: "error",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: true,
      };
    case "turn.interrupted":
      return {
        ...next,
        runStatus: "stopped",
        activeTurnId: null,
        activeItemId: null,
        needsInput: false,
        failed: false,
      };
    case "item.started":
      return upsertItem(next, event, "started");
    case "item.delta":
      return upsertItem(next, event, "streaming", true);
    case "item.completed":
      return upsertItem(
        { ...next, activeItemId: matchingActiveItem(next, event) ? null : next.activeItemId },
        event,
        "completed",
      );
    case "item.failed":
      return upsertItem(
        { ...next, runStatus: "error", failed: true },
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
      return {
        ...next,
        runStatus: next.activeTurnId ? "streaming" : "ready",
        needsInput: false,
      };
    case "session.activity_updated":
      return applyActivity(next, event);
    case "read_state.updated":
      return applyReadState(next, event);
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
  const message: ConversationMessage = {
    id: event.item_id ?? event.event_id,
    role: "user",
    text,
    turnId: event.turn_id,
    clientNonce: event.client_nonce,
    orderKey: event.order_key,
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
  appendDelta = false,
): ConversationReducerState {
  const id = event.item_id ?? event.event_id;
  const existing = state.items.find((item) => item.id === id);
  const text = stringPayload(event, appendDelta ? "delta" : "text");
  const item: ConversationItem = {
    id,
    turnId: event.turn_id,
    parentId: event.parent_id,
    actor: event.actor === "user" ? "runner" : event.actor,
    kind: stringPayload(event, "kind") ?? defaultItemKind(event),
    status,
    title: stringPayload(event, "title") ?? existing?.title,
    text: appendDelta ? [existing?.text, text].filter(Boolean).join("") : (text ?? existing?.text),
    orderKey: event.order_key ?? existing?.orderKey,
  };
  const items = existing
    ? state.items.map((candidate) => (candidate.id === id ? item : candidate))
    : [...state.items, item];
  return {
    ...state,
    items,
    activeItemId: status === "started" || status === "streaming" ? id : state.activeItemId,
  };
}

function applyActivity(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  const status = stringPayload(event, "status");
  const unreadCount = numberPayload(event, "unread_count");
  return {
    ...state,
    runStatus: isRunStatus(status) ? status : state.runStatus,
    needsInput: booleanPayload(event, "needs_input") ?? state.needsInput,
    failed: booleanPayload(event, "failed") ?? state.failed,
    activeTurnId: stringPayload(event, "active_turn_id") ?? state.activeTurnId,
    unreadCount: unreadCount ?? state.unreadCount,
  };
}

function applyReadState(
  state: ConversationReducerState,
  event: TankConversationEvent,
): ConversationReducerState {
  return {
    ...state,
    lastReadOrderKey:
      stringPayload(event, "last_read_order_key") ?? event.order_key ?? state.lastReadOrderKey,
    unreadCount: 0,
  };
}

function matchingActiveItem(
  state: ConversationReducerState,
  event: TankConversationEvent,
): boolean {
  return Boolean(event.item_id && state.activeItemId === event.item_id);
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

function numberPayload(event: TankConversationEvent, key: string): number | undefined {
  const value = event.payload?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function booleanPayload(event: TankConversationEvent, key: string): boolean | undefined {
  const value = event.payload?.[key];
  return typeof value === "boolean" ? value : undefined;
}

function isRunStatus(value: string | undefined): value is ConversationRunStatus {
  return (
    value === "ready" ||
    value === "submitted" ||
    value === "streaming" ||
    value === "needs_input" ||
    value === "stopped" ||
    value === "error"
  );
}
