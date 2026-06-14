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

export function questionClientNonce(askingTurnID, providerTimelineID) {
  return `question-${hashIDPart(`${askingTurnID}\0${providerTimelineID}`)}`;
}

export function questionMessageTimelineID(askingTurnID, providerTimelineID) {
  return `${askingTurnID}:assistant_question:${stableIDPart(providerTimelineID)}`;
}

export function shellTaskTimelineID(turnID, taskID) {
  return `${turnID}:shell_task:${stableIDPart(taskID)}`;
}

export function userSubmissionEvents(args) {
  const text = requireNonEmpty(args.text, "text");
  const clientNonce = requireNonEmpty(args.clientNonce, "clientNonce");
  const createdAt = args.now ?? new Date().toISOString();
  const turnID = turnIDForClientNonce(clientNonce);
  const producer = { name: `${args.runtime}-runner`, runtime: args.runtime };
  const display = userMessageDisplay(args.skillName, text);
  const attachments = userMessageAttachments(args.attachments);
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
        ...(attachments.length > 0 ? { attachments } : {}),
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

function userMessageAttachments(input) {
  if (!Array.isArray(input)) return [];
  return input.flatMap((attachment) => {
    if (!attachment || typeof attachment !== "object") return [];
    const label = String(attachment.label || attachment.name || "").trim();
    const name = String(attachment.name || attachment.label || "").trim();
    if (!label || !name) return [];
    const kind = attachment.kind === "image" ? "image" : "file";
    return [
      {
        label,
        name,
        kind,
        ...(typeof attachment.path === "string" && attachment.path.trim()
          ? { path: attachment.path.trim() }
          : {}),
        ...(typeof attachment.absPath === "string" && attachment.absPath.trim()
          ? { absPath: attachment.absPath.trim() }
          : {}),
        ...(typeof attachment.size === "number" &&
        Number.isFinite(attachment.size) &&
        attachment.size >= 0
          ? { size: attachment.size }
          : {}),
      },
    ];
  });
}

export function turnEvent(args) {
  const payload = {};
  if (args.reason) payload.reason = args.reason;
  if (args.usage !== undefined) payload.usage = args.usage;
  if (args.usageObservation !== undefined)
    payload.usage_observation = args.usageObservation;
  if (args.error !== undefined) payload.error = args.error;
  if (args.finalAnswer !== undefined)
    payload.final_answer = normalizeFinalAnswer(args.finalAnswer);
  // background_work_pending stamps whether provider-tracked background work
  // is still in flight at this terminal; the user-facing-turn projection folds a would-be-ready
  // terminal to the non-summoning scheduled status when set (#906 spine).
  if (args.backgroundWorkPending !== undefined)
    payload.background_work_pending = args.backgroundWorkPending;
  // turn.awaiting_input carries the Tank-canonical questions the agent asked
  // (the pause point for the asking turn) plus the AskUserQuestion item ids
  // the /answer endpoint targets. See docs/tank-conversation-protocol.md.
  if (args.questions !== undefined) payload.questions = args.questions;
  if (args.askingTurnID) payload.asking_turn_id = args.askingTurnID;
  if (args.questionTurnID) payload.question_turn_id = args.questionTurnID;
  if (args.awaitingProviderItemID)
    payload.provider_item_id = args.awaitingProviderItemID;
  if (args.awaitingTimelineID) payload.timeline_id = args.awaitingTimelineID;
  if (args.awaitingProviderTimelineID)
    payload.provider_timeline_id = args.awaitingProviderTimelineID;
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
  if (args.providerEventID)
    event.producer.provider_event_id = args.providerEventID;
  if (Object.keys(payload).length > 0) event.payload = payload;
  return event;
}

export function askUserQuestionHandoffEvents(args) {
  const sessionID = requireNonEmpty(args.sessionID, "sessionID");
  const askingTurnID = requireNonEmpty(args.askingTurnID, "askingTurnID");
  const askingClientNonce = requireNonEmpty(
    args.askingClientNonce,
    "askingClientNonce",
  );
  const source = requireNonEmpty(args.source, "source");
  const providerItemID = requireNonEmpty(args.providerItemID, "providerItemID");
  const providerTimelineID = requireNonEmpty(
    args.providerTimelineID,
    "providerTimelineID",
  );
  const questions = requireQuestions(args.questions);
  const questionNonce = questionClientNonce(askingTurnID, providerTimelineID);
  const questionTurnID = turnIDForClientNonce(questionNonce);
  const questionTimelineID = itemTimelineID(questionTurnID, providerItemID);
  const text = formatAskUserQuestionText(questions);
  const createdAt = new Date().toISOString();
  const producer = { name: `${source}-runner`, runtime: source };
  // Optional plan markdown for ExitPlanMode pauses. The plan-approval shape
  // reuses this AskUserQuestion handoff: one Approve/Request-changes question
  // plus the full plan body, which the Turns question page renders as markdown
  // above the question. Empty/absent for ordinary AskUserQuestion pauses.
  const plan =
    typeof args.plan === "string" && args.plan.trim() ? args.plan : undefined;
  const askingTurnFinalAnswer =
    args.finalAnswer === undefined
      ? undefined
      : normalizeFinalAnswer(args.finalAnswer);
  const awaitingInput = {
    asking_turn_id: askingTurnID,
    question_turn_id: questionTurnID,
    provider_item_id: providerItemID,
    timeline_id: questionTimelineID,
    provider_timeline_id: providerTimelineID,
    questions,
    ...(plan ? { plan } : {}),
    ...(askingTurnFinalAnswer
      ? { asking_turn_final_answer: askingTurnFinalAnswer }
      : {}),
  };
  return {
    questionClientNonce: questionNonce,
    questionTurnID,
    questionTimelineID,
    questionMessage: {
      event_id: `${askingTurnID}:assistant_message.created:ask_user_question:${stableIDPart(providerTimelineID)}`,
      conversation_id: sessionID,
      session_id: sessionID,
      turn_id: askingTurnID,
      timeline_id: questionMessageTimelineID(askingTurnID, providerTimelineID),
      provider_item_id: providerItemID,
      actor: "assistant",
      source,
      type: "assistant_message.created",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        text,
        message: { role: "assistant", content: text },
        display: { kind: "ask_user_question" },
        awaiting_input: awaitingInput,
      },
    },
    invocation: {
      event_id: `${askingTurnID}:turn.awaiting_input.invocation:${stableIDPart(providerTimelineID)}`,
      conversation_id: sessionID,
      session_id: sessionID,
      turn_id: askingTurnID,
      timeline_id: providerTimelineID,
      provider_item_id: providerItemID,
      actor: "runner",
      source,
      type: "turn.awaiting_input.invocation",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: {
        provider_item_id: providerItemID,
        timeline_id: providerTimelineID,
        questions,
      },
    },
    questionSubmitted: {
      event_id: `${questionTurnID}:turn.submitted`,
      conversation_id: sessionID,
      session_id: sessionID,
      turn_id: questionTurnID,
      client_nonce: questionNonce,
      actor: "runner",
      source: "tank",
      type: "turn.submitted",
      created_at: createdAt,
      producer: { name: "tank-operator", runtime: source },
      visibility: "durable",
      payload: { status: "submitted" },
    },
    awaitingInput: {
      event_id: `${questionTurnID}:turn.awaiting_input:runner`,
      conversation_id: sessionID,
      session_id: sessionID,
      turn_id: questionTurnID,
      client_nonce: questionNonce,
      provider_item_id: providerItemID,
      actor: "runner",
      source,
      type: "turn.awaiting_input",
      created_at: createdAt,
      producer,
      visibility: "durable",
      payload: awaitingInput,
    },
  };
}

function requireQuestions(value) {
  if (!Array.isArray(value) || value.length === 0) {
    throw new TypeError("questions must be a non-empty array");
  }
  return value;
}

function formatAskUserQuestionText(questions) {
  return questions
    .map((question, index) => {
      const record = question && typeof question === "object" ? question : {};
      const text =
        typeof record.question === "string" && record.question.trim()
          ? record.question.trim()
          : typeof record.header === "string" && record.header.trim()
            ? record.header.trim()
            : "Answer to continue.";
      return `${index + 1}. ${text}`;
    })
    .join("\n");
}

function normalizeFinalAnswer(finalAnswer) {
  if (
    !finalAnswer ||
    typeof finalAnswer !== "object" ||
    Array.isArray(finalAnswer)
  ) {
    throw new TypeError("finalAnswer must be an object");
  }
  const timelineIDs = nonEmptyStringArray(
    finalAnswer.timelineIDs ?? finalAnswer.timeline_ids,
    "finalAnswer.timelineIDs",
  );
  const providerItemIDs = optionalNonEmptyStringArray(
    finalAnswer.providerItemIDs ?? finalAnswer.provider_item_ids,
    "finalAnswer.providerItemIDs",
  );
  const out = { timeline_ids: timelineIDs };
  if (providerItemIDs !== undefined) out.provider_item_ids = providerItemIDs;
  return out;
}

function nonEmptyStringArray(value, field) {
  if (!Array.isArray(value) || value.length === 0) {
    throw new TypeError(`${field} must be a non-empty string array`);
  }
  return value.map((item) => {
    if (typeof item !== "string" || !item.trim()) {
      throw new TypeError(`${field} must be a non-empty string array`);
    }
    return item.trim();
  });
}

function optionalNonEmptyStringArray(value, field) {
  if (value === undefined) return undefined;
  return nonEmptyStringArray(value, field);
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
    visibility: "durable",
  };
  if (args.providerEventID)
    event.producer.provider_event_id = args.providerEventID;
  if (args.payload) event.payload = args.payload;
  return event;
}

export function shellTaskEvent(args) {
  const taskID = requireNonEmpty(args.taskID, "taskID");
  const status = requireNonEmpty(args.status, "status");
  const payload = {
    ...(args.payload ?? {}),
    kind: "shell_task",
    task_id: taskID,
    status,
  };
  const providerEventPart =
    args.providerEventID ?? stableIDPart(JSON.stringify(payload));
  const event = {
    event_id: `${args.turnID}:${args.type}:${stableIDPart(taskID)}:${providerEventPart}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: args.turnID,
    timeline_id: shellTaskTimelineID(args.turnID, taskID),
    task_id: taskID,
    provider_item_id: args.providerItemID ?? taskID,
    parent_id: args.parentID ?? args.turnID,
    actor: "tool",
    source: args.source,
    type: args.type,
    created_at: new Date().toISOString(),
    producer: {
      name: `${args.source}-runner`,
      runtime: args.source,
    },
    visibility: "durable",
    payload,
  };
  if (args.providerEventID)
    event.producer.provider_event_id = args.providerEventID;
  return event;
}

// contextCompactedEvent records that the provider summarized earlier
// conversation context to reclaim context-window space. It is a durable,
// turn-scoped system notice (actor=runner, mirroring turn.usage: the runner
// observed a provider event and emitted the Tank-shape equivalent). The
// backend projection records it as an ordinary mid-turn Turn-activity row —
// intra-turn system noise, the same tier as tool calls and reasoning — so it
// surfaces in the turn's activity disclosure, not the settled transcript. See
// docs/tank-conversation-protocol.md → "Context Compaction Notice".
export function contextCompactedEvent(args) {
  const turnID = requireNonEmpty(args.turnID, "turnID");
  const source = requireNonEmpty(args.source, "source");
  const trigger = args.trigger === "manual" ? "manual" : "auto";
  const payload = { trigger };
  if (
    typeof args.preTokens === "number" &&
    Number.isFinite(args.preTokens) &&
    args.preTokens >= 0
  ) {
    payload.pre_tokens = Math.floor(args.preTokens);
  }
  const providerPart = args.providerEventID
    ? stableIDPart(args.providerEventID)
    : stableIDPart(
        JSON.stringify({ trigger, preTokens: payload.pre_tokens ?? null }),
      );
  const event = {
    event_id: `${turnID}:context.compacted:${providerPart}`,
    conversation_id: args.sessionID,
    session_id: args.sessionID,
    turn_id: turnID,
    actor: "runner",
    source,
    type: "context.compacted",
    created_at: new Date().toISOString(),
    producer: {
      name: `${source}-runner`,
      runtime: source,
    },
    visibility: "durable",
    payload,
  };
  if (args.providerEventID)
    event.producer.provider_event_id = args.providerEventID;
  return event;
}

const VALID_VISIBILITIES = new Set(["durable"]);

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
    throw new TypeError(
      `stampTankEvent: event_id is required (type=${event?.type})`,
    );
  }
  if (
    typeof event.visibility !== "string" ||
    !VALID_VISIBILITIES.has(event.visibility)
  ) {
    throw new TypeError(
      `stampTankEvent: visibility is required (type=${event.type})`,
    );
  }
  tankEventSeq += 1;
  const now = Date.now();
  const uuid =
    typeof event.uuid === "string" && event.uuid ? event.uuid : event.event_id;
  const writtenAt =
    typeof event.written_at === "string" && event.written_at
      ? event.written_at
      : new Date(now).toISOString();
  const orderKey =
    typeof event.order_key === "string" && event.order_key
      ? event.order_key
      : [
          String(now).padStart(13, "0"),
          String(tankEventSeq).padStart(8, "0"),
          uuid,
        ].join("-");
  const sequence =
    typeof event.sequence === "number" ? event.sequence : tankEventSeq;
  const createdAt =
    typeof event.created_at === "string" && event.created_at
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
  const hash = hashIDPart(trimmed);
  if (safe.length >= 6 && safe.length <= 80) return safe;
  if (safe.length > 80) return `${safe.slice(0, 64)}-${hash}`;
  return hash;
}

function hashIDPart(value) {
  return createHash("sha256")
    .update(String(value ?? ""))
    .digest("hex")
    .slice(0, 12);
}

function userMessageDisplay(skillName, text) {
  const trimmed = (skillName ?? "").trim();
  if (!trimmed) return { kind: "plain" };
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(trimmed))
    throw new Error("skillName is invalid");
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
