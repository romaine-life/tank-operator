# Antigravity Runner Architecture (Our Custom localharness)

This directory implements the runner (harness) that hosts and executes the Google `agy` CLI binary for `antigravity_gui` chat sessions.

## The Core Constraint: Why a PTY is Required

1. **No direct API**: The `agy` CLI binary does not expose a WebSocket or Protobuf server on its own.
2. **Closed-Source `localharness`**: The official `localharness` binary shipped inside the `google-antigravity` Python SDK is closed-source and only supports GCP Service Accounts. It cannot be used because our sessions require consumer OAuth authentication (Google One) to bypass billing requirements.
3. **Terminal Emulation (PTY)**: Since we must run the `agy` CLI binary, we are forced to emulate a human operator. `agy` uses interactive terminal features (e.g. bubbletea UI) and will hang or fail if run via standard piped I/O. We wrap it in a Pseudo-Terminal (PTY) using `github.com/creack/pty` to fool it into believing a real human is running it in a terminal.

---

## Architectural Event Flow

Our runner acts as the adapter layer (the harness) between the cluster's NATS JetStream session bus and the interactive `agy` process:

```
                  +--------------------------+
                  |   Tank Go Orchestrator   |
                  +-------------+------------+
                                | NATS Command
                                v
               +----------------+----------------+
               |  Go Harness (antigravity-runner) |
               +---|--------------------------^--+
                   | PTY stdin                | fsnotify
                   v                          | transcript_full.jsonl
               +---|--------------------------+--+
               |            agy CLI              |
               +---------------------------------+
```

1. **Model selection**: The orchestrator stamps the validated create-time
   session model into the pod manifest as `TANK_SESSION_MODEL`. The runner
   starts the resident process with `agy --model <TANK_SESSION_MODEL>` before
   the first turn and reports the applied value through Tank's internal
   runtime-config endpoint. Missing model env is a startup error; Antigravity
   sessions must not silently inherit a provider default.
2. **Prompt Ingestion**: The runner consumes `CommandSubmitTurn` from NATS, writes the prompt followed by a carriage return (`\r`) to `agy`'s PTY standard input.
3. **Interactive Bypasses**: The launcher (`antigravity-container/antigravity-runner-launch.sh`) seeds `onboarding.json` and theme settings to both the legacy and new config directories during pod bootstrap, so `agy` never presents onboarding/consent screens at runtime. The runner does not script the TUI: the PTY reader only drains output (agy blocks if the PTY buffer fills) and mirrors it to pod logs. If a new interactive screen appears, extend the seeded config files — do not add keystroke replay. The retired ToS auto-accept (PTY-stdout sniffing + replayed arrow/enter keys) raced real turn input and broke on TUI copy changes; its reintroduction is blocked by `TestPTYRunnerArchitectureConstraint` and `scripts/check-removed-chat-runtime.mjs`.
4. **Transcript Scraping**: `agy` writes JSON-lines steps to `transcript_full.jsonl`. The runner tails this file via `fsnotify`.
5. **Completion**: When the runner parses a completed `PLANNER_RESPONSE` step, it extracts the final answer, publishes `assistant_message.created` and `turn.completed` events back to NATS, and waits for the next turn.

---

## Why a Persistent Process (not `agy -p` per turn)

`agy` has a one-shot print mode (`agy -p`), and Tank's first chat runtime (the
May 2026 Python `exec_proxy`, removed in #437 "Remove legacy chat runtime") ran
Claude in the equivalent shape: spawn one headless process per run, read its
output, let it exit. The Antigravity GUI runner deliberately did not repeat
that shape. One `agy` process starts per session pod and stays alive across
turns, because:

1. **Per-spawn bootstrap cost.** Every `agy` start re-pays CLI init plus the
   auth handshake; first-ready takes up to 2 minutes (`waitForCliReady`), and
   `agy -p` enforces a ~30s auth timeout (see the antigravity notes in
   `backend-go/cmd/tank-operator/handlers_terminal.go`). Per-turn spawning
   turns that into per-turn latency and a per-turn failure mode.
2. **Conversation continuity.** The resident process holds the conversation.
   One-shot turns would depend on `agy` resume semantics that have no
   documented contract.
3. **Mid-turn control.** Tank's Stop path delivers SIGINT to the live process
   (`activeProcess.interrupt`) and must resolve in a durable
   `turn.interrupted`. A spawn-per-turn model has no stable target for
   interrupts, and the Tank Agent Runners contract requires exactly one
   durable terminal per turn.
4. **Native long-lived behaviors.** `agy` fires its own timers and background
   tasks and emits its own continuations between turns; the runner observes
   and relays them (see "The long-running-agent harness contract" below and
   the self-continuation relay capability in
   `docs/features/agent-runners/capabilities.md`). Self-continuation only
   exists while the process lives — a per-turn spawn has no one to continue.
5. **One-shot mode buys nothing here.** Even in print mode, `agy` performs the
   same auth/bootstrap dance (the 30s auth timeout above), the no-TTY hang
   risk documented above has not been cleared for `-p`, and the transcript
   files would still be the output contract. One-shot mode gives up the
   warm-state and control benefits without removing any of the PTY/seeding
   machinery.

This mirrors where the other providers landed: `claude-runner` holds one
SDK-spawned `claude` process (stream-json stdio) for the pod lifetime, and
`codex-runner` holds one `codex app-server` (JSON-RPC stdio). Antigravity sits
one rung lower on the interface ladder — PTY-driven input plus structured
transcript-file output — only because the structured front door
(`localharness`) is closed-source and GCP-Service-Account-only. Note that
`localharness` is not a cleverer way to drive the `agy` TUI: it is the same
agent loop compiled as a headless server with an RPC front end, so no TUI
exists in its path at all.

Revisit this if any of the following ship: consumer-OAuth support in
`localharness`, an open-source harness, or a documented headless/structured
mode in `agy`. Any of those should replace the PTY harness the same way the
codex exec transport was retired once the app-server transport could field
`request_user_input`.

---

## Process Death Is Session-Terminal (No Revival)

When the `agy` process exits — crash, OOM, or a Stop whose SIGINT turned out
to be fatal — **the session is done by design**. This is a deliberate product
decision (2026-06-10): restarting agy in place would resume the chat with a
fresh process that has lost the conversation (silent amnesia), and a
container restart via kubelet does the same thing implicitly. Neither is
acceptable as silent behavior, and revival is explicitly not part of the
architecture. If process death turns out to be frequent,
`tank_antigravity_runner_process_exit_total` and
`tank_session_provider_fatal_total` are the "how often" — design revival
deliberately at that point instead of inheriting it from a restart policy.

Mechanics, in order:

1. A `cmd.Wait()` supervisor goroutine observes the exit and closes the
   `activeProcess.exited` channel.
2. An in-flight turn resolves through the `exitedChan()` arm of
   `handleSubmitTurn`'s select: durable `turn.interrupted` if a Stop was in
   flight, otherwise `turn.failed{reason:"provider_process_exited"}`. The
   command is acked only after the terminal publishes.
3. The runner reports `POST /api/internal/sessions/{id}/provider-fatal`
   (projected SA token, self-session only). The orchestrator moves the
   session row to `Failed` through the same RowWriter transition pod death
   uses, so sidebar/activity/UI gating behave identically.
4. The runner stays alive but **inert**: subsequent `submit_turn` commands
   drain immediately to durable
   `turn.failed{reason:"provider_process_unavailable"}` instead of stranding
   un-acked in JetStream. The runner must NOT exit — a container exit would
   trigger kubelet's restart policy and relaunch agy with amnesia, which is
   exactly the revival this design forbids.

Two related liveness bounds share the same select:

- **Submit-ack watchdog** (`ANTIGRAVITY_SUBMIT_ACK_TIMEOUT_MS`, default 60s):
  the prompt write into the PTY is fire-and-forget, so if no transcript
  record at all appears within the window (the `USER_EXPLICIT` echo is the
  usual first signal), the turn fails durably as `prompt_not_accepted`.
  Deliberately no auto-retry: re-writing the prompt double-executes if agy
  did receive the first write.
- **Interrupt grace** (`ANTIGRAVITY_INTERRUPT_GRACE_MS`, default 10s): a Stop
  SIGINTs agy, but if agy neither settles a DONE planner response nor exits
  within the grace window, the runner forces the durable `turn.interrupted`
  anyway (mirroring codex-runner's "continue with durable Stop terminal").
  Interrupts with no active turn are ignored — SIGINTing an idle agy would
  be a session-terminal event for no reason.

---

## Silence Is the Boundary (turn-settle window)

`agy`'s planner loop runs multiple rounds per burst — an ack prose response,
tool-call rounds, a settled prose response — and **writes no end-of-burst
marker anywhere**. This was established empirically (slot-1 sessions 159/160,
tank-operator#1035): the transcripts carry no terminal record; `messages/` is
task-inbox read-state; task logs are per-task; `cli.log`'s only trailing line
is a `text_drip.go` typewriter-animation debug entry. agy's loop knows when a
burst is over and never says so. **Silence is the provider's boundary
semantics.**

The runner therefore publishes `turn.completed` only after a settled prose
response has been followed by `ANTIGRAVITY_TURN_SETTLE_MS` (default 2s) of
transcript silence; any further step cancels an armed window and a later
settled prose re-arms it. Observed intra-burst gaps are ~600ms, so the default
is ~3x margin. The prose itself streams to the user immediately — only the
turn-status transition waits.

This is what keeps the answer-first sequence ("I'll set a timer" → turn would
have closed → schedule tool call lands seconds later) inside ONE turn: the
task starts in-turn with clean attribution, the terminal's
`background_work_pending` is evaluated after the burst truly ended, the
session parks without a false `ready`/ring, and no untracked self-wake relay
is manufactured. If agy ever pauses longer than the window mid-burst, the turn
closes early and the continuation machinery (relay + fold + causal-adjacency
attribution) handles it exactly as before — the window failing re-admits a
cosmetic flicker for that turn, never incorrectness.
`tank_antigravity_runner_turn_settle_total{outcome="extended"}` counts how
often bursts continue past a settled prose (the answer-first frequency);
`outcome="quiet"` counts silence-confirmed boundaries.

Revisit if `agy` ever ships an explicit end-of-processing marker in its
transcript or logs — that signal should replace the window outright.

---

## The Transcript Writer Rewrites In Place (session-scoped step dedupe)

`agy`'s `transcript_full.jsonl` usually grows by appends, but it is **not a
contract-append-only event log**: agy performs its larger writes as an
**in-place truncate + full rewrite** — same inode, byte-identical prefix,
final content = old steps + new steps. This was established live (2026-06-11,
probe session 799, agy CLI 1.0.6): a 0.3s sampler saw only monotone sizes and
a constant inode through a heavy turn, snapshot prefixes stayed cksum-identical
— and the durable ledger still recorded the runner re-publishing the entire
prior history mid-turn. The only code path that can do that is the
`size < offset` rewind in `sweepTranscripts`, so a sweep's stat (fsnotify
fires per write, far denser than any sampler) landed inside the sub-second
truncate window. **The replay is a race**: zero on light sessions, routine on
real workloads with large step outputs (sessions 791/792/793 all hit it
repeatedly; 791's trigger was a ~30KB tool-result write). Replayed bytes are
*real transcript movement* (they clear the submit-ack watchdog and extend the
settle window) but they are **not new work**.

Step dedupe is therefore **session-scoped** (`runnerState.seenSteps`, keyed by
`providerStepID` + status), never per-turn. A (step, status) pair publishes
durable events exactly once, under the turn that first observed it. The
per-turn dedupe this replaced re-published the whole history under whatever
turn was live at the race: session 791's ledger carried turn 1's items under
four turn_ids, per-turn item counts grew cumulatively (2 → 35 → 270 → 282 —
O(N²) ledger growth), and expanding turn N in the Turns view showed turns
1..N. The same guard protects the idle path: a replayed idle MODEL step must
not be re-buffered into the next turn or manufacture a phantom
self-continuation relay (`handleStep` checks `stepObserved` before
buffering/waking; marking happens only at publish time in `observeStep`).

Why dedupe instead of a smarter cursor: resuming at the old offset after a
shrink would bet correctness on the rewrite prefix staying byte-identical —
an undocumented invariant of a closed binary, and one a future compaction
would silently break. Claude/Codex runners don't need any of this because
they consume push streams over stdio (SDK stream-json / app-server JSON-RPC):
each event arrives exactly once by construction. File-scraping is
at-least-once by nature; the session-scoped step identity is the adapter that
discharges it into Tank's exactly-once ledger — the same move as JetStream
`event_id` upserts and the `taskEventsPublished` marker dedupe (#1035).

The set lives in process memory on purpose: agy process death is
session-terminal (above), so there is no restart this map must survive that
the startup byte-cursor skip does not already cover. Task-lifecycle markers
have their own session-scoped dedupe (`taskEventsPublished`) because they must
be tracked even when no turn is active; the two sets are deliberate siblings.
`tank_antigravity_runner_step_replay_suppressed_total{context}` counts
suppressions (turn vs idle) — intermittent and workload-correlated, so a zero
on a light session is normal while cross-turn duplicate items in
`session_events` with a flat counter is the regression signature.

---

## WARNING FOR FUTURE DEVELOPERS (AND AGENTS)

* **DO NOT** attempt to remove the PTY wrapper.
* **DO NOT** attempt to send binary Protobuf messages directly to `agy`'s standard input. `agy` does not speak Protobuf on stdin.
* **DO NOT** assume `localharness` is present or can be used. We must run the raw `agy` binary and act as the harness ourselves.
* **DO NOT** re-add PTY-stdout sniffing or keystroke replay for onboarding/consent screens. Seed the config files in the launcher instead.

---

## The long-running-agent harness contract (timers, builds, self-continuation)

This is the **first harness in the codebase for a long-running, self-managing agent**,
and the rules below are the precedent. agy is **not** one-shot like the Claude/Codex
SDKs: it is one persistent process that schedules its own work, runs it in the
background, and **continues itself** when that work finishes. The harness's job is to
keep it alive, observe it, and faithfully relay what it does — never to drive it.

### Two different things both called "a turn"

- **SDK turn** — what `agy` reports: a `PLANNER_RESPONSE` `DONE` is a provider yield, and
  we map it to `turn.completed` **faithfully**, exactly as Claude/Codex do. We never
  withhold, delay, or fabricate it. Raw provider→Tank mappings are out of bounds.
- **User-facing turn** — the unit that needs the human's attention. It is a **projection
  over the SDK-turn stream**: it stays open while the agent is working *or has background
  work pending*, and it **ends — and summons — only at an SDK `turn.completed` that lands
  with no background work running.** A `turn.completed` that lands while a timer/build/task
  is in flight is mid-work, not an end. This is the same spine as
  `docs/scheduled-turn-continuity.md` (#906): the simulated turn spans the wake-chain;
  `working → scheduled` (parked) does not ring, `working → ready` (nothing pending) does.

A wait is **not** a user-facing boundary. "Waiting for CI", "I scheduled a 5s timer" — the
human has no business being summoned there. (The phone analogy: a wake-fired ping is
backwards — *the agent* initiated the continuation, not the user.)

### Realms split (keep these apart)

- **localharness realm** (agy + this runner): agy owns its timers/tasks, fires them, and
  emits its own continuation. The runner keeps agy alive, tails continuously, tracks
  pending background work, and **relays** agy's self-continuation.
- **Tank realm** (orchestrator): authors the durable turn boundaries and **records +
  projects** the user-facing turn. It never owns a clock for agy, never injects a wake,
  never registers a `session_scheduled_wakeups` / `session_background_task_wakes` row for
  antigravity. (Those tables and their fire loop are the *Claude/Codex* model, where the
  agent genuinely cannot self-continue.)

### How "background work pending?" is computed (the load-bearing signal)

agy routes **all** background work — `schedule` timers, `run_command` builds/shells, and
anything else — through one uniform task framework. The signal is in
`transcript_full.jsonl`, correlated by **task id**:

- **pending / started:** a `MODEL` step with `status:"RUNNING"` whose content is
  `Tool is running as a background task with task id: <X>`.
- **done:** a `SYSTEM` / `SYSTEM_MESSAGE` step whose content carries `sender=<X>`
  ("expired" / "finished with result").

So **pending-set = { task ids with a RUNNING start and no matching `sender=<X>` completion }**;
empty ⇒ no background work running. The RUNNING marker always lands **before** the SDK
`turn.completed`, so at the terminal the runner already knows whether work is pending —
no race. The runner stamps that observation onto the `turn.completed` it emits
(`payload.background_work_pending`), and the activity fold folds a would-be-`ready`
terminal to the non-summoning `scheduled` status when it is set.

Verified evidence (no Tank injection — the runner was observing only):

```
# session 62 (schedule timer) and 64 (run_command build/shell) — identical shape:
step  MODEL/PLANNER_RESPONSE DONE   tool: schedule{DurationSeconds} | run_command{CommandLine,WaitMsBeforeAsync}
step  MODEL/{GENERIC|RUN_COMMAND} status=RUNNING  "Tool is running as a background task with task id: …/task-N"
step  MODEL/PLANNER_RESPONSE DONE   "I have …; I will wait and report back"   ← SDK turn completes WHILE task-N runs
step  SYSTEM/SYSTEM_MESSAGE  sender=…/task-N  "…expired/finished…"            ← task-N completes (no Tank involvement)
step  MODEL/PLANNER_RESPONSE DONE   "…has completed. Output: …"               ← agy SELF-CONTINUES and reports
```

### "The agent must not wake itself up" = no *untracked* resumption

agy continues **only** when (a) the user sends a message, or (b) one of its tracked tasks
fires the `SYSTEM_MESSAGE` above. Every self-continuation is immediately preceded by a
task completion → always attributable. A continuation with **no** preceding task
completion would resurrect a user-facing turn we'd already closed — that is the forbidden
"self-wake", a blind-spot bug. Primary guard: the jsonl pending-set is complete.
**Backstop:** agy is a single process (`tank-supervisor → antigravity-cli-runner → agy`);
its background commands are descendants of the agy pid, observable via `/proc` (`ps` is
not in the image). An agy continuation while the pending-set is empty is the signature to
detect.

### WARNING FOR FUTURE DEVELOPERS (AND AGENTS)

* **DO NOT** make Tank own or fire a clock for agy — no `registerScheduledWakeup`,
  `registerBackgroundTaskWake`, `/scheduled-wakeups`, or `/background-task-wakes` from this
  runner; antigravity is rejected by `supportsScheduledWakeups` and the bg-task gate on the
  orchestrator. That is the puppeteer model for an agent that self-continues, and it
  double-wakes.
* **If agy timer/background waking looks broken, the bug is in the RELAY** (the runner
  failing to publish agy's idle self-continuation, or the `background_work_pending`
  annotation) — **never** a missing wake mechanism. agy already wakes itself; do not build
  a waker for it. (This is the trap that cost ~20 prior attempts.)
* The user-facing turn spans the wait as one episode; do not emit a second user-facing
  summon for a self-continuation that follows pending work.
