# Event-Driven Rollout: CI Watch, Auto-Merge, and Synthetic Completion Records

Status: **shipped** (phases 1–4; see
[CI-watch capabilities](features/ci-watch/capabilities.md)). Stage-5 hardening (the
stall alert / dead-man's timer) remains deferred. This doc is the original design;
[Phased delivery](#phased-delivery) tracks what landed.

> **Deployment wiring (required — the webhook half is inert without it).** The receiver
> fails closed when its secret is empty, so production must have BOTH:
> 1. `GITHUB_WEBHOOK_SECRET` mirrored from KV `tank-operator-github-webhook-secret` via
>    `k8s/templates/externalsecret-github-webhook.yaml` (the secret value is seeded
>    out-of-band in KV), and
> 2. a webhook on the **tank-operator-host** GitHub App →
>    `https://tank.romaine.life/webhooks/github` (content-type `application/json`, the
>    same secret, events: `pull_request` / `check_suite` / `check_run` / `workflow_run`).
>
> One app-level webhook covers every governed repo (reverse lookup is by
> `(owner, name, pr)`). Both pieces were missing at first ship (2026-06-17), so every
> "watching" session slept through its CI — confirmed by an empty `tank_ci_webhooks_total`.

Extends [tank-conversation-protocol.md](tank-conversation-protocol.md),
[scheduled-turn-continuity.md](scheduled-turn-continuity.md), and the
[transcript](features/transcript/contract.md) and
[session-lifecycle](features/session-lifecycle/contract.md) contracts. Changes the
behavior described by the `/rollout` skill
(`k8s/session-config/skills/common/rollout/SKILL.md`).

## Problem

The `/rollout` skill today tells the agent to push, then *poll* GitHub Actions and
ArgoCD "about every 5 seconds" until green, then merge. Agents do this badly, and the
failure is not cosmetic — it is non-deterministic:

1. **They watch CI badly.** They set their own long `ScheduleWakeup` timers, the
   estimates are wrong, and they frequently miss the moment CI actually finishes.
2. **They are unreliable narrators of state.** They report "it's good" while GitHub's
   own API says `dirty` (merge conflict) or `blocked`. The dominant root cause is that
   GitHub computes mergeability *asynchronously*: right after a push, `mergeable` is
   `null` and `mergeable_state` is `unknown` until GitHub builds the trial-merge in the
   background. The agent reads it once, too early, sees `unknown` (or a stale `clean`),
   and calls it done — never re-checking after the value resolves.
3. **They sometimes do work while "waiting."** Because the wait is unstructured, two
   runs of the same rollout do not produce the same outcome.

The thing being optimized is **workflow determinism**, not agent speed. The CI signal
itself is trusted (auto-merge on green is acceptable); the defect is that *the agent's
perception and pacing of that signal* sit on the critical path.

## Design principle: move detection and merge off the agent

Split "verify CI" into two responsibilities and relocate only one of them:

- **Detecting the outcome** — noticing CI finished, reading the authoritative
  mergeable/check state, performing the merge on green. This carries no judgment and is
  exactly what agents are bad at. **→ moves to infra.**
- **Owning the result — fixing broken code.** This never moves. It is the agent's code;
  the agent fixes it. **→ stays with the agent**, but the agent is only invoked when
  there is something to fix.

"Done" is redefined as **infra-verified-green-and-merged**, never the agent's
self-report. The agent has no API surface that asserts CI/merge status, so it cannot
"forget to verify" a thing that was never its job. The happy path runs with the agent
**not invoked at all**.

This yields four edges. Three are infra-only; one invokes the agent:

| Terminal state | Who acts | Surface |
| --- | --- | --- |
| All required checks green + mergeable | infra merges (governed) | **synthetic UI record** + notification, agent **not** invoked |
| A required check failed | agent | **real wake** (`submit_turn`), failure + logs in payload |
| Merge conflict / base moved (`dirty`/`behind`) | agent | **real wake**, rebase instructions |
| (deferred) deploy healthy | — | future synthetic UI record (see [Scope](#scope-and-explicit-non-goals)) |

## Architecture

```
agent: push → publish_current_head → watch_current_session_pr → STOP (turn ends)
                                              │
                                              ▼
                               authoritative read (fail-fast)
                          poll mergeable_state until != unknown;
                          read required-check rollup for head SHA
                                              │
                  ┌───────────────────────────┼───────────────────────────┐
              already bad                  clean+pending                already green
          return verdict NOW          register ci_watch row,         (rare) → straight
          (agent fixes in same        agent ends turn                to merge path
           turn, never disengages)            │
                                              ▼
                              GitHub webhook  POST /webhooks/github
                          (check_suite / check_run / workflow_run /
                                  status / pull_request)
                                              │
                          HMAC verify → lookup ci_watch by (repo, pr) →
                          ignore stale head SHA → coalesce → compute terminal
                                              │
                 ┌────────────────────────────┼────────────────────────────┐
            green+mergeable                  red                        conflict
        server-side governed merge   enqueueSDKTurn(source=        enqueueSDKTurn(source=
        → ci_status.updated record   ci-failure, payload=checks+  ci-conflict, payload=
        + notification (NO turn)     logs) → agent fixes          base) → agent rebases
```

### A. Agent contract — rewritten `/rollout` skill + new handoff tool

The skill stops instructing the agent to poll. Proposed replacement for steps 3–6 of
`k8s/session-config/skills/common/rollout/SKILL.md`:

```markdown
3. **Hand off CI to Tank.** After your branch is published, call the Tank MCP
   `watch_current_session_pr` tool. It performs the authoritative mergeability/CI
   read for you — you do not read check status yourself.
   - If it returns `conflict` or `failed`, fix it now, in this turn, and re-publish.
   - If it returns `watching`, you are done driving. End your turn with a one-line
     status ("pushed, CI watching"). **Do not** set a timer, estimate a duration,
     poll CI, or do other work. Tank will record the merge when CI is green, or wake
     you with the failure if it is not.
4. **(removed — Tank merges on verified green.)**
5. **(removed — deploy watch is out of scope for now; see event-driven-rollout.md.)**
6. **(removed — the completion record is emitted by Tank, not narrated by the agent.)**
```

New tool `watch_current_session_pr`, added to the Tank governed-tool surface in
`claude-container/mcp-auth-proxy/src/mcp_auth_proxy/server.py` (same place as
`merge_current_session_pr` / `publish_current_head`: schema in
`_append_tank_publish_tool_to_json` ~L493–725, handler ~L2315–2562, dispatch ~L4405–4430):

```jsonc
{
  "name": "watch_current_session_pr",
  "description": "Hand off CI/mergeability watching for the current session's governed PR to Tank. Performs the authoritative read (resolves GitHub's async mergeable_state) and either returns an immediate problem to fix, or registers a watch and returns 'watching' so the agent can end its turn. Tank emits a completion record on green-and-merged, or wakes the session on failure/conflict.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "repo":      { "type": "string",  "description": "owner/name; defaults to current session repo" },
      "pr_number": { "type": "integer", "description": "defaults to the governed PR for this branch" }
    },
    "additionalProperties": false
  }
}
```

Handler behavior (fail-fast, synchronous — this is the value the agent cannot fumble):

1. Resolve repo + governed PR + current local/remote/PR head SHA (reuse the head
   reconciliation already in `_verify_github_hot_swap_head`).
2. **Resolve mergeability authoritatively**: GET the PR, and if `mergeable == null` /
   `mergeable_state == "unknown"`, re-GET with backoff until GitHub has computed it
   (bounded, e.g. ≤10s). This is the single fix for the "says it's good over a
   conflict" class.
3. Resolve CI evidence for the PR head. A check is satisfied by an exact-head green
   run, or by a prior green run on the same PR branch when Tank can prove every commit
   since that run skipped the workflow because its `pull_request.paths` inputs were
   unchanged. If the workflow path filter cannot be inspected, exact-head evidence is
   required.
4. Return one of a small, unambiguous set:
   - `dirty` / `behind` → `{"state":"conflict", "base": "...", "detail": ...}`
   - a required check already failed → `{"state":"failed", "failing": [...], "logs_url": ...}`
   - `unknown`-but-no-checks-yet **and** checks expected → register a watch, return
     `{"state":"watching"}` (do **not** treat "no checks reported yet" as green —
     see [the empty-green trap](#the-empty-green-trap)).
   - `clean` + required checks still pending/missing with changed inputs → register a
     watch, `{"state":"watching"}`
   - `clean` + all required CI evidence satisfied → go straight to the merge path,
     `{"state":"merging"}`.
5. Registering a watch = upsert a `ci_watches` row (below) and record a control-action
   ledger event (`action=watch_current_session_pr`) for audit, consistent with every
   other governed PR mutation.

### B. Webhook ingestion (new — does not exist today)

There is no inbound GitHub webhook path in tank-operator today. Add one.

- **Route**: `mux.HandleFunc("POST /webhooks/github", s.handleGitHubWebhook)` registered
  in `backend-go/cmd/tank-operator/server.go` `registerRoutes` as a **public**
  (non-`requireAuth`) route — GitHub posts without our bearer.
- **Authentication is the signature, not a JWT**: verify `X-Hub-Signature-256`
  (HMAC-SHA256 over the raw body) with `crypto/hmac` + `crypto/sha256` against a
  `GITHUB_WEBHOOK_SECRET` sourced from Key Vault via ESO. Reject mismatches before
  parsing. This is new middleware/helper; nothing in the repo does HMAC today.
- **Subscribed events**: `check_suite`, `check_run`, `workflow_run`, `status`,
  `pull_request` (for `synchronize`/base-move/conflict signals). Configure the
  subscription on the **`tank-operator-host`** GitHub App (the automation app that
  authors session PRs; credentials in KV under `tank-operator-app-*`). For
  user-installed repos via the public `romaine-life-tank-operator` app, the same
  endpoint receives that app's deliveries — route by installation/repo.
- **Delivery is at-least-once and lossy**: GitHub may retry or drop a delivery. The
  handler must be idempotent (keyed on delivery id + computed terminal state) and the
  design must not assume every event arrives (see deferred backstop in
  [Scope](#scope-and-explicit-non-goals)).

### C. Reverse index: (repo, PR, head SHA) → session

Webhooks identify a repo + PR + SHA; we need the owning session. The association data
already exists in `control_action_events` (`repo_owner`, `repo_name`, `pr_number`,
`result_sha`, `session_id`, written by `publish_current_head` and the PR tools), but
there is **no index** for reverse lookup and no place to hold watch lifecycle. Add a
dedicated table rather than scanning the ledger:

```sql
-- new migration in backend-go/internal/pgstore/migrations.go
CREATE TABLE IF NOT EXISTS ci_watches (
    watch_id        text PRIMARY KEY,
    owner_email     text NOT NULL,
    session_scope   text NOT NULL,
    session_id      text NOT NULL,
    repo_owner      text NOT NULL,
    repo_name       text NOT NULL,
    pr_number       integer NOT NULL,
    head_sha        text NOT NULL,            -- current; webhooks for other SHAs are stale
    status          text NOT NULL,            -- watching | merged | failed | conflict | superseded | abandoned
    required_checks jsonb NOT NULL DEFAULT '[]'::jsonb,
    merge_commit    text NOT NULL DEFAULT '',
    registered_at   timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_event_at   timestamptz
);
CREATE INDEX ci_watches_repo_pr ON ci_watches (repo_owner, repo_name, pr_number);
CREATE INDEX ci_watches_session ON ci_watches (owner_email, session_scope, session_id);
CREATE INDEX ci_watches_active  ON ci_watches (status) WHERE status = 'watching';
```

The webhook handler looks up by `(repo_owner, repo_name, pr_number)`, then **discards
the event if `payload head sha != ci_watches.head_sha`** (a new push superseded it; the
agent's re-publish updates `head_sha` and the old suite is irrelevant). This is the
stale-SHA guard.

### D. Terminal-state computation + coalescing

Diagram: [CI Watch Webhook Reconcile Shape](features/ci-watch/ci-watch.html),
an overview with one page per lane.

A single PR emits dozens of `check_run`/`check_suite`/`workflow_run`/`status` events.
The watcher does **not** wake on each. On every (non-stale) event it recomputes the
CI evidence for the current head SHA — reusing the same resolver as
`_verify_github_hot_swap_head` (latest-per-name, path-aware prior-green evidence,
mergeable + `mergeable_state`) — and only acts on a **transition into a terminal
state**:

- all required CI evidence satisfied + `mergeable == true` + `mergeable_state == clean` → **green**
- any required check `conclusion ∈ {failure, cancelled, timed_out}` → **red**
- `mergeable_state ∈ {dirty, behind}` → **conflict**
- otherwise still pending → record `last_event_at`, do nothing

Coalescing falls out of "compute aggregate, act only on transition": three suites
finishing within a second produce one terminal transition, hence one action.

#### The empty-green trap

"No red checks" is **not** green. Immediately post-push, or on a PR whose required
workflows have not registered yet, the check list can be empty and `mergeable_state`
`clean` — which naively reads as green and would merge on nothing. Guard: a watch only
reaches **green** when every expected check has auditable evidence: either present and
green on HEAD, or absent on HEAD with a prior green run whose workflow inputs are proven
unchanged by the workflow's `pull_request.paths` filter. Until that evidence exists, the
state is pending, not green. Note `check-pr-body` is a required check (per
tank-operator `CLAUDE.md`), so the Feature-Contracts PR-body gate is naturally part of
"green" — no special-casing needed.

### E. Green path — server-side governed merge + synthetic UI record

On the **green** transition, infra (orchestrator, no agent) performs the governed merge
and then emits a display-only completion record.

**Merge.** `merge_current_session_pr` today runs in the Python mcp-auth-proxy sidecar,
invoked by the agent. The auto-merge needs a **server-side (Go) equivalent in the
orchestrator** that reuses the existing governance gates:

- verify against the durable ledger via the internal `/api/internal/sessions/{id}/hot-swap/verify`
  endpoint (already server-side),
- re-verify live GitHub head + mergeability + checks,
- mark the draft PR ready (`markPullRequestReadyForReview`) — session PRs start as
  drafts (`repo-cloner`); the agent's `watch_current_session_pr` handoff is the
  readiness signal,
- `PUT /repos/{o}/{r}/pulls/{n}/merge` with a `tank-operator-host` installation token
  (minted through the existing mcp-github path),
- record the `merge_current_session_pr` control-action event exactly as the agent path
  does, so the ledger is identical regardless of who merged.

**Completion record (the "synthetic UI turn").** This is a display-only conversation
event — it renders in the turns view and rings the notification, but **does not invoke
the agent and is never written to the model's replayed context**. The seam already
exists: only `submit_turn` commands on the data-plane JetStream consumer
(`tank.cmd.<scope>.<session>.commands.<provider>`) reach `query()` in
`claude-runner/src/runner.ts`; every event on the `tank.session.>` bus is a durable
record only. `scheduled_wakeup.updated` (`conversation/builders.go` →
`ScheduledWakeupUpdatedEventMap`, `actor=system`, `source=tank`) is the exact precedent.

Add a sibling event type `ci_status.updated`:

```jsonc
// conversation/types.go + a CIStatusUpdatedEventMap in builders.go
{
  "type": "ci_status.updated",
  "actor": "system", "source": "tank",
  "payload": {
    "kind": "ci_status",
    "state": "merged",                 // merged (green path); red/conflict use a real turn instead
    "repo": "owner/name",
    "pr_number": 1234,
    "pr_url": "https://github.com/owner/name/pull/1234",
    "head_sha": "abc123",
    "merge_commit": "def456"
  }
}
```

Emit it via the backend session-bus persister exactly like any backend-owned event:
upsert into `session_events`, publish the SSE wake. Because it is backend-emitted and
never touches the data-plane consumer, it works for **pre-deploy session pods** with no
pod migration (satisfies the session-lifecycle / Agent-Runners validation requirement in
`CLAUDE.md`'s migration checklist) and the runner never appends it to the model JSONL.

**Notification.** The green record does not ride a turn-completion activity transition
(no turn ran), so the existing `shouldRingForActivityTransition`
(`frontend/src/sessionActivity.ts`) will not fire for it. Extend the ring predicate to
ring on a `ci_status.updated` record (ideally a distinct chime from the turn-complete
`upgrade-complete.mp3` — semantically "your PR landed," not "your turn"). The
`conversationReducer.ts` gains a render case (a system "PR merged ✓ — <link>" bubble,
mirroring `isScheduledWakeupEntry` in `App.tsx`).

### F. Red / conflict path — real wake

On **red** or **conflict**, the agent must fix its own code, so this *is* a real turn.
Reuse the `ScheduleWakeup` mechanism verbatim — `enqueueSDKTurn`
(`handlers_turns.go` ~L991) with a new `source` (`ci-failure` / `ci-conflict`),
`AuthorKind=system`, writing the normal `user_message.created` + `turn.submitted`
boundaries and publishing `submit_turn`. The payload is **actionable and pre-fetched**
so the agent's next turn is productive without a round-trip:

- red: failing check name(s), conclusion, a `get_workflow_job_logs` excerpt, head SHA.
- conflict: `mergeable_state`, the base branch, "rebase onto base and re-publish."

The agent fixes, re-publishes (post-commit hook → `publish_current_head` updates
`ci_watches.head_sha`), and the watch resumes on the new SHA. The agent is in the loop
on every red, out of it on every green.

### G. Enforcement gate — "done" cannot be faked

Two layers, both off data we already hold:

1. **Structural**: the agent has no tool that emits "rollout complete." The only
   completion signal is the infra-emitted `ci_status.updated{merged}`. Nothing to
   forget.
2. **Server-side gate**: a session is not "rollout-complete" while it owns a
   `ci_watches` row in `status='watching'`. This row already gates the reaper
   correctly — like a pending `session_scheduled_wakeup`, an active watch must keep the
   session out of idle-reap (extend the reaper's claim predicate in
   `internal/sessions` / `sessionregistry.ClaimIdleForReap` to also exclude
   `ci_watches.status='watching'`). The session's `rollout_state` jsonb can mirror the
   active watch for UI.

## Protocol & data-model summary

| Change | Location | Kind |
| --- | --- | --- |
| `POST /webhooks/github` + HMAC verify | `cmd/tank-operator/server.go`, new handler | new public route |
| `GITHUB_WEBHOOK_SECRET` | KV → ESO → env | new secret |
| webhook subscription | `tank-operator-host` (+ public app) GitHub App | external config |
| `ci_watches` table + 3 indexes | `internal/pgstore/migrations.go` | additive migration |
| `ci_status.updated` event type | `conversation/types.go`, `builders.go` | additive event (display-only) |
| `source=ci-failure` / `ci-conflict` | `enqueueSDKTurn` callers | additive turn source |
| server-side governed merge | orchestrator (Go) reusing hot-swap/verify + mcp-github token | new internal path |
| `watch_current_session_pr` tool | `mcp-auth-proxy/.../server.py` | new governed tool |
| ring on `ci_status.updated` | `frontend/src/sessionActivity.ts`, `conversationReducer.ts`, `App.tsx` | additive UI |
| reaper excludes active watch | `sessionregistry.ClaimIdleForReap` | predicate change |
| rewritten `/rollout` steps 3–6 | `k8s/session-config/skills/common/rollout/SKILL.md` | skill edit (ConfigMap-shipped) |

No change to the data-plane/control-plane JetStream consumer subjects or filters — the
new event rides the existing `tank.session.>` bus stream. Per the `CLAUDE.md` migration
checklist for durable consumers: **no `ConsumerConfig` field changes, so no consumer
remediation is required**, and pre-deploy pods need no recreation.

## Scope and explicit non-goals

**In v1:** governed PRs only (the Tank-owned `tank/session/<id>/<repo>` flow, where the
PR↔session link and ledger already exist). The pipeline **stops at merged-green.**

**Deferred, by decision, each with rationale:**

- **Backstop / dead-man's timer.** Webhooks are blind to *non-events* (a hung CI run, a
  required check that never registers, a dropped delivery) — a watching session would
  sleep until the 7-day reaper. v1 relies on the human as backstop ("I can tell when an
  agent is making no progress"). Known blind spot: a silently-stalled watch looks
  identical to a correctly-waiting one. The clean fix when it bites is one armed
  `ScheduleWakeup` per watch (first-to-fire wins; also doubles as reaper protection).
- **Red-loop circuit breaker.** `red → wake → fix → re-publish → re-watch` has no floor;
  on an unfixable failure it can ping-pong. This is a pre-existing agent failure mode
  the event design does not worsen, and the defined loop here makes a breaker *easier*
  to add later (cap N attempts; escalate early on same-check-fails-twice).
- **Deploy / ArgoCD hop.** Same disease as CI polling, one layer down. It generalizes
  cleanly: build the webhook→record bridge generic enough that "deployed" becomes
  *another* `ci_status.updated`-style synthetic record (infra watches Argo via its
  notifications controller or the Application CRD status; agent still never invoked).
  Until then, post-merge deploy watching stays as-is.

## Observability

Per `CLAUDE.md`, observability is a completion requirement, not a follow-up. New
`tank_*` counters:

- `tank_ci_webhooks_total{event,result}` — `result ∈ received|verified|rejected_sig|no_watch|stale_sha`
- `tank_ci_watch_registered_total`
- `tank_ci_terminal_total{state}` — `green|red|conflict`
- `tank_ci_automerge_total{result}` — `merged|verify_failed|merge_conflict|error`
- `tank_ci_wake_total{source}` — `ci-failure|ci-conflict`
- `tank_ci_watch_age_seconds` (histogram) — registration→terminal; surfaces stalls the
  deferred backstop would otherwise catch.

Alert: a watch in `status='watching'` with `now - last_event_at` beyond a high
threshold (proxy for the missing backstop while it is deferred).

## Failure modes

- **Dropped webhook** → watch never terminates. Accepted in v1 (human backstop);
  `tank_ci_watch_age_seconds` + alert make it visible.
- **Stale SHA** → discarded by the `head_sha` guard (§C).
- **Empty/premature green** → blocked by the required-set guard (§D, empty-green trap).
- **Merge race / verify-fail at merge time** → `tank_ci_automerge_total{verify_failed}`;
  re-enter watch or wake the agent rather than force-merge.
- **Session reaped mid-watch** → prevented by the reaper-exclusion gate (§G); a watch is
  as reap-protective as a pending scheduled wakeup.

## Feature contracts touched

This PR (when implemented) must name and prove:
[tank-conversation-protocol](tank-conversation-protocol.md) (new system event type,
display-only seam preserved), [transcript](features/transcript/contract.md) (a
non-turn record renders durably and is excluded from model context),
[session-lifecycle](features/session-lifecycle/contract.md) (active watch participates
in reap protection; pre-deploy pods unaffected). Add a `capabilities.md` entry for the
named behavior "event-driven rollout watch."

## Phased delivery

Each stage is independently shippable and coherent.

1. **Authoritative read, no waiting.** Add `watch_current_session_pr` returning
   `conflict|failed|merging|watching` synchronously (the mergeable_state resolution +
   required-check read), and the `ci_watches` table. Rewrite `/rollout` to call it and
   stop. Even with no webhook yet, this alone kills the "says it's good over a conflict"
   bug — registration just has nothing to fire it yet.
2. **Webhook → terminal computation.** `POST /webhooks/github` + HMAC, reverse lookup,
   stale-SHA + coalescing + empty-green guards, transition detection. Log terminal
   states; do not act yet.
3. **Red/conflict wake.** Wire terminal red/conflict to `enqueueSDKTurn` with
   pre-fetched payloads. Agent fix-loop closes.
4. **Green path.** Server-side governed merge + `ci_status.updated` record +
   notification + reducer rendering + reaper-exclusion gate. Happy path goes
   agent-free.
5. **Hardening.** Full observability/alerts, idempotency, `capabilities.md`, contract
   evidence.

## Open questions

- Required-check source of truth for the empty-green guard: branch-protection API at
  registration vs. a configured expected-set per repo. Branch protection is
  authoritative but adds a GitHub call on the hot path.
- Server-side merge token path: reuse mcp-github's host-installation minting directly
  from the orchestrator, or add a small internal endpoint. Prefer reuse.
- Distinct completion chime vs. reusing the turn-complete sound.
