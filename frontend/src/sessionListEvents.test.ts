import { test } from "node:test";
import assert from "node:assert/strict";

import {
  applySessionListEvent,
  normalizeSessionListEvent,
  type SessionListEvent,
  type SessionListReducerState,
} from "./sessionListEvents";

// emptyState is the shared starting state for the reducer tests. The
// reducer is pure, so each test builds its own narrative on top of this.
function emptyState(): SessionListReducerState {
  return { sessions: [], activities: {} };
}

function makeEvent(overrides: Partial<SessionListEvent> & { type: SessionListEvent["type"] }): SessionListEvent {
  return {
    order_key: "1",
    email: "u@example.com",
    session_scope: "default",
    session_id: "21",
    event_id: overrides.type,
    occurred_at: "2026-05-16T19:35:15Z",
    payload: {},
    ...overrides,
  };
}

test("normalizeSessionListEvent rejects unknown event types", () => {
  const got = normalizeSessionListEvent({
    order_key: "1",
    session_id: "21",
    event_id: "x",
    type: "session.something_invented",
  });
  assert.equal(got, null);
});

test("normalizeSessionListEvent requires order_key / session_id / event_id", () => {
  assert.equal(
    normalizeSessionListEvent({ type: "session.created", session_id: "21", event_id: "x" }),
    null,
    "missing order_key must be rejected",
  );
  assert.equal(
    normalizeSessionListEvent({ type: "session.created", order_key: "1", event_id: "x" }),
    null,
    "missing session_id must be rejected",
  );
});

test("session.created adds a fresh session to the list", () => {
  const next = applySessionListEvent(emptyState(), makeEvent({
    type: "session.created",
    payload: { mode: "claude_gui", pod_name: "session-21" },
  }));
  assert.equal(next.sessions.length, 1);
  assert.equal(next.sessions[0].id, "21");
  assert.equal(next.sessions[0].mode, "claude_gui");
  assert.equal(next.sessions[0].pod_name, "session-21");
});

test("session.created is a no-op when the session is already in the list", () => {
  // Mirrors the race: POST /api/sessions returns the optimistic row,
  // setSessions adds it, then session.created arrives on SSE. Reducer
  // must not produce a duplicate.
  const initial = emptyState();
  initial.sessions = [
    {
      id: "21",
      pod_name: "session-21",
      owner: "u@example.com",
      status: "Pending",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
    },
  ];
  const next = applySessionListEvent(initial, makeEvent({
    type: "session.created",
    payload: { mode: "claude_gui" },
  }));
  assert.equal(next.sessions, initial.sessions, "no mutation expected for duplicate create");
});

test("session.pod_ready updates the existing session's status + ready_at", () => {
  const initial = emptyState();
  initial.sessions = [
    {
      id: "21",
      pod_name: "session-21",
      owner: "u@example.com",
      status: "Pending",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
    },
  ];
  const next = applySessionListEvent(initial, makeEvent({
    type: "session.pod_ready",
    payload: { status: "Active", ready_at: "2026-05-16T19:35:19Z" },
  }));
  assert.equal(next.sessions[0].status, "Active");
  assert.equal(next.sessions[0].ready_at, "2026-05-16T19:35:19Z");
});

test("session.pod_failed synthesizes a placeholder when no session row exists yet", () => {
  // Failure case from the original bug: the sidebar hadn't yet
  // received the matching /api/sessions row when the pod evicted.
  // Reducer must surface the Failed indicator anyway so the user sees
  // the dead session, not nothing.
  const next = applySessionListEvent(emptyState(), makeEvent({
    type: "session.pod_failed",
    payload: { status: "Failed", reason: "Evicted", exit_code: 137 },
  }));
  assert.equal(next.sessions.length, 1);
  assert.equal(next.sessions[0].id, "21");
  assert.equal(next.sessions[0].status, "Failed");
});

test("session.deleted removes the session and its activity entry", () => {
  const initial = emptyState();
  initial.sessions = [
    {
      id: "21",
      pod_name: "session-21",
      owner: "u@example.com",
      status: "Active",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
    },
    {
      id: "22",
      pod_name: "session-22",
      owner: "u@example.com",
      status: "Active",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
    },
  ];
  initial.activities = {
    "21": {
      session_id: "21",
      status: "ready",
      last_order_key: null,
      unread_count: 0,
      needs_input: false,
      failed: false,
      active_turn_id: null,
      updated_at: null,
    },
  };
  const next = applySessionListEvent(initial, makeEvent({
    type: "session.deleted",
    payload: {},
  }));
  assert.equal(next.sessions.length, 1);
  assert.equal(next.sessions[0].id, "22");
  assert.equal(next.activities["21"], undefined);
});

test("session.name_changed updates the name field on the matching row", () => {
  const initial = emptyState();
  initial.sessions = [
    {
      id: "21",
      pod_name: "session-21",
      owner: "u@example.com",
      status: "Active",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: "Old name",
    },
  ];
  const next = applySessionListEvent(initial, makeEvent({
    type: "session.name_changed",
    payload: { name: "Renamed" },
  }));
  assert.equal(next.sessions[0].name, "Renamed");
});

test("session.activity_changed updates the per-session activity entry", () => {
  const initial = emptyState();
  initial.sessions = [
    {
      id: "21",
      pod_name: "session-21",
      owner: "u@example.com",
      status: "Active",
      mode: "claude_gui",
      requested_at: null,
      created_at: null,
      ready_at: null,
      name: null,
    },
  ];
  const next = applySessionListEvent(initial, makeEvent({
    type: "session.activity_changed",
    payload: {
      status: "streaming",
      active_turn_id: "turn-1",
      needs_input: false,
      failed: false,
      last_order_key: "100",
      unread_count: 3,
    },
  }));
  const activity = next.activities["21"];
  assert.ok(activity, "activity entry must exist after the event");
  assert.equal(activity.status, "streaming");
  assert.equal(activity.active_turn_id, "turn-1");
  assert.equal(activity.unread_count, 3);
});
