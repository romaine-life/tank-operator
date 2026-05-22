# Session Lifecycle Contract

This contract applies to creating, loading, readying, stopping, deleting, and
terminating sessions, including the session pod boundary.

## Product Model

A Tank session is a user-owned pod-backed workspace with explicit lifecycle
state. The product should make the lifecycle legible without pretending that a
dead pod can be resurrected or that a request succeeded before durable state
confirms it.

## Sources Of Truth

- `session_registry` owns session rows and lifecycle metadata.
- Kubernetes owns pod existence and pod phase.
- `session_events` owns user-visible chat/run lifecycle events.
- `sessions.activity_summary` may summarize current activity, but durable
  lifecycle and event rows explain the state.

## Migration Rules

- Do not keep old lifecycle branches, route aliases, pod allocators, or tests
  once a lifecycle path has moved.
- Do not introduce compatibility for unknown callers during lifecycle
  migrations.
- Do not use browser-only session state to stand in for durable lifecycle
  state.
- Do not silently continue a session after the pod-death boundary.

## Live Behavior

- Creating a session writes durable session state before the UI depends on the
  new session.
- Loading and ready status shown to the user must be durable when it appears in
  transcript or sidebar state.
- Session readiness should arrive through the live path without forcing a
  transcript or session-list reset.
- Stop/delete controls must move through requested and confirmed states that
  match durable outcomes.

## Failure And Recovery

- Browser disconnect, orchestrator rollout, and runner-process restart are
  inside the durability boundary while the same session pod is alive.
- Session-pod death is outside the messaging durability boundary. The session
  is terminal because the `emptyDir` workspace is gone.
- Failed create, load, stop, and delete operations must leave visible failure
  state and durable or observable evidence.
- Repeated actions should be idempotent or return the already-terminal durable
  state.

## Observability

- Metrics must cover create, load, ready, stop, delete, pod-watch, and terminal
  outcomes.
- There must be enough durable and live telemetry to distinguish pod failure,
  runner failure, stream failure, and browser display lag.
- Stuck loading/running/deleting states need counters or alerts once they
  exceed their product boundary.

## Acceptance Checks

- Create produces a durable session row and the expected initial events before
  user-visible live state depends on them.
- Ready state appears without transcript reset or session-list refresh.
- Stop/delete controls require durable confirmation before success display.
- Pod death moves the session to the terminal lifecycle state expected by the
  product.
- Repeating a lifecycle command after success returns or displays the durable
  terminal state rather than failing ambiguously.
