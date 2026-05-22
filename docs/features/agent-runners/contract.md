# Agent Runners Contract

This contract applies to GUI chat runners, command delivery, provider adapters,
tool approvals, interruptions, scheduled wakeups, and runner-produced Tank
events.

## Product Model

Runners turn durable user intent into provider work and durable Tank events.
They are not UI helpers and they are not the source of product history. Their
job is to consume commands, call providers or tools, and emit events that let
the rest of the product reconstruct what happened.

## Sources Of Truth

- JetStream commands own delivery of runner work while the session pod is
  alive.
- `session_events` owns the durable conversation and run outcome.
- Provider SDK streams are adapter input only.
- Runner process memory may hold in-flight provider state but must not be the
  only record of user-visible completed work.

## Migration Rules

- Do not keep provider-specific frontend render paths after provider events
  have moved behind the Tank protocol.
- Do not ack a command before the durable terminal event required by the user
  action has been published.
- Do not keep fallback command paths, polling paths, or local-only stop
  handling after a command/event path exists.
- Delete old tests that assert provider raw events as UI behavior; replace them
  with Tank protocol contract tests.

## Live Behavior

- A submitted user turn produces a durable `turn.submitted`, runner progress,
  and exactly one terminal turn outcome.
- Stop/interrupt remains pending until a durable interrupted, completed,
  failed, or already-terminal event resolves it.
- Tool approval replies are routed to the intended provider item and resolved
  durably.
- Runner events must wake transcript and session-list followers after
  persistence.
- A runner must not require an open browser to continue work.

## Failure And Recovery

- Browser disconnect and orchestrator rollout must not cancel runner work while
  the session pod and runner remain alive.
- Runner-process restart may lose in-process state that is explicitly outside
  the durability boundary, such as current provider call state or pod-local
  scheduled wakeups.
- Command redelivery must be idempotent through command keys, turn IDs, or
  provider item IDs.
- Provider failures must become durable failure events instead of silent
  strandings.

## Observability

- Metrics must distinguish command published, consumed, acked, redelivered,
  failed, and terminal-event-emitted outcomes.
- Runner metrics and backend metrics must be comparable so a bug can be
  localized between command delivery, runner execution, persistence, and live
  client delivery.
- Silent strandings, where a requested action has no terminal event, are a
  counted bug class.

## Acceptance Checks

- A normal turn reaches exactly one durable terminal event.
- Stop/interrupt produces one durable resolution and clears visible pending
  state from that durable resolution.
- Command redelivery does not duplicate user-visible transcript entries.
- Provider output is converted to Tank protocol before browser rendering
  depends on it.
- Runner work continues across browser disconnect and remains reconstructable
  from durable events.
