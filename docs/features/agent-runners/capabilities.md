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
- **The fold edge is durable (tank-operator#1035).** Reusing the
  `turn_bgtask-<task>` id shape is necessary but not sufficient to fold: the
  transcript projection derives the relay → originating-turn parent edge from
  durable `shell_task.*` lifecycle events, and only recognized
  `turn.submitted` sources mark continuation turns. The runner therefore
  publishes `shell_task.started` at agy's RUNNING marker (carrying the
  originating turn id; `task_id` is the stableIDPart form so it round-trips
  through the relay turn id) and `shell_task.exited` at the `sender=`
  completion, and `isBackgroundTaskWakeTurnEvent` recognizes
  `source=agent-continuation` alongside `background-task`. The same events
  make the pending task visible at rest in the Background-activity screen
  (it renders the `shell_task.*` ledger set). Session 790's two symptoms —
  "nothing in background tasks" and a standalone turn for the woke-up
  report — were both this missing edge. A signal whose originating turn is
  unknowable publishes nothing and counts
  `tank_antigravity_runner_task_lifecycle_total{event="orphaned_start"|"orphaned_completion"}`
  — the fold-regression signal.
- **User-facing-turn projection (the #906 spine).** The runner stamps
  `turn.completed.background_work_pending` from the pending-set; the activity fold
  folds a would-be-`ready` terminal to the non-summoning `scheduled` status when
  it is set. `applyScheduledWakeOverride` unifies the two pending sources — the
  Tank wake tables (Claude/Codex) OR `background_work_pending` (antigravity) — so a
  parked agy turn reads as `scheduled` with no Tank wake row. `working → scheduled`
  does not ring; `working → ready` (nothing pending) rings.
- **Silence is the turn boundary (turn-settle window).** agy writes no
  end-of-burst marker anywhere (verified empirically across transcripts,
  messages/, task logs, cli.log — tank-operator#1035); its planner loop runs
  multiple rounds per burst. The runner publishes `turn.completed` only after
  a settled prose response plus `ANTIGRAVITY_TURN_SETTLE_MS` (default 2s) of
  transcript silence, so the answer-first sequence stays inside one turn with
  a correct `background_work_pending` stamp — no false `ready`/ring, no
  untracked self-wake relay. Only a genuinely NEW step may move the boundary:
  a dedupe-suppressed replayed step (rewrite-rewind) is invisible to the
  window, because a replay can only cancel — the re-arm lives behind the
  dedupe gate — and a cancel with no possible re-arm pins the turn forever
  (sessions 828/829, 2026-06-12: final answer published, settle armed, the
  rewrite that appended it replayed the whole history inside the 2s window,
  no `turn.completed` ever, data-plane consumer blocked at
  `max_ack_pending=1`). The window's failure modes are asymmetric by design:
  an early close degrades to the relay+fold machinery (cosmetic), a
  never-close violates "exactly one durable terminal per turn" and is
  structurally excluded by replay invisibility.
  `tank_antigravity_runner_turn_settle_total{outcome}` counts quiet vs
  extended boundaries.
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
- Turn settle: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestTurnSettleKeepsAnswerFirstBurstInOneTurn` replays slot-1 round 1's
  exact answer-first burst into one terminal;
  `TestTurnSettleQuietCompletesAfterWindow`,
  `TestTurnSettleZeroCompletesImmediately`;
  `TestTurnSettleSurvivesTranscriptRewriteReplay` pins the 828/829 wedge —
  the window armed by the final prose fires through a full-history replay;
  `TestReplayedTaskSignalsKeepPendingSetSettled` pins the companion guard —
  a replayed RUNNING marker cannot resurrect a completed task into
  `background_work_pending` at the terminal).
- Fold edge: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestTaskLifecycleEventsPublishDurableFoldEdge`,
  `TestTaskLifecycleStartWhileIdlePublishesNothing`);
  `backend-go/cmd/tank-operator/transcript_projection_test.go`
  (`TestProjectTranscriptEventsFoldsAgentContinuationTurnIntoOriginatingTurn`
  — session 790's exact event shape folds and never surfaces standalone).
- Metrics: `tank_agent_continuation_total{provider,result}`;
  `tank_background_task_wake_register_total{result="rejected_antigravity"}`;
  `tank_antigravity_runner_task_lifecycle_total{event}`.
- Contract: `backend-go/cmd/antigravity-runner/ARCHITECTURE.md`
  ("The long-running-agent harness contract"); the `scheduled` status spine in
  `docs/scheduled-turn-continuity.md`.

## Antigravity transcript-rewrite replay suppression (turn re-attribution)

Status: in progress

Intent:
agy performs its larger writes to `transcript_full.jsonl` as an in-place
truncate + byte-identical full rewrite (same inode, prefix cksum-stable —
verified live on probe session 799, agy CLI 1.0.6). When a tailer sweep's
stat lands inside that sub-second window, `size < offset` rewinds the byte
cursor and the entire history re-arrives — an intermittent race correlated
with large step outputs (zero on light sessions; routine on real workloads:
sessions 791/792/793 all hit it repeatedly). A transcript step must publish
durable events exactly once, under the turn that first observed it.
Originating incident: session 791 (2026-06-11, "chess shadow") — step dedupe
was scoped to the live turn, so every replay re-published all prior history
under whatever turn was live. Turn 1's "schedule 5s timer" items existed
under four turn_ids, per-turn item counts grew cumulatively (2 → 35 → 270 →
282; O(N²) ledger growth), and expanding turn N in the Turns view showed the
content of turns 1..N. Claude/Codex runners structurally lack this class:
they consume push streams over stdio (SDK stream-json / app-server JSON-RPC),
exactly-once by construction; file-scraping is at-least-once by nature and
needs this idempotency layer to discharge into the exactly-once ledger.

Affected contracts:
- Agent Runners
- Transcript

Contract impact:
- Step dedupe is session-scoped (`runnerState.seenSteps`, keyed by
  `providerStepID` + status) instead of per-`turnRun`. This is the
  transcript-replay analog of "command redelivery does not duplicate
  user-visible transcript entries", and the sibling of the task-lifecycle
  dedupe (`taskEventsPublished`) that #1035 added for repeating task markers.
- The idle path is guarded too: a replayed idle MODEL step is not re-buffered
  into the next attaching turn and does not manufacture a phantom
  agent-continuation relay — only a genuinely new idle MODEL step is agy
  self-continuing (the no-untracked-resumption rule).
- Replayed bytes prove aliveness only: the submit-ack watchdog clears, and
  nothing else moves. The durable publish is suppressed, the settle window is
  untouched (a replay can only cancel, never re-arm — cancel-on-replay is the
  828/829 forever-pinned-turn wedge), and the background-task pending-set
  ignores replayed signals (`runnerState.completedTasks`: task ids are never
  reused, so "completed once" is terminal — a replayed RUNNING marker must not
  transiently resurrect a dead task into a terminal's
  `background_work_pending` stamp, and a replayed completion must not reset
  the consumed relay attribution).
- Scoped to process memory deliberately: agy process death is
  session-terminal (no revival), so no replay must survive a restart that the
  tailer's startup byte-cursor skip does not already cover.
- Pre-fix sessions' duplicated `session_events` rows were scrubbed on
  2026-06-11 (user-approved: historic sessions are ephemeral/killable):
  content-identical later-turn copies of item.* events deleted, transcript
  rows re-materialized via per-session backfill-row reset — no code path
  keeps old data alive (migration policy).

Evidence:
- Runner: `backend-go/cmd/antigravity-runner/main_test.go`
  (`TestCumulativeTranscriptReplayDoesNotReattributeStepsAcrossTurns` replays
  session 791's shape — full-history re-arrival under a second turn publishes
  nothing and the second turn's final answer stays its own;
  `TestIdleCumulativeReplayDoesNotBufferOrManufactureContinuation` pins the
  idle buffer + phantom-relay guard).
- Ledger diagnosis: session 791 `session_events` — same agy step ids under up
  to 4 turn_ids, 0 of 501 step/status pairs with divergent content across
  copies (the re-publications were byte-identical replays).
- Writer-behavior verification (probe sessions 798/799, 2026-06-11): 0.3s
  size/inode watcher + content snapshots through light and heavy turns —
  monotone sizes, constant inode, cksum-identical prefixes — while the
  durable ledger recorded a full mid-turn re-publication on the heavy turn,
  proving the sub-second in-place truncate+rewrite race (the runner's
  fsnotify-paced stats catch windows wall-clock samplers cannot).
- Metrics: `tank_antigravity_runner_step_replay_suppressed_total{context}` —
  taxonomy in `docs/observability.md`.
- Design record: `backend-go/cmd/antigravity-runner/ARCHITECTURE.md` →
  "The Transcript Writer Rewrites In Place (session-scoped step dedupe)".

## Background-task completion wake

Status: in progress

Intent:
When a session backgrounds provider work and then ends its turn, the work
finishing later must re-invoke the agent. Two provider shapes are covered:
Claude's `run_in_background` Bash tasks (the original), and Codex's
unified-exec background shells (added after the antigravity fold work proved
the rails provider-generic — tank-operator#1035 follow-up). Codex parity has
three legs: the app-server transport surfaces idle item notifications instead
of dropping them at the active-queue guard, the adapter remembers each
background shell's originating turn (`runningBackgroundTasks`) so the idle
`shell_task.exited` carries the fold edge and the turn terminal stamps
`background_work_pending` (park, non-summoning, same #906 fold the wake
tables drive for Claude), and the runner registers the durable wake via the
shared `runner-shared/backgroundTaskWake.js` helper with Claude's exact
skip-when-active + idempotent-register semantics. Codex's app-server emits
NO notification when a background command finishes (verified against the
binary's RPC surface — `backgroundTerminals` has only `/clean`), so the
completion source is the OS: the provider declares each shell's PID in its
own item payload, the shell is a descendant of the app-server in the same
container PID namespace, and the runner probes liveness (`kill(pid,0)`)
every 5s ONLY while shells are pending — zero cost when idle, watcher
self-stops. The synthesized exited claims no output; the wake-turn's model
retrieves results natively from its own unified session.

Original Claude intent:
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
- The wake row stores STRUCTURED task facts (status, description, summary,
  last tool, error) plus the durable `observed_event_id` of the
  `shell_task.exited` observation that registered it. The agent-facing prompt
  is composed AT FIRE TIME, in the provider's own tool idiom
  (`buildBackgroundTaskWakePromptForProvider`): codex is never pointed at
  BashOutput/TaskOutput. The prompt DEMANDS a user-facing report — the
  session-161 bug museum proved the frozen Claude-shaped prompt with an
  "end without taking action" escape produced zero fulfilled reports across
  every fired wake.
- Idempotent per OBSERVATION, re-armable per task: same observation
  re-registered (SDK frame repeats, runner restarts) is a durable no-op; a
  NEW observation of an already-fired task arms the next wake generation
  (`wake_id`/nonce gain a `-g<N>` suffix; the fold derives the originating
  turn from the payload task_id either way), so a premature fire no longer
  permanently burns the task's only report. Generations are capped
  (`generation_capped` is the flapping-observer alarm); `failed`/`cancelled`
  rows are never resurrected.
- `unobservable` no longer resolves to user-facing silence: the runner
  registers the wake with status `unknown`, and the prompt states that
  observability was lost and demands the agent verify the real state and
  report. A later real observation re-arms the next generation with the
  truth.
- Delivered-mid-turn dedupe: when a runner observes a task's completion
  delivered INTO an active turn, it cancels the task's pending wake
  (`POST …/background-task-wakes/cancel`), and the fire loop soft-defers
  while the session's durable activity says a turn is in flight
  (`deferred_active_turn`) — one completion must never arrive as both a
  mid-turn notification and a later wake turn.
- Must not clobber an in-flight question: the fire loop defers (release + retry)
  while the session's durable activity is `needs_input` (an AskUserQuestion
  awaiting an answer).
- A transiently non-Active session defers, a dead one rings (#1079): the K8s
  watch flips the durable session row Active → Pending on ANY probe blip, so
  the fire loop releases the claim (`deferred_session_not_active`) and retries
  instead of terminal-failing the promised report; the release retains the
  attempt bump so the fire-attempt cap bounds a never-recovering session and
  `FailExceeded` then terminals it ringing. A durably dead session (row
  missing, `Failed`, terminating) fails fast AND emits the away-tagged
  `turn.command_failed` ring carrier — every durable wake failure rings, none
  resolves silently. Same ladder on the scheduled-wakeup loop. See
  `docs/scheduled-turn-continuity.md` → "Failure model".
- Closes a "silent stranding" — a counted bug class — rather than adding one.
- Codex corrective observations survive force-exits: the adapter tombstones
  recently-exited shells so a late idle item notification for a task the
  pid-watcher already exited still publishes a corrective `shell_task.exited`
  on the originating turn (the observation that re-arms the wake).
- Runner restart no longer orphans tracked tasks. Tracked tasks are process
  memory; on boot each runner reads its own durable open lifecycles
  (`GET /api/internal/sessions/{id}/background-tasks/unresolved` —
  shell_task.started with no exited) and re-adopts, provider-correctly:
  codex re-seeds its pid watcher (the shells are real OS processes it can
  still observe; one that finished during the restart gap resolves through
  the observed-alive-first guard as an honest unknown wake); claude CLOSES
  the orphans honestly — its SDK task registry is severed by the restart, so
  it publishes a corrective `shell_task.exited{status: unknown,
  completion_source: runner_restart}` on the originating turn with a
  deterministic event id (repeated restarts dedupe instead of stacking wake
  generations) and registers the unknown-status wake demanding the agent
  verify and report. Antigravity needs no re-adoption: agy process death is
  session-terminal by design (#1034).

Evidence:
- Backend: `backend-go/cmd/tank-operator/background_task_wakes_test.go`
  (durable turn boundary + `source=background-task`, fire-time provider-aware
  prompt incl. demand-report / codex-idiom / unknown-status / generation-note
  assertions, defer-on-awaiting-input, defer-on-active-turn,
  defer-on-pending-session with the attempt retained
  (`TestFireBackgroundTaskWakeDefersWhileSessionPending`), dead-session
  fail-fast + away-error ring
  (`TestFireBackgroundTaskWakeFailsDeadSessionDurablyAndRings`),
  `sdkTurnSource`, turn-id-safe nonce);
  `backend-go/internal/pgstore/background_task_wakes_integration_test.go`
  (`TestPostgresBackgroundTaskWakeGenerationsRearmAfterPrematureFire`: re-arm
  on new observation, duplicate no-op, generation cap, cancel-for-task,
  no resurrection after cancel;
  `TestPostgresBackgroundTaskWakeReleaseRetainingAttemptIsCapBounded`: the
  transient defer burns attempts and terminals through `FailExceeded`).
- Codex runner: `codex-runner/src/adapters/codex.test.ts` ("turn terminal
  stamps background_work_pending while a unified-exec shell runs" — in-turn
  start, pending stamp, idle exited attributed to the originating turn,
  post-drain unstamp; untracked idle items publish nothing).
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

## Stranded-turn sweep (command-plane four-outcome backstop)

Status: shipped (2026-06-11, tank-operator#1051 PR 4); AskUserQuestion
pause exclusion + pipeline-liveness gate (2026-06-12, the sweep's
first-day false-positive incident)

Intent:
A durably submitted turn whose submit_turn command or runner dies has no
other terminal writer; before the sweep such turns sat as permanent
"submitted"/"streaming" ghosts (five sessions in the 2026-06-11 incident,
plus 53 historical strands spanning 30 days drained on first deploy). The
sweep terminals them with durable turn.command_failed once the whole session
has been silent past the stranding floors (30m never-claimed / 2h mid-turn),
never re-driving the command. Continuation strands (background-task wakes,
scheduled wakeups, agent continuations) carry the
stranded_continuation_swept away-error reason so the sidebar rings the
summon bell; ordinary user turns fail plainly with resubmit guidance.

The sweep's turn model deliberately excludes the three legitimate
terminal-less shapes AskUserQuestion creates — the synthetic question
shell (turn_question-*, submitted + awaiting_input, never claimed by
design), the asking turn paused on the user (terminal arrives only after
a human answers, possibly days later), and the answered asking turn
(the runner rotates the live turn to the answer nonce; the terminal lands
under turn_answer-*, never under the asking id). On its first day in
production the sweep wrote false turn.command_failed terminals onto all
three shapes — 164 of 302 terminals (54%), across 50+ sessions —
destroying pending questions (the /answer endpoint 409s once a terminal
exists) and ringing false summons for wake-source asking turns. The
exclusion lives in the FindStrandedTurns candidate query (pause-linkage
NOT EXISTS over turn.awaiting_input / turn.input_answered, matched by
turn_id, asking_turn_id, or question_turn_id, riding the
session_events_input_pause partial index). After an answer, the
strandable identity is the rotated continuation turn (turn_answer-*),
which carries its own turn.submitted and is correctly swept if the
input_reply command is lost or the rotated turn dies mid-work.

The sweep also gates on fleet-wide pipeline liveness
(HasRecentRunnerEvent): turn.submitted lands over HTTP directly, so a
persister/session-bus outage makes every active session look quiet while
submits accumulate — without the gate, the sweep would mass-fail healthy
in-flight turns exactly while the pipeline recovers. Candidates found
while zero runner-produced events landed in the quiet window are deferred
(result=deferred_pipeline_quiet), never written. Backend-writable event
types — including the sweep's own output — never count as proof of life.

Affected contracts:
- Agent Runners ("a durable *_requested event MUST be followed by exactly one
  durable terminal; silent strandings are a counted bug class")
- Tank conversation protocol ("AskUserQuestion pauses the same turn" — the
  pause states and the rotation are part of every consumer's turn model;
  the sweep is enumerated in the protocol's consumer-audit list)
- Observability (tank_stranded_turn_swept_total, TankStrandedTurnsSwept — the
  alert is about WHY commands die; the terminal is the recovery)

Retirement note:
Scan cadence is 15 minutes because FindStrandedTurns is a heavy 30-day window
scan; if the candidate query ever becomes incremental, the cadence can drop.
The quiet-session predicate is the false-positive guard — do not relax it to
catch strands faster. Do not "simplify" the pause exclusion into an age
floor: a question can legitimately wait longer than any floor, and a false
terminal on a question turn is unrecoverable without ledger surgery
(tank-operator PR B remediation, 2026-06-12).


## Supervised session bus (connection immortality + consumer self-healing)

Status: shipped (2026-06-12, issue #1076 item 1)

Intent:
The session pod outliving a NATS disruption is the durability boundary the
conversation protocol promises, so the runner's bus must never give up
while the process lives. Previously every runner was mortal: the NATS
client defaulted to 10 reconnect attempts x 2s, consumers were started
exactly once at boot, and an iterator crash was only logged — any NATS
outage longer than ~25 seconds (a rolling restart of the memory-only
JetStream, a node drain) left a deaf-but-alive zombie process holding the
session forever, with /healthz answering 200 unconditionally.

SharedSessionBus now connects with maxReconnectAttempts -1 +
waitOnFirstConnect (boot blocks until NATS is reachable instead of
failing fast), and both consumers run under superviseConsumer: when an
iterator ends or throws, the durable is re-ensured (idempotent; also
recreates one a memory-only JetStream lost across a restart) and
consumption resumes with capped exponential backoff (1s..30s; the reset
requires real progress so an instantly-dying iterator cannot thrash). A
PERMANENT connection close — only possible terminally with unlimited
reconnects (authorization revoked, protocol-fatal) — exits the process
loudly so the kubelet restarts the container (session pods run
restartPolicy Always); durable consumers redeliver anything un-acked.
/healthz now returns 503 once the connection is permanently closed, and
both runner containers carry a livenessProbe (new pods only) as the
belt-and-braces for wedged-event-loop states the exit path cannot see.

Observability: tank_runner_nats_connection_status_total{type} (bounded)
and tank_runner_bus_consumer_restart_total{kind}. A sustained restart
rate means JetStream state is flapping; status churn without restarts is
ordinary reconnection riding out a NATS blip.

Affected contracts:
- Agent Runners ("orchestrator rollout and runner-process restart are
  inside the durability boundary" — now NATS disruptions are too, for
  new session pods)
- Observability (two new bounded counters)

Retirement note:
Pre-deploy session pods keep the mortal behavior until they are recycled
— a deploy cannot repair a live pod's runner (session lifecycle
contract). No wire shape changed: subjects, durable names, and consumer
configs are identical, so old and new runners coexist freely.
