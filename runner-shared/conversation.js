// Shared Tank conversation contract: actor/source/type/visibility enums, the
// envelope shape, and the type guards. This module is the read-side surface
// consumed by the frontend (browser) AND by the runners. It deliberately
// does NOT import node:crypto so it can be bundled by Vite for the browser.
//
// Builder functions (turnEvent, itemEvent, userSubmissionEvents,
// stampTankEvent, stableIDPart) live in runner-shared/conversation-builders.js
// — that module DOES import node:crypto and is consumed only by the runners.
//
// JSON Schema at schemas/tank-conversation-event.schema.json is the upstream
// authority; conversation-contract.yml enforces drift detection between this
// module's exported arrays and the schema's enums.

export const TANK_ACTORS = ["user", "assistant", "system", "tool", "runner"];

export const TANK_EVENT_SOURCES = ["tank", "claude", "codex", "hermes"];

// Tank events are durable-by-design; `live-only` was retired once the
// producer-side live channel never landed. The enum stays single-valued so
// callers still tag durability explicitly rather than infer it.
export const TANK_VISIBILITIES = ["durable"];

export const TANK_EVENT_TYPES = [
  "user_message.created",
  "turn.submitted",
  "turn.started",
  "turn.completed",
  "turn.failed",
  "turn.command_failed",
  "turn.interrupt_requested",
  "turn.interrupted",
  "item.started",
  "item.completed",
  "item.failed",
  "tool.approval_requested",
  "tool.approval_resolved",
];

const TANK_EVENT_TYPE_SET = new Set(TANK_EVENT_TYPES);
const TANK_ACTOR_SET = new Set(TANK_ACTORS);
const TANK_EVENT_SOURCE_SET = new Set(TANK_EVENT_SOURCES);
const TANK_VISIBILITY_SET = new Set(TANK_VISIBILITIES);

export function isTankConversationEvent(event) {
  if (!event || typeof event !== "object") return false;
  const candidate = event;
  if (
    typeof candidate.event_id !== "string" || !candidate.event_id ||
    typeof candidate.session_id !== "string" || !candidate.session_id ||
    typeof candidate.type !== "string" ||
    !TANK_EVENT_TYPE_SET.has(candidate.type) ||
    typeof candidate.actor !== "string" ||
    !TANK_ACTOR_SET.has(candidate.actor) ||
    typeof candidate.source !== "string" ||
    !TANK_EVENT_SOURCE_SET.has(candidate.source) ||
    typeof candidate.created_at !== "string" || !candidate.created_at ||
    typeof candidate.visibility !== "string" ||
    !TANK_VISIBILITY_SET.has(candidate.visibility)
  ) {
    return false;
  }
  if (!hasOrderCursor(candidate)) return false;
  return isValidEventByType(candidate);
}

export function isDurableTankConversationEvent(event) {
  // All Tank events are durable now (the `live-only` visibility was
  // retired); the predicate is kept for call-site clarity but is now a
  // synonym for `isTankConversationEvent`.
  return isTankConversationEvent(event);
}

export function normalizeClientNonce(value) {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

function isValidEventByType(event) {
  switch (event.type) {
    case "user_message.created":
      return event.actor === "user" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id", "timeline_id", "client_nonce"]) &&
        isUserMessagePayload(event.payload);
    case "turn.submitted":
      return event.actor === "runner" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id", "client_nonce"]) &&
        isStringPayload(event.payload, "status");
    case "turn.started":
    case "turn.completed":
    case "turn.failed":
    case "turn.interrupted":
      return event.actor === "runner" && hasStrings(event, ["turn_id"]);
    case "turn.command_failed":
      return event.actor === "system" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id"]) &&
        isStringPayload(event.payload, "reason");
    case "turn.interrupt_requested":
      return event.actor === "system" &&
        event.source === "tank" &&
        hasStrings(event, ["turn_id"]);
    case "item.started":
    case "item.completed":
    case "item.failed":
      return hasStrings(event, ["turn_id", "timeline_id"]) &&
        isStringPayload(event.payload, "kind") &&
        isItemOutcome(event.payload.outcome);
    case "tool.approval_requested":
    case "tool.approval_resolved":
      return event.actor === "tool" &&
        hasStrings(event, ["turn_id", "timeline_id"]) &&
        isStringPayload(event.payload, "kind") &&
        isItemOutcome(event.payload.outcome);
    default:
      return false;
  }
}

function hasOrderCursor(event) {
  return typeof event.order_key === "string" && event.order_key.length > 0;
}

function hasStrings(event, keys) {
  return keys.every((key) => typeof event[key] === "string" && event[key]);
}

function isUserMessagePayload(payload) {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  if (typeof payload.text !== "string" || !payload.text) return false;
  return isUserMessageDisplay(payload.display);
}

function isUserMessageDisplay(display) {
  if (!display || typeof display !== "object" || Array.isArray(display)) return false;
  if (display.kind === "plain") return true;
  return display.kind === "skill_invocation" &&
    isSkillName(display.skill_name) &&
    (display.supplemental_text === undefined || typeof display.supplemental_text === "string");
}

function isStringPayload(payload, key) {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return false;
  const value = payload[key];
  return typeof value === "string" && value.length > 0;
}

function isItemOutcome(outcome) {
  if (outcome === undefined) return true;
  if (!outcome || typeof outcome !== "object" || Array.isArray(outcome)) return false;
  if (outcome.kind === "ok") return outcome.reason === undefined;
  if (outcome.kind === "result_failed") {
    return ["claude_tool_result_is_error", "codex_item_status_failed", "exit_code"].includes(outcome.reason);
  }
  return outcome.kind === "execution_failed" && outcome.reason === "provider_item_error";
}

function isSkillName(value) {
  return typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
}
