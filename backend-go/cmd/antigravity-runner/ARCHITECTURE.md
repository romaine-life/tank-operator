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

1. **Prompt Ingestion**: The runner consumes `CommandSubmitTurn` from NATS, writes the prompt followed by a carriage return (`\r`) to `agy`'s PTY standard input.
2. **Interactive Bypasses**: The runner seeds `onboarding.json` and theme settings to both the legacy and new config directories during pod bootstrap. It monitors PTY stdout to automatically click through any unexpected Terms of Service screen.
3. **Transcript Scraping**: `agy` writes JSON-lines steps to `transcript_full.jsonl`. The runner tails this file via `fsnotify`.
4. **Completion**: When the runner parses a completed `PLANNER_RESPONSE` step, it extracts the final answer, publishes `assistant_message.created` and `turn.completed` events back to NATS, and waits for the next turn.

---

## WARNING FOR FUTURE DEVELOPERS (AND AGENTS)

* **DO NOT** attempt to remove the PTY wrapper.
* **DO NOT** attempt to send binary Protobuf messages directly to `agy`'s standard input. `agy` does not speak Protobuf on stdin.
* **DO NOT** assume `localharness` is present or can be used. We must run the raw `agy` binary and act as the harness ourselves.

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
