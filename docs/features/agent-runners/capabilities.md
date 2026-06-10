# Agent Runners Capabilities

This ledger names user-facing behavior under the claude-runners feature area. It
is not a backlog. Add entries only when the behavior needs a stable handle for
planning, review, tests, incident follow-up, or retirement.

## Antigravity no-answer provider failure terminal

Status: in progress

Intent:
When Antigravity (`agy`) exits with code 0 after producing tool activity but no
assistant prose, the turn must resolve as a durable failure instead of a
successful empty completion. Originating incident: session 711 on 2026-06-08
ran 61 tool steps, logged `agent executor error: UNKNOWN (code 500)` /
`PlannerResponse without ModifiedResponse`, and wrote `turn.completed` with no
final answer, making the page look stalled even though the durable terminal was
`completed`.

Affected contracts:
- Agent Runners

Contract impact:
- Converts a provider failure that previously masqueraded as success into
  exactly one durable `turn.failed`, satisfying "Provider failures must become
  durable failure events instead of silent strandings."
- A normal successful Antigravity turn requires assistant prose that can be
  promoted as `final_answer`; tool activity alone is not a successful user
  answer. The explicit exception is native schedule parking, where the runner
  interrupts agy's native timer only after durable wakeup registration.
- The Antigravity adapter mirrors the SDK's completed-response boundary where
  possible from agy's JSONL: only a `MODEL` `PLANNER_RESPONSE` that is `DONE`
  and has non-empty text can become assistant prose. `IN_PROGRESS` records may
  start the turn but cannot open tools, close tools, or consume a step index
  before the later settled transition.
- The driver is event-driven: it watches agy's data directory and drains
  transcript records on filesystem/output/process-exit notifications, then
  performs one final reconciliation drain after exit. It does not run a fixed
  transcript polling loop. The transcript tailer owns per-file byte cursors and
  partial-line buffers, so long cumulative Antigravity transcripts are not
  reread from the beginning on every file event.
- Transcript event-source health is explicit. Watcher startup failure or a
  watcher error becomes a bounded `turn.failed` reason
  (`transcript_event_source_unavailable` / `transcript_event_source_error`) and
  increments `tank_antigravity_runner_transcript_event_source_total{result}`;
  live-update degradation is not silent.
- Provider executor stderr such as `UNKNOWN (code 500)` and
  `PlannerResponse without ModifiedResponse` is counted separately as
  `provider_executor_error`; normal-looking no-answer exits are counted as
  `provider_no_final_answer`.

Evidence (Go runner; the TS runner this entry originally cited was replaced
by the Go spike in #994 and its test files no longer exist):
- Runner: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestTurnRunFailsWhenProviderProducesNoFinalAnswer` pins the no-answer
  terminal; `TestTurnRunClassifiesExecutorErrorOnNoFinalAnswer` pins the
  `provider_executor_error` vs `provider_no_final_answer` classification).
- Metrics/docs: `tank_antigravity_runner_provider_error_total{reason}` with
  `provider_executor_error` and `provider_no_final_answer` implemented and
  documented in `docs/observability.md`.
- Outstanding from the original TS-era scope: transcript event-source health
  (`tank_antigravity_runner_transcript_event_source_total`,
  `transcript_event_source_*` failure reasons) and the
  `agy_diagnostic`/`schedule_intent` counters are documented but not yet
  implemented in the Go runner; this entry stays in progress until they are.

## Antigravity process death is session-terminal

Status: in progress

Intent:
When the `agy` process exits (crash, OOM, or a Stop whose SIGINT proved
fatal), the session is done by explicit product decision (2026-06-10): no
in-place respawn, no container-restart revival — both resume the chat with an
agent that silently lost the conversation. The runner instead resolves
everything durably and the session row moves to `Failed` exactly like pod
death. `tank_antigravity_runner_process_exit_total` /
`tank_session_provider_fatal_total` measure how often this happens; revival
gets designed deliberately if that rate ever matters.

Affected contracts:
- Agent Runners

Contract impact:
- An in-flight turn resolves with exactly one durable terminal when agy
  exits: `turn.interrupted` if a Stop was in flight, else
  `turn.failed{reason:"provider_process_exited"}` — "provider failures must
  become durable failure events instead of silent strandings."
- After death the runner goes inert instead of exiting: queued and new
  `submit_turn` commands drain to durable
  `turn.failed{reason:"provider_process_unavailable"}` with normal acks, so
  the command queue cannot strand. The runner must not exit — kubelet's
  restart policy would relaunch agy with amnesia (forbidden revival).
- The runner reports `POST /api/internal/sessions/{id}/provider-fatal`
  (projected SA token, self-session only); the orchestrator applies the
  `session.provider_fatal` RowWriter transition → row status `Failed`, same
  downstream behavior as `session.pod_failed`.
- Two liveness bounds share the same turn-resolution select: the submit-ack
  watchdog (`ANTIGRAVITY_SUBMIT_ACK_TIMEOUT_MS`, default 60s) fails a turn
  whose PTY prompt write produced no transcript movement at all
  (`prompt_not_accepted`, no auto-retry — a re-written prompt can
  double-execute), and the interrupt grace window
  (`ANTIGRAVITY_INTERRUPT_GRACE_MS`, default 10s) forces the durable
  `turn.interrupted` when a Stop is neither settled nor fatal. Interrupts
  with no active turn are ignored rather than SIGINTing idle agy.

Evidence:
- Runner: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestHandleSubmitTurnFailsDurablyWhenAgyExits`,
  `TestHandleSubmitTurnDrainsCommandsAfterAgyExit`,
  `TestHandleSubmitTurnWatchdogFailsSwallowedPrompt`,
  `TestHandleSubmitTurnInterruptGraceForcesDurableStop`,
  `TestInterruptWithoutActiveTurnDoesNotSignalIdleAgy`).
- Backend: `backend-go/internal/sessioncontroller/provider_fatal_test.go`
  (`session.provider_fatal` derives row status `Failed`).
- Metrics: `tank_antigravity_runner_process_exit_total{phase}`,
  `tank_antigravity_runner_interrupt_outcome_total{outcome}`,
  `tank_antigravity_runner_submit_watchdog_total{result}`,
  `tank_antigravity_runner_provider_fatal_report_total{result}`,
  `tank_session_provider_fatal_total{provider,result}` — taxonomy in
  `docs/observability.md`.
- Design record: `backend-go/cmd/antigravity-runner/ARCHITECTURE.md` →
  "Process Death Is Session-Terminal (No Revival)".

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
- Runner: `claude-runner/src/runner.test.ts`
  (register-once-when-idle, skip-when-active, ignore user-stop/lifecycle-start);
  `claude-runner/src/adapters/claude.test.ts` (natural-vs-user terminal split).
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
- Runner: `claude-runner/src/runner.test.ts` (rate_limit stall → one durable
  terminal + ack + released turn; progress resets the window; `overloaded`
  observed-not-failed); `classifyApiRetryError` unit behavior.
- Metrics: `tank_runner_provider_api_retry_total{error}` and
  `tank_runner_provider_rate_limit_decision_total{decision="retry_stall_failed"}`.
- Docs: `docs/observability.md` runner-metrics taxonomy.
