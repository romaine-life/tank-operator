# CI Watch Capabilities

Named behaviors for the event-driven rollout / CI-watch surface. See
[../../event-driven-rollout.md](../../event-driven-rollout.md) for the design,
and [../README.md](../README.md) for how capability ledgers are used.

## pr-readiness-request

- **Status:** shipped
- **Intent:** Tank owns a neutral PR/head readiness process. `watch_current_session_pr`
  and the hot-swap/test-slot gate both register the same readiness request via
  `POST /api/internal/sessions/{id}/pr-readiness`; older `/ci-watches` callers are
  compatibility facades. The backend reducer reads GitHub's live
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

## test-slot-ci-gate

- **Status:** shipped
- **Intent:** Glimmung test-slot deployment still requires a governed publish record for
  the exact branch/commit, but it no longer owns a separate CI/mergeability gate.
  `/api/internal/sessions/{id}/hot-swap/verify` is now a compatibility facade over
  the same Tank PR-readiness registration/reconcile process used by
  `watch_current_session_pr`.
- **Durable source:** publish proof remains the control-action ledger
  (`github.commit.push` / `github.break_glass.push`); CI and mergeability are the
  durable `session_ci_watches` readiness row plus live reducer output from GitHub PR
  state.

## gated-test-slot-provisioning

- **Status:** shipped (shared server-side gate + orchestration-review path); interactive
  endpoint and agent-tool retirement land in later slices.
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

## ci-status-record

- **Status:** shipped (event + webhook merge path); in-Tank merge surface in progress
- **Intent:** A merge surfaces as a display-only `ci_status.updated` record in the turns
  view — it renders and (will) ring, but never invokes the agent and never enters the
  model's replayed context. External merges are recorded via the `pull_request`
  closed/merged webhook.
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
