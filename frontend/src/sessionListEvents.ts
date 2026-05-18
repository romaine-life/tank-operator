// Typed session-list event wire shape + reducer for the sidebar SSE
// stream introduced in tank-operator#83. Replaces the prior
// wake-and-refetch + activity-polling architecture; see
// docs/product-inspirations.md for the constraint set this implements
// ("durable typed events with cursor resume; not polling, not opaque
// wakes"). The chat-window stream in App.tsx has the same shape; only
// the event types and the state it mutates differ.

import {
  normalizeSessionActivity,
  type SessionActivitySummary,
} from "./sessionActivity";

// ----- Event types ---------------------------------------------------------

// Wire event types must match
// backend-go/internal/lifecycleevents/types.go. Renaming either side
// without the other is the bug class the chat-side typed-event surface
// was hardened against; keep the strings here in lockstep with the Go
// constants. The migration-guard
// (scripts/check-removed-chat-runtime.mjs) also references some of
// these names — adjust there if a type is renamed.
export const SESSION_EVENT_TYPES = [
  "session.created",
  "session.deleted",
  "session.name_changed",
  "session.test_state_changed",
  "session.rollout_state_changed",
  "session.pod_scheduled",
  "session.pod_ready",
  "session.pod_not_ready",
  "session.pod_failed",
  "session.pod_terminating",
  "session.activity_changed",
] as const;

export type SessionListEventType = (typeof SESSION_EVENT_TYPES)[number];

export interface SessionListEvent {
  order_key: string;
  email: string;
  session_scope: string;
  session_id: string;
  type: SessionListEventType;
  event_id: string;
  occurred_at: string;
  payload: Record<string, unknown>;
}

// ----- Wire parsing --------------------------------------------------------

// normalizeSessionListEvent parses one wire payload from /api/sessions/events
// or /api/sessions/timeline. session_scope is required: the backend SSE
// handler only subscribes to its own (email, scope) NATS subject and only
// catches up from that scope's slice of session_lifecycle_events, so a
// well-formed payload always carries the scope it was produced under.
// Rejecting payloads without a scope makes the wire shape an explicit
// contract rather than letting a malformed payload silently land in
// reducer state with a defaulted scope — which was the bug class behind
// the "deletes come back" + "test-slot sessions in prod sidebar" symptoms
// that pre-#83 cross-scope reads produced.
export function normalizeSessionListEvent(value: unknown): SessionListEvent | null {
  if (!isRecord(value)) return null;
  const type = value.type;
  if (typeof type !== "string" || !isSessionListEventType(type)) return null;
  const sessionId = stringField(value, "session_id");
  const orderKey = stringField(value, "order_key");
  const eventId = stringField(value, "event_id");
  const sessionScope = stringField(value, "session_scope");
  if (!sessionId || !orderKey || !eventId || !sessionScope) return null;
  return {
    order_key: orderKey,
    email: stringField(value, "email") ?? "",
    session_scope: sessionScope,
    session_id: sessionId,
    type,
    event_id: eventId,
    occurred_at: stringField(value, "occurred_at") ?? "",
    payload: isRecord(value.payload) ? (value.payload as Record<string, unknown>) : {},
  };
}

export function isSessionListEventType(value: string): value is SessionListEventType {
  return (SESSION_EVENT_TYPES as readonly string[]).includes(value);
}

// ----- Reducer -------------------------------------------------------------

// SessionListShape is the narrow Session subset the reducer mutates. Kept
// local-only so this module doesn't depend on App.tsx's Session
// interface (which carries a lot of UI-only sugar fields and stricter
// mode/test_state types). The annotated fields use `unknown` /
// `string` rather than the App.tsx-specific union types because the
// reducer treats them opaquely — the App.tsx Session interface is a
// structural superset of this shape, so generic instantiation
// `SessionListShape<Session>` works.
export interface SessionListShape {
  id: string;
  pod_name: string | null;
  owner: string;
  status: string;
  mode: string;
  requested_at: string | null;
  created_at: string | null;
  ready_at: string | null;
  name: string | null;
  test_state?: unknown;
  rollout_state?: unknown;
  activity?: SessionActivitySummary | null;
}

export interface SessionListReducerState<S extends SessionListShape = SessionListShape> {
  sessions: S[];
  activities: Record<string, SessionActivitySummary>;
}

// applySessionListEvent is the pure reducer; the App.tsx integration
// wraps it in setSessions/setSessionActivities calls. Returns a new
// state object — callers that care about reference equality should
// compare to the input to skip a re-render when nothing changed.
//
// For unknown session ids on lifecycle events that imply the session
// already exists (anything other than session.created), the reducer
// stores the activity / status update on the activities map / a
// placeholder Session that subsequent /api/sessions snapshots will fill
// in. This keeps the SSE stream consistent during the window between
// "pod_ready arrived" and "the next /api/sessions list rolls in".
export function applySessionListEvent<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
  options: { sessionFactory?: (id: string) => S } = {},
): SessionListReducerState<S> {
  const factory = options.sessionFactory ?? defaultSessionFactory<S>();

  switch (event.type) {
    case "session.created":
      return createSession(state, event, factory);
    case "session.deleted":
      return deleteSession(state, event);
    case "session.name_changed":
      return patchSessionField(state, event, (s) => ({
        ...s,
        name: stringOrNull(event.payload.name),
      }));
    case "session.test_state_changed":
      return patchSessionField(state, event, (s) => ({
        ...s,
        test_state: recordOrNull(event.payload),
      }));
    case "session.rollout_state_changed":
      return patchSessionField(state, event, (s) => ({
        ...s,
        rollout_state: recordOrNull(event.payload),
      }));
    case "session.pod_scheduled":
    case "session.pod_ready":
    case "session.pod_not_ready":
    case "session.pod_failed":
    case "session.pod_terminating":
      return applyPodStatusEvent(state, event, factory);
    case "session.activity_changed":
      return applyActivityEvent(state, event);
    default:
      return state;
  }
}

function defaultSessionFactory<S extends SessionListShape>(): (id: string) => S {
  return (id) =>
    ({
      id,
      pod_name: null,
      owner: "",
      status: "Pending",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
      test_state: null,
      rollout_state: null,
      activity: null,
    }) as unknown as S;
}

function createSession<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
  factory: (id: string) => S,
): SessionListReducerState<S> {
  const existing = state.sessions.find((s) => s.id === event.session_id);
  if (existing) {
    // Optimistic POST /api/sessions response already added the row. Just
    // backfill any missing fields the create event carries.
    return state;
  }
  const next = factory(event.session_id);
  next.owner = event.email || next.owner;
  next.mode = stringField(event.payload, "mode") ?? next.mode;
  next.pod_name = stringOrNull(event.payload.pod_name);
  next.requested_at = stringOrNull(event.payload.requested_at);
  next.created_at = stringOrNull(event.payload.created_at);
  return { ...state, sessions: [...state.sessions, next] };
}

function deleteSession<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
): SessionListReducerState<S> {
  if (!state.sessions.some((s) => s.id === event.session_id)) return state;
  const sessions = state.sessions.filter((s) => s.id !== event.session_id);
  const activities = { ...state.activities };
  delete activities[event.session_id];
  return { sessions, activities };
}

function patchSessionField<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
  patch: (s: S) => S,
): SessionListReducerState<S> {
  const idx = state.sessions.findIndex((s) => s.id === event.session_id);
  if (idx === -1) return state;
  const next = patch(state.sessions[idx]);
  if (next === state.sessions[idx]) return state;
  const sessions = state.sessions.slice();
  sessions[idx] = next;
  return { ...state, sessions };
}

function applyPodStatusEvent<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
  factory: (id: string) => S,
): SessionListReducerState<S> {
  const status = stringField(event.payload, "status");
  if (!status) return state;
  const readyAt = stringOrNull(event.payload.ready_at);
  const updateSession = (session: S): S => ({
    ...session,
    status,
    ready_at: readyAt ?? session.ready_at,
  });
  const idx = state.sessions.findIndex((s) => s.id === event.session_id);
  if (idx === -1) {
    // Pod transitioned before the matching /api/sessions row landed.
    // Synthesize a placeholder so the status renders correctly; the
    // next snapshot will fill in the missing fields (mode, name, etc).
    const placeholder = updateSession(factory(event.session_id));
    placeholder.owner = event.email || placeholder.owner;
    placeholder.pod_name = stringOrNull(event.payload.pod_name) ?? placeholder.pod_name;
    return { ...state, sessions: [...state.sessions, placeholder] };
  }
  const next = updateSession(state.sessions[idx]);
  const sessions = state.sessions.slice();
  sessions[idx] = next;
  return { ...state, sessions };
}

function applyActivityEvent<S extends SessionListShape>(
  state: SessionListReducerState<S>,
  event: SessionListEvent,
): SessionListReducerState<S> {
  // event.payload mirrors the SessionActivitySummary shape — feed it
  // through the same normalizer the /api/sessions snapshot uses so the
  // wire-to-state translation is in one place.
  const summary = normalizeSessionActivity({
    session_id: event.session_id,
    ...event.payload,
  });
  if (!summary) return state;
  const activities = { ...state.activities, [event.session_id]: summary };
  return { ...state, activities };
}

// ----- helpers -------------------------------------------------------------

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function stringOrNull(value: unknown): string | null {
  return typeof value === "string" && value ? value : null;
}

function recordOrNull(value: unknown): Record<string, unknown> | null {
  return isRecord(value) ? value : null;
}
