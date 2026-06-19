# Session Bar Contract

This contract applies to the left session list, active-session row, status
chips, unread counts, age/timer labels, delete controls, and other sidebar
state derived from session activity.

## Product Model

The session bar is a compact control surface for choosing and managing live
sessions. It must not present state that contradicts the durable session and
turn ledgers. It may be optimistic about selection and hover UI, but not about
work completion, deletion, unread counts, or running state.

## Sources Of Truth

- `session_registry` owns session identity, owner, mode, lifecycle, pod
  metadata, repositories, clone state, and spawned-session lineage
  (`sessions.spawned_sessions`, the parent→child links the spawned-sessions
  chip lists).
- `session_events` owns turn activity and terminal run outcomes.
- `sessions.activity_summary` is a projection for sidebar display; it must
  converge from durable events and not replace them as the investigation
  source of truth.
- Session-list SSE wakes the browser to refetch or apply durable session
  changes; it is not the source of state.

## Migration Rules

- Do not keep browser-local running, unread, or delete-success state that can
  contradict durable state.
- Do not treat a control as complete before the durable terminal event or
  registry state confirms it.
- Do not preserve old polling paths as a fallback after the contracted live
  path exists.
- When replacing sidebar state ownership, delete stale selectors, tests, and
  display branches that still read the old source.

## Live Behavior

- Status chips converge without refresh when the durable run state changes.
- A terminal turn event clears running/stopping state without requiring a page
  reload.
- Unread counts and read markers derive from durable cursor/read state.
- Session create, delete, and lifecycle changes reach every open session list
  owned by the user through the contracted live path.
- The selected row may update immediately for navigation, but durable state
  must correct it without manual refresh.

## Failure And Recovery

- Browser reconnect resumes session-list delivery from a durable or explicit
  snapshot boundary.
- Stale auth must surface as an authentication recovery path, not as silent
  stale sidebar state.
- Orchestrator rollout must not leave a session row permanently running when
  durable terminal state exists.
- Pod deletion is terminal for the session lifecycle and must be reflected as
  such rather than hidden behind a retry loop.

## Observability

- Metrics must distinguish session-list stream open, reconnect, resync,
  heartbeat, emitted change, auth failure, and server error.
- A mismatch between `sessions.activity_summary` and terminal
  `session_events` must be diagnosable and should be counted or alerted when
  it affects user-visible running state.
- Delete and stop controls need outcome metrics that separate requested,
  confirmed, failed, and already-terminal states.

## Acceptance Checks

- A turn terminal event updates the session row from running/stopping to the
  terminal display state without refresh.
- A new unread message increments unread state for non-selected sessions and
  clears through the durable read cursor.
- Delete does not appear complete until the durable session lifecycle confirms
  it.
- Reconnect or resync produces the same session bar state as a fresh load.
- A session spawned by an agent appears in its origin session's
  spawned-sessions chip as a working link, converging from the durable
  `sessions.spawned_sessions` row without a manual refresh, and is absent for
  sessions that spawned nothing or were created without an origin.
- A same-scope spawned child renders as a single indented tier directly under
  its origin in the session list, grouped from the child's durable
  `sessions.parent_session_id` (stamped in the same write that creates the
  child). Because the pointer arrives with the child row, the child appears
  already nested on its first snapshot/row-update — it must not first render as
  a top-level row and then reflow into place. Nesting never exceeds one tier
  (deeper lineage is clamped to the same tier under the top-level ancestor), and
  a cross-scope test-slot child — whose origin is not in the
  `(email, session_scope)`-scoped list — does not nest. The grouping must not
  drop, duplicate, or reorder a root relative to the durable `sidebar_position`
  order.
- Tests cover a projection lag or missed wake scenario and prove the sidebar
  catches up from durable state.
