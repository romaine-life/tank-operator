# Agent Runners Capabilities

This ledger names user-facing behavior under the agent-runners feature area. It
is not a backlog. Add entries only when the behavior needs a stable handle for
planning, review, tests, incident follow-up, or retirement.

## Background-task completion wake

Status: in progress

Intent:
When a Claude session backgrounds a task (`run_in_background`) and then ends its
turn, the task finishing later must re-invoke the agent — the base Bash tool's
"re-invokes you when it exits" promise. Before this, a task-lifecycle SDK frame
never started a turn, so a task that finished while the session was idle left the
follow-up silently stranded (the originating incident: a session that backgrounded
a "Wait for CI" task, ended its turn, and never woke).

Affected contracts:
- Agent Runners

Contract impact:
- Wakes go through the same backend-owned turn boundary as a user turn
  (`source=background-task`); the runner never fabricates a turn.
- Idempotent per task id via the durable `session_background_task_wakes` row
  (`wake_id = sha256(tank_session_id, provider, task_id)`), so SDK frame repeats
  and runner restarts cannot double-wake — "command redelivery must be idempotent
  through command keys, turn IDs, or provider item IDs."
- Must not clobber an in-flight question: the fire loop defers (release + retry)
  while the session's durable activity is `needs_input` (an AskUserQuestion
  awaiting an answer).
- Closes a "silent stranding" — a counted bug class — rather than adding one.

Evidence:
- Backend: `backend-go/cmd/tank-operator/background_task_wakes_test.go`
  (durable turn boundary + `source=background-task`, defer-on-awaiting-input,
  fail-on-inactive, `sdkTurnSource`, turn-id-safe nonce);
  `backend-go/internal/pgstore/background_task_wakes.go` (idempotent `Register`).
- Runner: `agent-runner/src/runner.test.ts`
  (register-once-when-idle, skip-when-active, ignore user-stop/lifecycle-start);
  `agent-runner/src/adapters/claude.test.ts` (natural-vs-user terminal split).
- Metrics: `tank_runner_background_task_wake_total{result}`,
  `tank_background_task_wake_register_total`,
  `tank_background_task_wake_fire_total`, `tank_background_task_wakes_due`.
- Durable schema: migrations 0121–0124 (`session_background_task_wakes`).

## Provider rate-limit retry-stall terminal

Status: in progress

Intent:
When the Claude SDK's internal HTTP retry loop keeps getting `rate_limit` (429)
from the provider and never converges, it emits only `system/api_retry` /
`status` / `thinking_tokens` frames and never surfaces a terminal
`rate_limit_event`. Before this, those frames fell through to
`logUnhandledSdkMessage`, so the turn sat `claimed` with no `turn.started`, no
output, and no terminal — the user saw dead air indefinitely. Originating
incident: session 638 ("abmience runs") on 2026-06-06 sat wedged 35+ minutes
across three turns while sibling sessions on the same shared account streamed
normally. The runner now classifies the retry storm and, after a bounded
no-progress window, resolves the in-flight turn with the same durable
`turn.failed{reason:"provider_rate_limit"}` terminal a rejected quota would.

Affected contracts:
- Agent Runners

Contract impact:
- Converts a silent stranding — a counted bug class — into exactly one durable
  terminal so the command queue drains ("Provider failures must become durable
  failure events instead of silent strandings").
- Distinct from the terminal `rate_limit_event` path: the new
  `decision="retry_stall_failed"` keeps "the SDK never surfaced a terminal
  frame" separable from an ordinary rejected primary quota
  ("rate-limit frame handling is counted by decision").
- Bounded by `SESSION_PROVIDER_RETRY_STALL_MS` (default 240s); real provider
  progress (`turn.started` or a mapped canonical event) resets the window, while
  `status`/`thinking_tokens` heartbeats do not (they are part of the stuck
  cycle), so a slow-but-progressing turn is never falsely failed.
- Non-`rate_limit` retries (`overloaded`/`api_error`) are observed but never
  forced to a terminal — the SDK recovers from those on its own.
- Runner-scoped: it catches the case where api_retry frames keep arriving. The
  case where the SDK (or runner) goes fully silent and cannot emit its own
  terminal is the orchestrator-side stall detector (session-lifecycle, Stage 2).

Evidence:
- Runner: `agent-runner/src/runner.test.ts` (rate_limit stall → one durable
  terminal + ack + released turn; progress resets the window; `overloaded`
  observed-not-failed); `classifyApiRetryError` unit behavior.
- Metrics: `tank_runner_provider_api_retry_total{error}` and
  `tank_runner_provider_rate_limit_decision_total{decision="retry_stall_failed"}`.
- Docs: `docs/observability.md` runner-metrics taxonomy.
