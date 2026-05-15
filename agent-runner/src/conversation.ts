import { createHash } from "node:crypto";

export const TANK_ACTORS = ["user", "assistant", "system", "tool", "runner"] as const;

export type TankActor = (typeof TANK_ACTORS)[number];

export const TANK_EVENT_SOURCES = ["tank", "claude", "codex"] as const;

export type TankEventSource = (typeof TANK_EVENT_SOURCES)[number];

export const TANK_VISIBILITIES = ["durable", "live-only", "audit-only"] as const;

export type TankVisibility = (typeof TANK_VISIBILITIES)[number];

export const TANK_EVENT_TYPES = [
  "conversation.started",
  "conversation.archived",
  "user_message.created",
  "turn.submitted",
  "turn.started",
  "turn.completed",
  "turn.failed",
  "turn.command_failed",
  "turn.interrupted",
  "item.started",
  "item.delta",
  "item.completed",
  "item.failed",
  "tool.approval_requested",
  "tool.approval_resolved",
  "session.activity_updated",
  "read_state.updated",
] as const;

export type TankEventType = (typeof TANK_EVENT_TYPES)[number];

export interface TankConversationEvent {
  event_id: string;
  order_key?: string;
  sequence?: number;
  conversation_id?: string;
  session_id: string;
  turn_id?: string;
  timeline_id?: string;
  provider_item_id?: string;
  parent_id?: string;
  client_nonce?: string;
  actor: TankActor;
  source: TankEventSource;
  type: TankEventType;
  created_at: string;
  producer?: {
    name?: string;
    version?: string;
    runtime?: string;
    provider_event_id?: string;
  };
  visibility: TankVisibility;
  payload?: Record<string, unknown>;
  [key: string]: unknown;
}

export type UserMessageDisplay =
  | { kind: "plain" }
  | { kind: "skill_invocation"; skill_name: string; supplemental_text?: string };

const TANK_EVENT_TYPE_SET = new Set<string>(TANK_EVENT_TYPES);
const TANK_ACTOR_SET = new Set<string>(TANK_ACTORS);
const TANK_EVENT_SOURCE_SET = new Set<string>(TANK_EVENT_SOURCES);
const TANK_VISIBILITY_SET = new Set<string>(TANK_VISIBILITIES);

export function isTankConversationEvent(event: { [key: string]: unknown }): event is TankConversationEvent {
  if (
    typeof event.event_id === "string" &&
    typeof event.session_id === "string" &&
    typeof event.type === "string" &&
    TANK_EVENT_TYPE_SET.has(event.type) &&
    typeof event.actor === "string" &&
    TANK_ACTOR_SET.has(event.actor) &&
    typeof event.source === "string" &&
    TANK_EVENT_SOURCE_SET.has(event.source) &&
    typeof event.created_at === "string" &&
    typeof event.visibility === "string" &&
    TANK_VISIBILITY_SET.has(event.visibility)
  ) {
    return isValidEventByType(event);
  }
  return false;
}

export function isDurableTankConversationEvent(event: { [key: string]: unknown }): boolean {
  return isTankConversationEvent(event) && event.visibility !== "live-only" && hasOrderCursor(event);
}

export function normalizeClientNonce(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

export function turnIDForClientNonce(clientNonce: string): string {
  return `turn_${stableIDPart(clientNonce)}`;
}

export function userTimelineID(turnID: string): string {
  return `${turnID}:user`;
}

export function itemTimelineID(turnID: string, providerItemID: string): string {
  return `${turnID}:item:${stableIDPart(providerItemID)}`;
}

export function userSubmissionEvents(args: {
  sessionID: string;
  clientNonce: string;
  text: string;
  message: unknown;
  runtime: "claude" | "codex";
  skillName?: string;
  now?: string;
}): { turnID: string; userMessage: TankConversationEvent; turnSubmitted: TankConversationEvent } {
  const text = requireNonEmpty(args.text, "text");
  const clientNonce = requireNonEmpty(args.clientNonce, "clientNonce");
  const createdAt = args.now ?? new Date().toISOString();
  const turnID = turnIDForClientNonce(clientNonce);
  const producer = { name: `${args.runtime}-runner`, runtime: args.runtime };
  const display = userMessageDisplay(args.skillName, text);
  return {
    turnID,
    userMessage: {
      event_id: `${turnID}:user_message.created`,
      conversation_id: args.sessionID,
      session_id: args.sessionID,
      turn_id: turnID,
      timeline_id: userTimelineID(turnID),
      client_nonce: clientNonce,
      actor: "user",
      source: "tank",
      type: "user_message.created",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        text,
        message: args.message,
        display,
      },
    },
    turnSubmitted: {
      event_id: `${turnID}:turn.submitted`,
      conversation_id: args.sessionID,
      session_id: args.sessionID,
      turn_id: turnID,
      client_nonce: clientNonce,
      actor: "runner",
      source: "tank",
      type: "turn.submitted",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        status: "submitted",
      },
    },
  };
}

export function turnEvent(args: {
  sessionID: string;
  turnID: string;
  clientNonce?: string;
  source: "claude" | "codex";
  type: "turn.started" | "turn.completed" | "turn.failed" | "turn.interrupted";
  reason?: string;
  usage?: unknown;
  error?: unknown;
  providerEventID?: string;
}): TankConversationEvent {
  const payload: Record<string, unknown> = {};
  if (args.reason) payload.reason = args.reason;
  if (args.usage !== undefined) payload.usage = args.usage;
  if (args.error !== undefined) payload.error = args.error;
  return {
    event_id: `${args.turnID}:${args.type}:${args.reason ?? args.providerEventID ?? "runner"}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    ...(args.clientNonce ? { client_nonce: args.clientNonce } : {}),
    actor: "runner",
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
      ...(args.providerEventID ? { provider_event_id: args.providerEventID } : {}),
    },
    visibility: "durable",
    ...(Object.keys(payload).length > 0 ? { payload } : {}),
  };
}

export function itemEvent(args: {
  sessionID: string;
  turnID: string;
  source: "claude" | "codex";
  type:
    | "item.started"
    | "item.delta"
    | "item.completed"
    | "item.failed"
    | "tool.approval_requested"
    | "tool.approval_resolved";
  providerItemID: string;
  parentID?: string;
  actor: TankActor;
  visibility?: TankVisibility;
  providerEventID?: string;
  payload?: Record<string, unknown>;
}): TankConversationEvent {
  return {
    event_id: `${args.turnID}:${args.type}:${stableIDPart(args.providerItemID)}:${args.providerEventID ?? "runner"}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    timeline_id: itemTimelineID(args.turnID, args.providerItemID),
    provider_item_id: args.providerItemID,
    parent_id: args.parentID ?? args.turnID,
    actor: args.actor,
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
      ...(args.providerEventID ? { provider_event_id: args.providerEventID } : {}),
    },
    visibility: args.visibility ?? "durable",
    ...(args.payload ? { payload: args.payload } : {}),
  };
}

function stableIDPart(value: string): string {
  const safe = value
    .trim()
    .replace(/[^A-Za-z0-9_.:-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
  const hash = createHash("sha256").update(value).digest("hex").slice(0, 12);
  if (safe.length >= 6 && safe.length <= 80) return safe;
  if (safe.length > 80) return `${safe.slice(0, 64)}-${hash}`;
  return hash;
}

function isValidEventByType(event: { [key: string]: unknown }): boolean {
  switch (event.type) {
    case "user_message.created":
      return hasStrings(event, ["turn_id", "timeline_id", "client_nonce"]) && isUserMessagePayload(event.payload);
    case "turn.submitted":
      return event.actor === "runner" && event.source === "tank" && hasStrings(event, ["turn_id", "client_nonce"]) && isStringPayload(event.payload, "status");
    case "turn.started":
    case "turn.completed":
    case "turn.failed":
    case "turn.interrupted":
      return event.actor === "runner" && hasStrings(event, ["turn_id"]);
    case "item.started":
    case "item.delta":
    case "item.completed":
    case "item.failed":
      return hasStrings(event, ["turn_id", "timeline_id"]) && isStringPayload(event.payload, "kind");
    case "tool.approval_requested":
    case "tool.approval_resolved":
      return event.actor === "tool" && hasStrings(event, ["turn_id", "timeline_id"]) && isStringPayload(event.payload, "kind");
    case "session.activity_updated":
      return isStringPayload(event.payload, "status");
    case "read_state.updated":
      return isStringPayload(event.payload, "last_read_order_key");
    default:
      return true;
  }
}

function hasOrderCursor(event: { [key: string]: unknown }): boolean {
  return typeof event.order_key === "string" && event.order_key
    ? true
    : typeof event.sequence === "number" && Number.isInteger(event.sequence) && event.sequence >= 0;
}

function hasStrings(event: { [key: string]: unknown }, keys: string[]): boolean {
  return keys.every((key) => typeof event[key] === "string" && event[key]);
}

function isUserMessagePayload(payload: unknown): boolean {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  const record = payload as Record<string, unknown>;
  if (typeof record.text !== "string" || !record.text) return false;
  return isUserMessageDisplay(record.display);
}

function isUserMessageDisplay(display: unknown): display is UserMessageDisplay {
  if (!display || typeof display !== "object" || Array.isArray(display)) return false;
  const record = display as Record<string, unknown>;
  if (record.kind === "plain") return true;
  return record.kind === "skill_invocation" && isSkillName(record.skill_name);
}

function isStringPayload(payload: unknown, key: string): boolean {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  const value = (payload as Record<string, unknown>)[key];
  return typeof value === "string" && value.length > 0;
}

function userMessageDisplay(skillName: string | undefined, text: string): UserMessageDisplay {
  if (!skillName) return { kind: "plain" };
  if (!isSkillName(skillName)) throw new Error("skillName is invalid");
  return {
    kind: "skill_invocation",
    skill_name: skillName,
    supplemental_text: skillSupplementalText(skillName, text),
  };
}

function skillSupplementalText(skillName: string, text: string): string {
  const triggerPattern = new RegExp(`^[$/]${skillName}(?:\\s+|\\n+)?`, "i");
  return text.trim().replace(triggerPattern, "").trim();
}

function isSkillName(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
}

function requireNonEmpty(value: string, field: string): string {
  const trimmed = value.trim();
  if (!trimmed) throw new Error(`${field} is required`);
  return trimmed;
}
