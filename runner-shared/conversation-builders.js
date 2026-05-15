// Tank conversation event builders + the unconditional stamper. These are
// the write-side of the contract: the runners construct events through
// these builders, the dispatcher stamps and publishes them. Browser code
// must NOT import this module — it uses node:crypto for the deterministic
// stableIDPart hash. The read-side (types, predicates) lives in
// runner-shared/conversation.js and is browser-safe.

import { createHash } from "node:crypto";

export function turnIDForClientNonce(clientNonce) {
  return `turn_${stableIDPart(clientNonce)}`;
}

export function userTimelineID(turnID) {
  return `${turnID}:user`;
}

export function itemTimelineID(turnID, providerItemID) {
  return `${turnID}:item:${stableIDPart(providerItemID)}`;
}

export function userSubmissionEvents(args) {
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

export function turnEvent(args) {
  const payload = {};
  if (args.reason) payload.reason = args.reason;
  if (args.usage !== undefined) payload.usage = args.usage;
  if (args.error !== undefined) payload.error = args.error;
  const event = {
    event_id: `${args.turnID}:${args.type}:${args.reason ?? args.providerEventID ?? "runner"}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    actor: "runner",
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
    },
    visibility: "durable",
  };
  if (args.clientNonce) event.client_nonce = args.clientNonce;
  if (args.providerEventID) event.producer.provider_event_id = args.providerEventID;
  if (Object.keys(payload).length > 0) event.payload = payload;
  return event;
}

export function itemEvent(args) {
  const event = {
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
    },
    visibility: args.visibility ?? "durable",
  };
  if (args.providerEventID) event.producer.provider_event_id = args.providerEventID;
  if (args.payload) event.payload = args.payload;
  return event;
}

const VALID_VISIBILITIES = new Set(["durable", "live-only"]);

// stampTankEvent attaches uuid, order_key, sequence, and written_at to a
// built Tank event. Throws TypeError if the input is not a Tank event so
// that callers can't accidentally publish a non-envelope object. This is
// the "fail loud" replacement for the conditional stampers that the prior
// runners shipped, which silently passed half-envelopes through.
let tankEventSeq = 0;
export function stampTankEvent(event) {
  if (!event || typeof event !== "object") {
    throw new TypeError("stampTankEvent: event must be an object");
  }
  if (typeof event.event_id !== "string" || !event.event_id) {
    throw new TypeError(`stampTankEvent: event_id is required (type=${event?.type})`);
  }
  if (typeof event.visibility !== "string" || !VALID_VISIBILITIES.has(event.visibility)) {
    throw new TypeError(`stampTankEvent: visibility is required (type=${event.type})`);
  }
  tankEventSeq += 1;
  const now = Date.now();
  const uuid = typeof event.uuid === "string" && event.uuid ? event.uuid : event.event_id;
  const writtenAt = typeof event.written_at === "string" && event.written_at
    ? event.written_at
    : new Date(now).toISOString();
  const orderKey = typeof event.order_key === "string" && event.order_key
    ? event.order_key
    : [
        String(now).padStart(13, "0"),
        String(tankEventSeq).padStart(8, "0"),
        uuid,
      ].join("-");
  const sequence = typeof event.sequence === "number" ? event.sequence : tankEventSeq;
  const createdAt = typeof event.created_at === "string" && event.created_at
    ? event.created_at
    : writtenAt;
  return {
    ...event,
    uuid,
    event_id: event.event_id,
    written_at: writtenAt,
    order_key: orderKey,
    sequence,
    created_at: createdAt,
  };
}

export function stableIDPart(value) {
  const trimmed = String(value ?? "").trim();
  let safe = trimmed.replace(/[^A-Za-z0-9_.:-]+/g, "-");
  safe = safe.replace(/-+/g, "-").replace(/^-|-$/g, "");
  const hash = createHash("sha256").update(trimmed).digest("hex").slice(0, 12);
  if (safe.length >= 6 && safe.length <= 80) return safe;
  if (safe.length > 80) return `${safe.slice(0, 64)}-${hash}`;
  return hash;
}

function userMessageDisplay(skillName, text) {
  const trimmed = (skillName ?? "").trim();
  if (!trimmed) return { kind: "plain" };
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(trimmed)) throw new Error("skillName is invalid");
  return {
    kind: "skill_invocation",
    skill_name: trimmed,
    supplemental_text: skillSupplementalText(trimmed, text),
  };
}

function skillSupplementalText(skillName, text) {
  const triggerPattern = new RegExp(`^[$/]${skillName}(?:\\s+|\\n+)?`, "i");
  return text.trim().replace(triggerPattern, "").trim();
}

function requireNonEmpty(value, field) {
  if (typeof value !== "string") throw new Error(`${field} is required`);
  const trimmed = value.trim();
  if (!trimmed) throw new Error(`${field} is required`);
  return trimmed;
}
