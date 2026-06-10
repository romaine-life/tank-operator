# Scheduled Turn Continuity

Status: implemented in PR #906 (branch `scheduled-turn-state`). Built in three
stages on one branch: (1) the non-summoning `scheduled` status; (2) a broken
self-resume rings the summon via the durable `away_error` bit; (3) cancel +
prompt-mid-sleep take-over (a `cancelled` wake state). As-built refinements over
the original sketch below: the ring keys off the durable `away_error` provenance
bit (not prior status, which flickers through `scheduled -> submitted -> error`);
and a direct `scheduled -> ready` is a cancel/clear and does NOT ring — the
genuine end-of-chain hand-off arrives as `streaming -> ready`.
Extends [tank-conversation-protocol.md](tank-conversation-protocol.md) (state
machine, provider self-scheduled wakeups, AskUserQuestion pause/resume) and the
[transcript](features/transcript/contract.md) and
[session-lifecycle](features/session-lifecycle/contract.md) contracts. Names are
working names (`scheduled`).

## Problem

When the agent schedules its own continuation (Claude `ScheduleWakeup`,
Antigravity `schedule`, and the same shape for `run_in_background` task wakes)
it is **not done** — it has parked itself and will resume on a clock. Today the
runner emits `turn.completed` for
that turn, so:

- the activity fold (`sessionactivity.DeriveActivitySummaryWithStats`) lands
  `ready`, which `sessionActivity.ts -> sessionActivityDotStatus` paints as the
  muted "your turn" dot, and
- `shouldRingForActivityTransition` sees `working -> ready` and **rings the
  turn-complete alert.**

So a self-parked agent trips the exact "your turn, human" summon it shouldn't.
Separately, because the parking message carries a `tool_use` block,
`claude.ts` sets `turn.finalAnswer = undefined` (last assistant message wins;
any tool_use clears it), so the pre-sleep summary is **demoted to Turn
activity**, not promoted — the user is summoned to a transcript with no settled
message.

Scheduled wakeups have **zero** influence on activity status today; that is the
gap.

## Spine

`scheduled` is the **sibling of `needs_input`.** Both are pause-phases of a live
*simulated turn*, decoupled from the backend's turn boundaries; they differ only
in what resumes them:

- `needs_input` — paused, resumes on **your input** -> **summon** (it needs you)
- `scheduled` — paused, resumes on the **clock** -> **no summon** (it doesn't)

`needs_input` pauses *within* one backend turn (the backend turn stays alive).
`scheduled` pauses *between* backend turns (turn N completed; the wake-fired
turn N+1 has not started). Either way the user-facing turn is **mid-flight** —
the agent is thinking / sleeping / about-to-resume. The sleep is a phase *inside*
a live turn, not a gap after a dead one. The simulated turn spans the whole
wake-chain and ends only at the first backend `turn.completed` with **no pending
wake.**

Neither phase is terminal; neither is a flavor of `ready`.

## The summon invariant

The turn-complete alert means exactly: **"you're needed, and you weren't already
on it."** Every decision below falls out of this one rule.

| transition | ring? | why |
| --- | --- | --- |
| working -> ready (finished, nothing pending) | yes | you may be away |
| working -> needs_input (asked you) | yes | it needs you |
| working -> scheduled (parked itself) | no | it's on it, not done |
| stopping/stopped -> ready (you pressed Stop) | no | you're on it |
| a turn **you** submitted -> error | no | you're watching |
| scheduled -> error (self-resume broke) | **yes** | promised a wake, you left |

The last row fights today's code: `shouldRingForActivityTransition` deliberately
stays silent on `error` ("presumably already looking at it"). A broken
self-resume is the one error you are guaranteed *not* watching, so it must ring —
keyed off the failed turn's **provenance** (`source=schedule-wakeup`/`background`),
not the prior status (which flickers through `submitted`/`claimed` on the fire
path).

## Decisions (locked)

1. **`scheduled` is a non-terminal, non-summoning status** beside `needs_input`
   in the state machine. Add it to the Go fold and the TS
   `ConversationActivityStatus` enum. Because the ring predicate keys off status
   and `scheduled` is not in `{ready, needs_input}`, the false summon is
   suppressed *for free*; the genuine end-of-chain (`-> ready`) still rings
   exactly once.

2. **The user-visible turn spans the wake-chain.** It holds the wake-chain
   together as one continuous "agent working, don't summon" episode. The backend
   turns stay durably distinct (own `turn_id` / provider boundary / ledger
   events), but the transcript and Turns projection attribute wake-continuation
   output back to the originating turn so the user does not see two agent turns
   for one simulated turn. The lifecycle envelope still reduces to one durable
   predicate evaluated at each turn terminal: **does this session have a pending
   (registered, unfired, uncancelled) wake?**

3. **Nothing enters the main transcript for a sleep.** A sleep is
   intra-(simulated)-turn state; only the simulated turn's true end promotes a
   final answer and rings. No announcement card — attention-required gates
   conversation surfacing, and a sleep requires nothing (contrast AskUserQuestion,
   whose card exists *because* it needs you). The pre-sleep summary stays in Turn
   activity, where it already lives. This closes the original "which summary
   wins" question: the end is the sole settled record *because the sleeps are
   inside the turn.*

4. **Failure model.**
   - *Fire attempted, then failed* (publish/NATS failure; session momentarily not
     Active): durable error -> **rings**. This is recording the outcome of an
     action the orchestrator is actively taking, not a timer. `MarkFailed` must
     stop being a silent DB write and emit a durable, provenance-tagged error
     event the fold can see.
   - *Fire never happened* (loop regressed, row never claimed): **fleet metric +
     operator alert only** — the due-gauge climbing while fire-rate sits at zero
     is "wakeups stopped firing," at fleet blast radius, on the existing Grafana
     surface. **No per-session watchdog.** A detector that depends on a second
     timer watching the first is recursive self-distrust with the same failure
     modes.

5. **One bell.** Finish, question, and broken-wake all ring the *same* alert.
   Broken-wake rings because it is an *away-error*, discriminated by provenance —
   not by making all errors ring.

6. **User prompt mid-sleep = Stop.** A new prompt interrupts the live simulated
   turn: cancel the pending wake (cancel-resolution path), and the prompt opens a
   new turn. A still-looping agent re-arms on the very turn that handles the
   message, so the loop survives iff the agent still means to loop. Add an
   **explicit cancel** control on a `scheduled` session for silent abort (the
   literal "Stop" for this state).

7. **Colors (owner: implementer).** The "waiting for you" treatment **and** the
   ring are reserved for `ready`/`needs_input` — one signal, undiluted.
   `scheduled` reads **live-but-holding** (an agent mid-work, sleeping on its own
   clock — not idle, not "your turn"). `working` reads live; `error` stays red.
   Fixes today's inversion where "your turn" is the muted grey.

8. **Cost is bounded by legibility, not enforcement.** No reaper, no loop cap —
   that would police the autonomy the feature exists to enable and would kill the
   legitimate long monitor. The surviving obligation (heavy bar:
   understood / measured / **or** bounded) is visibility: the sidebar keeps
   parked sessions shown and countable, and operator metrics expose the aggregate
   (sessions parked, for how long, pods pinned). Trust is not blindness.

## Design principle

**Trust the agent's autonomy; instrument the aggregate; never build per-session
scaffolding to police it.** Drawn three times here — no watchdog on the wake, no
forced wake-retention on a user prompt, no reaper on a loop. It is the
philosophical companion to the technical spine (`scheduled` = sibling of
`needs_input`).

## What it touches

Raw provider->Tank adapter mappings are **out of bounds** — we do not distort
what the SDK reported. The change lives in the durable Tank projection:

- **Backend fold** — `backend-go/internal/sessionactivity/activity.go`: add
  `scheduled`; a turn terminal with a pending wake folds to `scheduled` instead
  of `ready`. Needs a durable "session has a pending wake?" read (new store query
  over `session_scheduled_wakeups` + `session_background_task_wakes`,
  `status IN ('scheduled','claiming')`); today only a scope-wide due *count*
  exists.
- **Two pending sources, one fold** — the `scheduled` override
  (`backend-go/internal/sessioncontroller/chat_activity.go ->
  applyScheduledWakeOverride`) unifies two independent "is there pending work?"
  signals so the bidirectional ready↔scheduled fold stays correct: (1) the durable
  Tank wake tables above, for Claude/Codex — their SDKs cannot self-continue, so
  Tank owns the wake row and fires it; and (2) a self-managing agent's own report —
  Antigravity (`agy`) stamps `turn.completed.payload.background_work_pending` while
  a background task it owns is in flight, and has NO Tank wake row (agy fires its
  own clock and the runner relays the continuation via `/agent-continuation`). The
  fold surfaces the runner's flag through `ActivityFoldStats.BackgroundWorkPending`;
  the override parks on either source and never strands `scheduled` when both clear.
  See `backend-go/cmd/antigravity-runner/ARCHITECTURE.md`.
- **Race** — the runner registers the wake *after* `turn.completed`
  (`claude-runner/src/runner.ts -> registerWakeup`), so a naive fold flashes
  `ready` before the row exists. Land the schedule intent at the terminal (emit
  it before the terminal, like `turn.awaiting_input`, or refresh the activity
  summary transactionally with the wake-row write) so the fold goes
  `working -> scheduled` without passing through `ready`.
- **Lifecycle visibility** — `backend-go/cmd/tank-operator/scheduled_wakeups.go`
  persists `scheduled_wakeup.updated` for registration, cancellation, fire, and
  failure. Background -> Scheduled is fed by the timeline bootstrap and session
  event stream, so the user can see a pending timer before it fires.
- **Frontend** — `frontend/src/sessionActivity.ts`: add `scheduled` to
  `ConversationActivityStatus` and `sessionActivityDotStatus` (live-but-holding);
  keep `shouldRingForActivityTransition`'s user-turn set as `{ready, needs_input}`
  plus the provenance-keyed broken-wake ring.
- **Cancel** — explicit cancel control on a `scheduled` session; reuses the
  cancel-resolution path; prompt-mid-sleep triggers the same cancel.

## Open / deferred / out of scope

- **Structural chain grouping** (one visible container holding every backend turn
  in a wake-chain): not required for summon; reconstructable later from the
  durable wake-row links.
- **Background-task wakes**: same *summon* treatment (the agent resumes itself ->
  don't summon). Whether a live background process shares the `scheduled`
  indicator or gets a sibling (running-on-a-process vs sleeping-on-a-clock) is an
  indicator nuance for the implementer.
- **Status name**: `scheduled` is a working name (`sleeping` / `parked`
  candidates).
- **Pod cost/lifecycle of long sleeps**: predates this feature (provider
  self-scheduled wakeups already keep the pod alive across sleeps); handled here
  only by the legibility obligation above.

## Definition of done (per quality-timeframes.md)

- `scheduled` is a first-class durable state in the fold and both enums; no
  fallback path that lets a parked turn read as `ready`.
- The schedule-intent -> status path is race-free (no `ready` flash) and
  reload / fresh-tab stable (folded from durable state, never browser-local).
- Wake failure (attempted) is a durable, visible, ringing error; wake
  never-fired is covered by a fleet metric + alert.
- Migration guard: a contract/migration test fails if a self-parked turn
  re-introduces `ready` + ring, or if the ring's user-turn set silently grows.
- Counters: `scheduled` entries/exits by resolution (fired / failed / cancelled /
  superseded-by-prompt); the parked-session aggregate gauge.
- Docs: this note graduates into the conversation protocol's state machine and
  the transcript/lifecycle contracts when built; provider self-scheduled wakeups
  do not "ring on completion" through a second path.
