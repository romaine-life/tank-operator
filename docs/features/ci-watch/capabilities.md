# CI Watch Capabilities

Named behaviors for the event-driven rollout / CI-watch surface. See
[../../event-driven-rollout.md](../../event-driven-rollout.md) for the design,
and [../README.md](../README.md) for how capability ledgers are used.

## pr-readiness-request

- **Status:** shipped
- **Intent:** Tank owns a neutral PR/head readiness process. `watch_current_session_pr`
  and the governed-merge gate both register the same readiness request via
  `POST /api/internal/sessions/{id}/pr-readiness`; the legacy `/ci-watches`
  registration drives the same process. The backend reducer reads GitHub's live
  `mergeable_state` plus auditable CI evidence and returns
  `conflict | failed | ready | watching`. When GitHub reports
  `mergeable=null` / `unknown`, the backend schedules a narrow deduped retry for
  the same PR head instead of treating any webhook payload as terminal. Exact
  head-SHA check runs satisfy a check directly. A missing path-filtered check can
  satisfy only when Tank finds the same PR branch's prior green run and proves no
  commit since that run changed the workflow's `pull_request.paths` inputs. This is
  the fix for both the "reports it's good while the PR actually has a conflict" class
  and the "path-filtered checks never appear on unchanged HEAD" class.
- **Durable source:** `session_ci_watches` row (status `watching`) registered by the
  Tank PR-readiness endpoint; legacy `github.commit.ci` control-action payloads
  remain publish/ledger evidence, not the canonical readiness reducer.

## ci-watch-wake

- **Status:** shipped
- **Intent:** When a watched PR's *current head SHA* goes red or conflicted after a
  live reducer read, Tank wakes the owning session with an actionable `ci-failure` /
  `ci-conflict` turn (webhook/retry trigger → reducer → `enqueueSDKTurn`). The agent
  fixes and re-publishes. The agent is **never** woken on success.
- **Durable source:** `POST /webhooks/github` (HMAC-verified) → `session_ci_watches`
  reverse lookup → wake. Stale deliveries (superseded head SHA) and duplicates
  (already-terminal watch) are dropped.
- **Deployment:** delivery requires `GITHUB_WEBHOOK_SECRET` (KV
  `tank-operator-github-webhook-secret` → `externalsecret-github-webhook.yaml`) AND a
  webhook on the tank-operator-host GitHub App pointed at **exactly**
  `https://tank.romaine.life/webhooks/github` — the plausible-looking but nonexistent
  `/api/github/webhook` 404s silently (the misconfigured webhook URL was the other half
  of the 2026-06-17 stall; deliveries were arriving, just to a dead path). With either
  missing the receiver fails
  closed and sessions sleep through CI (the 2026-06-17 "watching" stall, diagnosed via
  an empty `tank_ci_webhooks_total`).

## ci-watch-reaper-protection

- **Status:** shipped
- **Intent:** A session with a `watching` CI watch is excluded from the idle reaper so a
  red/conflict wake can land. Once `ready`/terminal, protection drops — the originating
  session may reap, and the human merges independently.
- **Durable source:** `ClaimIdleForReap` `NOT EXISTS (session_ci_watches ... status='watching')`.

## governed-merge-gate

- **Status:** shipped
- **Intent:** `POST /api/internal/sessions/{id}/governed-merge/verify` is the
  server-side governed-merge gate. It composes the shared Tank PR-readiness
  registration/reconcile process (same one `watch_current_session_pr` drives)
  with governed-publish-proof for the exact branch/commit, and returns an
  `allowed` / `reasons` decision. It is **not** a compatibility facade and is
  not a test-slot deploy path — the test-slot deploy is provisioned server-side
  by the deterministic gate. Its sole caller is the governed `merge` MCP tool in
  the mcp-auth-proxy sidecar, which refuses to merge unless the gate returns
  `allowed=true`.
- **Durable source:** governed-publish-proof remains the control-action ledger
  (`github.commit.push` / `github.break_glass.push`); CI and mergeability are the
  durable `session_ci_watches` readiness row plus live reducer output from GitHub PR
  state.

## governed-merge-control-action-audit

- **Status:** shipped
- **Intent:** Every governed PR mutation (`mark_pull_request_ready_for_review`,
  `merge_pull_request`) records its `control_action_events` audit on the **owning
  session's** ledger, regardless of which principal performed the merge. The agent
  in-pod path and the two orchestrator-mediated paths — the in-app "Merge in Tank"
  button (`handleMergeSessionPR`) and the green-path auto-merge
  (`autoMergeOrchestrationPhasePR`) — produce an identical ledger row keyed to the
  PR's session (event-driven-rollout.md §E).
- **Durable source / mechanism:** mcp-github keys the audit to a `governed_session_id`
  the orchestrator passes (the owning session), falling back to the caller's own
  session for the in-pod agent path. Tank's internal write endpoint
  (`internalCallerMatchesSession`) authorizes **two** verified-IdP writers: a session
  pod for its *own* session (`svc:tank:<id>`, the #1207 rule) and the orchestrator
  control plane (`svc:tank-operator:<id>`) for *any* session. Neither is a
  caller-asserted header.
- **Failure signature:** if the control-plane writer is not recognized, the audit
  `started` write is rejected `403`, the mcp-github tool fails closed *before* the
  GitHub merge, the PR stays an unmerged draft, and the human sees `merge failed:
  mcp-github tool error: Error executing tool merge_pull_request: …`. Watched by
  `tank_control_action_internal_write_total{writer="control_plane",result="forbidden"}`.

## gated-test-slot-provisioning

- **Status:** shipped (shared server-side gate + orchestration-review path + interactive
  endpoint); agent-tool retirement lands in a later slice.
- **Intent:** Provisioning a Tank test slot is deterministic and gated with **zero LLM
  involvement**. The shared `appServer.provisionTestSlotForSession` helper validates a
  session's PR-readiness through a **one-shot live read** (no durable `session_ci_watches`
  row, no wake side effects) classified by the *same* `classifyCIWatchState` reducer the
  CI-watch path uses, and only runs `glimmung.CheckoutTestSlot` →
  `DeployImageToTestSlot` → `mgr.SetTestState` on a `ready` (green + mergeable) verdict.
  Every other verdict refuses without touching glimmung: `failed` lists the failing
  checks, `conflict` asks for a rebase onto main, `merged` is a no-op, a still-`watching`
  PR is re-polled on a bounded settle-wait (interval + hard cap, both injectable) before
  refusing on timeout, and an `expectedSHA` pin that the live head has moved past refuses
  ("redeploy latest") rather than greenlight a superseded commit. The settle-wait runs in
  a background context, never a blocked HTTP handler. `checkoutAndDeployOrchestrationReview`
  is the first caller — its previously **ungated** checkout+deploy now runs behind this
  gate.
- **Durable source:** no new durable row — the gate is a transient in-memory
  `pgstore.CIWatch` fed to the reducer against live `mcpgithub.PullRequestState`. Outcomes
  are observable via `tank_test_slot_validate_total{outcome}` and
  `tank_test_slot_provision_total{outcome}`.

## interactive-test-workflow

- **Status:** shipped (deterministic UI trigger)
- **Intent:** The interactive "test" button is a deterministic, **zero-LLM** server-side
  trigger, not an agent skill send. The UI POSTs
  `POST /api/sessions/{id}/test-workflow/start` (owner-scoped); the backend
  (`handleStartTestWorkflow`) resolves the session's governed-PR coordinates from durable
  state — owner email, repo from `sessions.repos` (single-repo direct; multi-repo
  disambiguated by an explicit `repo` body field or the repo carrying the open
  `session_ci_watches` record, else refused as ambiguous), the governed session branch
  `tank/session/<id>/<repo>`, and the glimmung project mapping — then runs the shared
  `provisionTestSlotForSession` gate **by branch (current head, no PR-number or SHA pin)**
  in a background goroutine with a fresh `provisionBackgroundTimeout()` context, mirroring
  `provisionOrchestrationReviewSlot`. The handler returns **202 Accepted** immediately
  because validate+wait can take minutes. On a `ready` verdict the gate's `SetTestState`
  marks the slot active + URL (the pill lights via the session-row SSE); on **any refusal**
  glimmung and test-state are left untouched. The whole run surfaces inline as a grouped
  role:system thread of display-only `test_provision.updated` records — an opener
  ("Creating test slot."), intermediate validating/waiting progress, and a terminal
  ready/refusal record (the refusal carries the reason). This replaced an earlier
  `ci_status.updated` emission that was **invisible inline**: no projection case exists for
  `ci_status.updated` in `transcript_projection.go`/`conversationReducer.ts`, so those
  records never rendered in the turns view (see ci-status-record below). The interactive agent
  provisioning path has since been retired: the `/test` skill was rewritten so it no longer
  instructs manual slot checkout/deploy/pill steps (it now reflects that provisioning is
  deterministic), and the three agent-facing MCP provisioning tools were removed from their
  servers (the slot checkout/deploy wrappers in mcp-glimmung and the test-pill wrapper in
  mcp-tank-operator). The underlying HTTP endpoints those wrappers called stay — the
  deterministic gate drives them server-side.
- **Durable source:** no new durable row — reuses the gate's transient in-memory
  `pgstore.CIWatch` validation and surfaces the outcome as `test_provision.updated`
  session-event records (display-only; actor=system, source=tank; one per phase, grouped
  by `run_id`). Repo disambiguation reads the durable `session_ci_watches` row.
  Outcomes are observable via `tank_test_slot_interactive_total{outcome}` (terminal
  outcome of the interactive trigger) plus the shared
  `tank_test_slot_validate_total` / `tank_test_slot_provision_total` gate counters.
- **Update (2026-06-19):** the surfacing described above is **retired** — the
  interactive provision is now **page-only** and emits nothing to the transcript.
  History: #1332 first re-authored the outcome as a backend notice turn (replacing
  the orphan `test_provision.updated` records); this change then retired that
  notice turn entirely in favor of the dedicated **test-slot-page** (below). The
  durable outcome lives on the `pending_test_provisions` row (refusal reason /
  done) and the slot pill (`test_state`); the page reads both. Removed:
  `emitTestProvisionRecord` / `newTestProvisionRunID` and the
  `internal/conversation/notice_turn.go` helpers (their sole caller). The
  pre-#1332 `test_provision.updated` event type, its `validateTestProvisionPayload`
  validation, the `applyTestProvision` projection (`transcript_projection.go`), and
  the `conversationReducer.ts` / `conversationProjection.ts` cases are intentionally
  **kept** as a read path so historical durable records still render; retiring them
  is a separate, data-aware migration (old rows must not silently stop rendering).
- **Update (2026-06-19): agent/automation-drivable trigger.** The deterministic
  gate is now reachable programmatically, not only via the owner's browser
  button — closing the gap that left the flow untestable by an agent / CI (and,
  because nothing but a human click ever exercised it, prone to silent rot). Two
  surfaces, both **zero-LLM triggers for the same `provisionTestSlotForSession`
  gate**. This is **not** a reintroduction of the retired LLM-orchestrated
  provisioning wrappers (the removed mcp-glimmung checkout/deploy + mcp-tank-operator
  pill tools): there is no agent-driven checkout/deploy/pill logic — the agent only
  *triggers*, and the server provisions deterministically, exactly as the button
  does. (1) `POST /api/internal/sessions/{id}/test-workflow/start` —
  service-principal gated (`requireServicePrincipal` + `internalCallerMatchesSession`:
  the session's own `svc:tank:<id>` or the orchestrator control plane), sharing the
  extracted `startTestWorkflowForSession` with the browser handler so the
  resolve→double-trigger-guard→launch→202 tail cannot drift between the two
  triggers. (2) a `provision_test_slot` MCP tool (mcp-auth-proxy sidecar injection,
  alongside `publish_current_head`, restricted-git sessions only) that POSTs to (1)
  with the session's forwarded identity (`_tank_caller_session_headers`). Same
  double-trigger guard, same 202, same `tank_test_slot_interactive_total` /
  `tank_test_slot_validate_total` / `tank_test_slot_provision_total` counters.
  Evidence: `TestInternalStartTestWorkflow_*` (own-session 202, cross-session 403,
  non-service 403, missing-glimmung 503, body threading), the unchanged
  `TestStartTestWorkflow_*` browser suite (proves the refactor is behavior-identical),
  and the sidecar `test_provision_test_slot_tool.py` + tools-list contract test.

## interactive-test-workflow-drive

- **Status:** shipped (deterministic provision + post-ready agent wake)
- **Intent:** The "Create test slot and test" button is the **drive** sibling of
  interactive-test-workflow. It runs the **identical** deterministic, **zero-LLM**
  provision (same `POST /api/sessions/{id}/test-workflow/start`, now with a
  `{"drive": true}` body flag) and surfaces the **identical** visible
  `test_provision.updated` role:system thread. The only difference is the
  terminal: on a **ready** verdict — and only then — the runner wakes the
  session's agent with a backend-owned turn instructing it to validate its
  changes against the now-running slot at the slot URL. On **any refusal** no
  wake is submitted (just the thread's refusal record, identical to the plain
  button). **The LLM boundary is the whole point:** provisioning stays zero-LLM
  (the gate), and the agent re-enters only *after* a successful provision, to do
  the inherently-agent part — exercise the running app.
- **Mechanism:** `runInteractiveTestWorkflow`'s ready branch, when `req.drive`,
  calls `driveTestSlot` → `enqueueTestDriveWakeTurn`, which reuses the same
  backend-owned-turn machinery `ScheduleWakeup` fires: `enqueueSDKTurn` persists
  durable `user_message.created` + `turn.submitted` boundaries and publishes a
  `submit_turn` command, tagged payload `source=test-slot-drive`
  (`conversation.TurnSubmittedSourceTestSlotDrive`, added to the
  `turn.submitted` payload.source schema enum). The wake turn is
  `AuthorKind=system` with a deterministic `turn_testdrive_*` client nonce so a
  re-fire on the same (session, branch, url) collapses under JetStream command
  dedupe. The woken prompt assumes the slot already exists and tells the agent to
  validate only — it must NOT reserve/check out a slot; the rewritten
  `/test-drive` skill carries the same assumption.
- **Durable source:** no new durable row — the wake rides the existing
  `session_events` ledger (user_message.created + turn.submitted) like any
  backend-owned turn. Observable via `tank_test_slot_interactive_total{outcome}`
  with `outcome="drive_wake"` (wake submitted) / `"drive_wake_error"` (enqueue
  failed; non-fatal — the slot is up and the ready thread already announced it).

## test-slot-page

- **Status:** in progress (dedicated UI surface; shipped pending slot validation)
- **Intent:** The composer "beaker" is a **navigation entry**, not an action menu:
  it routes to a dedicated per-session page at `/sessions/{id}/test-slot`
  (`SessionRouteTab` `test-slot`) that is the primary surface for the
  create / create-and-test / open / return controls and for PR readiness. This
  replaces the inline beaker dropdown and moves provisioning feedback off the
  transcript and onto a page the user navigates to. **Create gating:** the button
  is greyed out only when there is **no open PR** to test (resolved
  `has_open_pr` is false); an open PR in any CI state stays clickable and the
  gate surfaces its verdict. Provisioning itself is unchanged — the click still
  runs the deterministic, zero-LLM `provisionTestSlotForSession` gate, which
  re-reads live GitHub, so the page's cheap durable display can never cause a
  wrong provision.
- **Durable source:** read-only `GET /api/sessions/{id}/test-slot`
  (`handleGetTestSlotStatus`, owner-scoped) returns a snapshot that must not
  contradict the durable system: last-known readiness from the
  `session_ci_watches` row (cheap, event-driven, rendered "as of"
  `last_event_at`), the in-flight/last interactive `pending_test_provisions`
  row, the resolved governed-PR coordinates (or a soft `repo_error` for an
  ambiguous multi-repo session so the page can render a picker), and the session
  `test_state`. With `?refresh=1` it additionally runs the **same** one-shot
  live read + `classifyCIWatchState` the gate uses (`testSlotPreflight`) with no
  durable row and no side effects, so the page shows an authoritative current
  verdict on demand; `mcpgithub.ErrNoOpenPR` maps to a first-class `no_pr`
  verdict rather than an error. Observable via
  `tank_test_slot_status_requests_total{result}`.
- **Affected contracts:** ci-watch (the readiness display must converge from /
  not contradict the durable watch), session-lifecycle (test-slot provisioning),
  transcript-navigation (new session route, refresh-survivable).
- **Evidence:** `appRoutes.test.ts` (route parse/build + a guard that the page
  route does not shadow the `test-slot-model` approval route),
  `handlers_test_slot_status_test.go` (auth, owner scope, durable coordinate
  resolution, live ready/`no_pr` preflight, multi-repo soft error), frontend
  `tsc` + `vite build`, and a Glimmung test-slot click-through (pending).
- **Enrichment (2026-06-19, follow-up):** the page now names and links the
  entities it talks about: a labeled identity strip (repo ↗ / branch ↗ /
  **PR #N ↗**), and — when a slot is up — an **active-environment panel** (slot
  name/index, URL shown + clickable, **deployed branch @ commit** from the
  provision row, started-at) with a **stale-slot** warning when the deployed SHA
  no longer matches the branch head. The readiness card humanizes
  `mergeable_state`, summarizes the check rollup, and lists **failing/pending
  check names** (from the live preflight's `failing_checks`/`pending_checks`,
  shown only while they describe the current head). The verdict is sourced from
  the durable watch (event-driven) and a **12s durable poll** (paused when the
  tab is hidden) converges the `mergeable → merged` transition without a manual
  refresh; the live preflight (mount + Refresh) supplies the check names. Backend:
  `pr_number` + `pending_checks` added to the preflight view.
- **Fixes (2026-06-19, follow-up):**
  - **Merged showed as "Green & mergeable".** The durable CI-watch row freezes at
    its first terminal verdict — a `ready` watch never flips to `merged` because
    the merge webhook handler skipped any non-`watching` row. Fixed two ways: the
    page now **prefers the live preflight verdict** (the watch is the instant
    paint + fallback) with a **live 20s poll** so a merge while-viewing converges;
    and the merge webhook now **marks even a `ready`/terminal watch `merged`**
    (`github_webhook.go`, before the not-watching coalescing guard). A distinct
    **purple `is-merged`** tone + "Merged" label render it. The read-only status
    endpoint resolves the preflight against the durable watch's **PR by number**
    (not the open PR by branch) so a merged PR is detected as `merged` rather than
    `no_pr` — a merged PR has no *open* PR for the branch, and the watch row may be
    stuck `ready`; the by-number live read sees `merged=true` either way.
  - **Falsely showed "Test environment — running".** An `active:true` `test_state`
    with no URL (the optimistic flag set at "test" session-create) was rendered as
    a running env. The page now requires a real **URL** to show "running"; and the
    provision double-trigger guard (`handleStartTestWorkflow`) requires
    `active && url` so the empty flag no longer 409s a genuine Create.
- **Branch/PR picker (2026-06-19, follow-up):** the page lists **every branch/PR
  this session has worked on** and lets the user pick which one to provision
  instead of silently assuming the session's bare branch. Tank already hooks
  every governed commit, so each PR head it has watched is a `session_ci_watches`
  row; the picker reads them. Backend: `ListForSession` (`pgstore`,
  newest-first) feeds a `prs[]` array on the status response (`testSlotPRViewFrom`,
  each row carrying `pr_number`/`pr_url`/durable `status`/`has_open_pr`); a
  `?pr=<n>` query selects which watch the `?refresh=1` preflight reads and which
  the `pickWatch` default falls back to (newest when unset). The interactive
  trigger (`POST .../test-workflow/start`) accepts `{"pr":N}`, so Create
  provisions the **selected PR's head branch** — `provisionSlotAfterReady`
  deploys `state.PR.Head.Ref` (the resolved head), not always the bare branch.
  The page renders the picker only when the session has **more than one** PR;
  single-PR sessions are visually unchanged and keep provisioning their one
  branch. **Affected contracts:** ci-watch (the picker rows are the durable
  watches, never a parallel store), session-lifecycle (the gate still re-reads
  the selected PR live before provisioning). **Evidence:**
  `TestGetTestSlotStatus_ListsSessionPRs` (list order newest-first, per-row
  `has_open_pr`, default-watch = newest, `?pr=` selection still yields a live
  preflight), `TestGetTestSlotStatus_RefreshDetectsMergedFromWatchPR` (selected
  watch read by number), frontend `tsc` + `vitest` + `vite build`.

## pending-provision-backstop

- **Status:** shipped (durable reconcile backstop + double-trigger guard)
- **Intent:** The two background provisioning entry points
  (`provisionOrchestrationReviewSlot`, `runInteractiveTestWorkflow`) run the
  validate→settle-wait→provision gate in a fire-and-forget goroutine that can
  wait ~23m for CI to settle. An orchestrator restart mid-wait previously dropped
  that provision with **no retry**. This capability closes that gap, mirroring
  the #1295 CI-watch reconcile backstop: a durable `pending_test_provisions` row
  is written `'pending'` at kickoff (deterministic `provision_id` per
  session/repo/branch/kind) and terminalized (`'done'` on any reached verdict
  including a gate refusal, `'failed'` on an infra error) at finish. A reconcile
  loop (`runPendingTestProvisionReconcileLoop`, 5m ticker, wired in `main.go`)
  re-drives any record stranded in `'pending'` past the settle cap + grace
  (~25m) **idempotently**: it takes a conditional `ClaimForRedrive` (attempt-gated
  so two replicas / a double pass cannot both fire the gate — the
  `ErrPendingTestProvisionStale` lost-race pattern), short-circuits to `'done'`
  when a test environment is already active for the session (never
  double-provisions), then re-invokes the same entry point. The **double-trigger
  guard** on the interactive endpoint rides the same row: `Register`'s atomic
  conditional `ON CONFLICT ... WHERE status <> 'pending'` admits one winner, so a
  rapid double-click (or an already-active test environment) is refused **409**
  instead of launching a second gate run / second glimmung checkout.
- **Durable source:** new `pending_test_provisions` table (migrations
  0175–0177; bounded `kind`/`status` CHECK constraints, partial index on the
  stale-pending scan). Observable via
  `tank_test_slot_pending_provision_oldest_age_seconds` (gauge),
  `tank_test_slot_provision_redrive_total{kind}` (backstop re-drives), and
  `tank_test_slot_provision_guard_total{result}` (double-trigger outcomes). The
  `TankTestSlotProvisionStuck` alert fires on the gauge, mirroring
  `TankCIWatchStalled`.

## ci-status-record

- **Status:** event + webhook merge path shipped; **inline rendering NOT implemented**
- **Intent:** A merge is recorded as a display-only `ci_status.updated` event that never
  invokes the agent and never enters the model's replayed context. External merges are
  recorded via the `pull_request` closed/merged webhook.
- **Known gap (corrected 2026-06-18):** `ci_status.updated` does **not** render inline.
  There is no `ci_status` projection case in `cmd/tank-operator/transcript_projection.go`
  and no handler in `frontend/src/conversationReducer.ts`, so these records are durable but
  invisible in the turns view. The earlier claim that a merge "renders (and will ring) in
  the turns view" was aspirational, not implemented. The interactive test-workflow outcome,
  which previously rode this invisible event, now uses the dedicated, projected
  `test_provision.updated` thread instead (see interactive-test-workflow above). A future
  slice that wants merges visible inline must add the projection + reducer cases (the
  `test_provision.updated` path is the worked example).
- **Durable source:** `ci_status.updated` event (actor=system, source=tank).

## orchestration-advance

- **Status:** shipped (advance-on-merge + spawn)
- **Intent:** The deterministic engine that walks a multi-phase orchestration DAG
  with no LLM in the continuation loop. When a phase's PR merges, the merged-PR
  webhook (`pull_request` closed/merged → `advanceOnMerge`) marks the phase
  `merged`, recomputes which still-`pending` phases now have every `depends_on`
  satisfied (all `merged`/`skipped`), and dispatches a spoke session for each —
  cloned off the run's repo `main`, with the phase's stored `brief` as its first
  turn (the same `mgr.Create` + `enqueueSDKTurn` machinery `spawn_run_session`
  uses). When no phase remains active the run transitions to `done`. Idempotent
  against GitHub's at-least-once delivery: every state move is an atomically
  guarded conditional UPDATE (`MarkPhaseReady` pending→ready, `ClaimPhaseForSpawn`
  ready→running, `RequeuePhaseForRespawn` running-with-empty-spoke→ready), so a
  duplicate webhook or a racing replica advances the run exactly once.
- **Durable source:** `orchestration_phases` status/spoke/PR/merge columns
  (#1264); the advance engine drives `orchestrations.state`. Out of scope for this
  slice (later): autonomous green→merge, integration-branch targets, and the
  terminal human-review gate.

## orchestration-phase-pr-link

- **Status:** shipped
- **Intent:** Joins a phase's spoke session to the PR it opened so the merged-PR
  reverse lookup resolves at merge time. When a `running` phase's spoke registers
  its PR via the existing `watch_current_session_pr` / `POST
  /api/internal/sessions/{id}/pr-readiness` handoff, the orchestrator copies the PR
  coordinates onto the phase (`MarkPhasePROpen`, status → `pr_open`). A no-op for
  ordinary (non-orchestration) sessions; never drags a `merged` phase back.
- **Durable source:** `orchestration_phases` `pr_owner/pr_name/pr_number/pr_url`,
  resolved by `spoke_session_id` (index `orchestration_phases_spoke`).

## orchestration-reconcile-backstop

- **Status:** shipped
- **Intent:** The dropped-webhook backstop. A periodic loop re-drives every
  non-terminal run (`ListActiveOrchestrationIDs`): a phase whose PR actually
  merged but whose advance never landed, and any ready/pending phase that should
  have a spoke but doesn't, are repaired without a webhook; a freshly-`approved`
  run's root phases are bootstrapped. A missed webhook degrades to a delay, never
  a hung run. Per-replica idempotent — every effect is a guarded write.
- **Durable source:** `orchestrations` (state `approved`/`running`) +
  `orchestration_phases`; `runOrchestrationReconcileLoop`.
