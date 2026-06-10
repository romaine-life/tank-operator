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
  answer. When agy starts background work (a `schedule` timer or a `run_command`
  build) it narrates alongside it ("I will wait and report back"), so a
  background-work turn still has prose to promote and its terminal carries
  `background_work_pending=true` — see the self-continuation relay capability below.
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

Evidence:
- Runner: `antigravity-runner/src/runner.test.ts` (executor 500 after tool
  output fails, tool-only no-final-answer fails, schedule parking may complete
  without final prose).
- Adapter: `antigravity-runner/src/adapters/antigravity.test.ts` (final-answer
  state requires done non-empty assistant prose; in-progress tool calls and
  tool results do not consume the later done transition).
- Driver: `antigravity-runner/src/driver.test.ts` (a transcript write is
  surfaced before the fake agy process exits, proving live updates are driven
  by events rather than process-exit reconciliation).
- Tailer: `antigravity-runner/src/transcriptTailer.test.ts` (pre-existing
  transcript bytes are skipped, new appended bytes are emitted, and partial
  appended JSONL records are buffered until complete).
- Metrics/docs: `tank_antigravity_runner_provider_error_total{reason}` with
  `provider_executor_error`, `provider_no_final_answer`, and transcript
  event-source failure reasons documented in `docs/observability.md` and the
  Antigravity provider-error alert runbook.

## Antigravity self-continuation relay (timer + background work)

Status: in progress

Intent:
Antigravity (`agy`) is the first long-running, self-managing agent in the
codebase: one persistent process that schedules its own work (`schedule` timers,
`run_command` builds/shells), runs it in the background, and **continues itself**
when that work finishes — no Tank clock involved. The runner keeps agy alive,
OBSERVES its self-continuation, and RELAYS it through a backend-authored turn
boundary. One user request becomes one user-facing turn that spans agy's
background work: non-summoning through the wait, summoning at the report.
Originating saga: ~20 PRs across 2026-06 tried to make "timer waking" work by
having Tank own and fire agy's clock (the puppeteer model). Session 781 was the
last failure of the Go rewrite (#996), which had dropped wakeup registration; an
earlier `firstEnv` token-path bug then made a "registered" log line lie with no
durable row. The whole inject approach was the wrong shape — it double-wakes an
agent that already wakes itself — and is replaced by the observe-and-relay model.

Affected contracts:
- Agent Runners

Contract impact:
- **Tank never owns a clock for agy.** `supportsScheduledWakeups` and the
  background-task-wake register endpoint REJECT antigravity (the single
  `providerSelfContinues` realm-split predicate); the runner registers no
  `session_scheduled_wakeups` / `session_background_task_wakes` row. Those tables
  and their fire loops are the Claude/Codex model, where the agent genuinely
  cannot self-continue.
- **The runner observes + relays.** agy's idle self-continuation (a `MODEL` step
  with no active turn, after one of its tracked tasks fired) POSTs
  `/api/internal/sessions/{id}/agent-continuation`. The backend — the sole
  producer of `turn.submitted` — authors the boundary (`source=agent-continuation`,
  `OmitUserMessage`) reusing the `turn_bgtask-<task>` client nonce so the relay
  turn folds into the originating user-facing turn. `handleSubmitTurn` skips the
  PTY write for `agent-continuation` and replays agy's already-emitted steps.
- **User-facing-turn projection (the #906 spine).** The runner stamps
  `turn.completed.background_work_pending` from the pending-set; the activity fold
  folds a would-be-`ready` terminal to the non-summoning `scheduled` status when
  it is set. `applyScheduledWakeOverride` unifies the two pending sources — the
  Tank wake tables (Claude/Codex) OR `background_work_pending` (antigravity) — so a
  parked agy turn reads as `scheduled` with no Tank wake row. `working → scheduled`
  does not ring; `working → ready` (nothing pending) rings.
- **The pending-set is the load-bearing signal.** agy routes all background work
  through one uniform task framework: a `MODEL` `status=RUNNING`
  "running as a background task with task id: X" marker adds X; a SYSTEM_MESSAGE
  `sender=X` completion removes X. The RUNNING marker always lands before the SDK
  `turn.completed`, so the runner knows at the terminal whether work is pending —
  no race.
- **No untracked self-wake.** agy continues only after a tracked task completion;
  a self-continuation with no preceding completion is logged loudly (the
  forbidden-self-wake signature) rather than silently resurrecting a closed turn.
- **Idempotent + resurrection-safe relay.** The relay turn id is deterministic
  per task; the endpoint re-enqueues only while no terminal exists
  (`FindTurnTerminal`), and JetStream `WithMsgID` dedups the deterministic command
  so a retry never double-delivers. `agent-continuation` is a self-resume source,
  so a transient publish-fail writes no `command_failed` terminal and the runner's
  retry recovers.

Evidence:
- Runner: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestRunnerSelfContinuationContract` AST-asserts the runner owns no Tank wake
  and POSTs only `/agent-continuation`; `TestNoteTaskSignalTracksPendingSet`;
  `TestTurnCompletedStampsBackgroundWorkPending`).
- Orchestrator reject/relay: `backend-go/cmd/tank-operator/scheduled_wakeups_test.go`
  (`TestSupportsScheduledWakeupsRejectsAntigravity`); `background_task_wakes_test.go`
  (`TestProviderSelfContinues`, `sdkTurnSource` includes `agent-continuation`);
  `handleInternalAgentContinuation` + the antigravity reject in
  `handleInternalRegisterBackgroundTaskWake` (`background_task_wakes.go`).
- Projection: `backend-go/internal/sessioncontroller/chat_activity_test.go`
  (`TestApplyScheduledWakeOverride` background_work_pending cases park even with a
  nil wake checker); the fold in `backend-go/internal/sessionactivity/activity.go`
  (`ActivityFoldStats.BackgroundWorkPending`).
- Guards: `scripts/check-removed-chat-runtime.mjs` (runner self-continuation
  contract check — no `registerScheduledWakeup` / wake-endpoint literals in the
  runner main.go, `/agent-continuation` present); the AST test above.
- Metrics: `tank_agent_continuation_total{provider,result}`;
  `tank_background_task_wake_register_total{result="rejected_antigravity"}`.
- Contract: `backend-go/cmd/antigravity-runner/ARCHITECTURE.md`
  ("The long-running-agent harness contract"); the `scheduled` status spine in
  `docs/scheduled-turn-continuity.md`.

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
