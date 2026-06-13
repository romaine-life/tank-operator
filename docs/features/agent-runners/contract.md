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

- A submitted user turn produces a durable `turn.submitted`, visible
  pre-provider progress (`turn.submitted` projection and runner-owned
  `turn.claimed` when the runner accepts the command), runner progress, and
  exactly one terminal turn outcome.
- Model and effort are sealed within a turn but re-pinnable between turns: a
  user may change the session's model/effort mid-session (the durable
  `model`/`effort` columns, set via `PUT /api/sessions/{id}/run-config`). The
  change applies to the next turn, never mid-turn — the runner re-pins at an
  idle turn boundary by tearing down the current provider query/thread and
  rebuilding it with provider-session resume + the new options, preserving the
  conversation. Antigravity is excluded (its model is an `agy` process-start
  argument). The runner must not silently ignore a changed model/effort. The
  per-turn run config is durable: the resolved model/effort is recorded on each
  turn's submission events so the transcript shows which model answered each
  turn, independent of the session's current (next-turn) selection.
- Stop/interrupt remains pending until a durable interrupted, completed,
  failed, or already-terminal event resolves it.
- Stop/interrupt against a turn that is already terminal must not create a new
  `turn.interrupt_requested` row or move activity back to `stopping`; the
  existing durable terminal is the resolution and activity is refreshed from
  the ledger.
- Tool approval replies are routed to the intended provider item and resolved
  durably.
- Claude parent agents and Claude subagents share the same session-pod
  authority boundary. Local tool authority is blanket pod authority, and MCP
  authority is generated at server granularity from the mounted `.mcp.json`
  (`mcp__server`), not maintained as per-tool rules. A configured MCP server
  that the parent can use must not be denied solely because the call originates
  inside a subagent.
- Claude AskUserQuestion is a Tank-owned SDK MCP tool
  (`mcp__tank__AskUserQuestion`) that parks a durable `turn.awaiting_input`.
  It must not depend on Claude's permission callback path; permission mode and
  human-question handoff are separate runner concerns.
- Runner events must wake transcript and session-list followers after
  persistence.
- A runner must not require an open browser to continue work.
- Provider self-scheduled wakeup state is durable in Postgres. Claude
  `ScheduleWakeup` and Antigravity `schedule` tool calls are registered by the
  runner with the backend; the orchestrator later submits the wakeup through
  the same backend-owned turn boundary as a user turn.
  Registration, cancellation, fire, and failure also write
  `scheduled_wakeup.updated` into `session_events`. `/timeline` includes a
  one-shot `scheduled_background_tasks` bootstrap and the session event stream
  delivers later projected rows, so Background -> Scheduled is event-driven and
  users can confirm due, firing, fired, and failed state without inspecting
  logs or waiting for browser polling.
- A Claude background task (`run_in_background`) that reaches a natural terminal
  while the session has no active turn wakes the session through the same
  backend-owned turn boundary as a user turn (`source=background-task`). The
  runner registers the terminal; the orchestrator owns the fire decision —
  deferring while the session is awaiting an AskUserQuestion answer (so the wake
  never clobbers a pending question) and failing the wake durably if the session
  is no longer Active. The wake is idempotent per task id (durable
  `session_background_task_wakes`), so an SDK frame repeat or a runner restart
  cannot double-wake. This closes the silent stranding where a runner
  backgrounds a task and ends the turn but the task's completion never re-invokes
  the agent.
- Token usage is durable. Each turn emits cumulative usage on its terminal
  (for cost) and, where the provider exposes per-call usage, a `turn.usage`
  snapshot per model call (for backend accounting and diagnostics). Claude reports
  usage only on the cumulative terminal — whose `input_tokens` is the uncached
  sliver under always-on prompt caching — so the Claude runner synthesizes a
  `turn.usage` snapshot from each assistant message's own usage, tagged
  `usage_source = "claude.message"`, mirroring the Codex
  `thread.tokenUsage.updated` stream. The terminal carries `claude.result`.
  Transcript and Turns projections do not render these usage events.

## Failure And Recovery

- Browser disconnect and orchestrator rollout must not cancel runner work while
  the session pod and runner remain alive.
- Runner-process restart may lose in-process state that is explicitly outside
  the durability boundary, such as current provider call state. Scheduled
  wakeups are not in-process state and must remain visible from durable
  `scheduled_wakeup.updated` events and the backend scheduled-wakeup table after
  a runner restart.
- Command redelivery must be idempotent through command keys, turn IDs, or
  provider item IDs.
- Provider failures must become durable failure events instead of silent
  strandings.
- A mid-session model/effort re-pin that fails provider resume (for example a
  cross-model thinking-block rejection) must resolve the turn with a durable
  `turn.failed`, never a silent stranding; the failure class stays visible in
  `tank_runner_provider_failure_signature_total` so a cross-model resume
  regression is observable.
- Provider rate-limit stream frames must be classified by the provider's
  primary quota status before they affect durable turn state. A primary
  rejected/exhausted quota resolves the active turn with
  `turn.failed{reason:"provider_rate_limit"}` so the command queue does not
  strand. A primary allowed quota is an informational capacity observation,
  even when overage/extra-usage fields are rejected, and must not create a
  durable turn terminal.

## Observability

- Metrics must distinguish command published, consumed, acked, redelivered,
  failed, and terminal-event-emitted outcomes.
- Metrics must expose command-created to claimed and claimed to started
  latency, so provider stalls and runner queueing are visible before the
  terminal event arrives.
- Runner metrics and backend metrics must be comparable so a bug can be
  localized between command delivery, runner execution, persistence, and live
  client delivery.
- Silent strandings, where a requested action has no terminal event, are a
  counted bug class.
- Provider rate-limit frame handling is counted by decision, so false terminal
  failures and unhandled terminal frames are visible separately from ordinary
  rejected primary quota.
- A turn that emits assistant messages but no usage snapshot is a backend
  accounting/diagnostic regression signature; `tank_runner_turn_usage_emitted_total{kind}`
  counts `snapshot` vs `terminal` usage emissions so the gap is visible.
- AskUserQuestion answer delivery is counted at the runner/provider boundary:
  `tank_runner_input_reply_answer_shape_total{shape}` distinguishes selected
  labels, free-form-only notes, and selected-labels-plus-notes so a durable
  `turn.input_answered` row with annotations can be compared against what the
  runner actually normalized for provider delivery.
- Claude tool permission denials are counted at the runner/provider boundary:
  `tank_runner_tool_permission_denied_total{agent_kind,tool_family,server,decision}`
  distinguishes parent versus subagent origin, MCP versus local tools, the MCP
  server name when applicable, and the bounded SDK decision class. A subagent
  denial for a configured MCP server is a runner authority regression, not a
  user-debuggable provider detail.
- Background-task wakes are counted on both sides so a regression localizes
  between detection, registration, and firing: the runner's
  `tank_runner_background_task_wake_total{result}` and the orchestrator's
  `tank_background_task_wake_register_total` / `tank_background_task_wake_fire_total`
  (plus the `tank_background_task_wakes_due` gauge). A task-lifecycle terminal
  that the runner logs as `sdk_task_lifecycle_unbound` while wake registrations
  stay flat is the detection-regressed signature.

## Acceptance Checks

- A normal turn reaches exactly one durable terminal event.
- Stop/interrupt produces one durable resolution and clears visible pending
  state from that durable resolution.
- Command redelivery does not duplicate user-visible transcript entries.
- Provider output is converted to Tank protocol before browser rendering
  depends on it.
- Runner work continues across browser disconnect and remains reconstructable
  from durable events.
