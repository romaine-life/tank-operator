# Session Lifecycle Capabilities

This ledger names session lifecycle behavior that crosses browser,
orchestrator, database, and pod boundaries.

## Soft-Deleted Session Recovery Metadata

Status: in progress

Intent:
Let an admin or support agent recover the durable create-time shape of a
soft-deleted session without direct Postgres credentials. Soft deletion is a
sidebar tombstone, not a loss of registry metadata needed to understand or
recreate the session.

Affected contracts:
- Session Lifecycle
- Session Bar
- Auth And Streams

Contract impact:
- Normal `/api/sessions` and `/api/sessions/{id}` continue to hide
  `visible=false` rows because the product sidebar should not reopen deleted
  sessions.
- Admin-only `/api/debug/session-list-state` includes invisible rows for one
  bounded owner/scope and carries the durable recreate inputs: `mode`, `name`,
  `repos`, `capabilities`, `model`, and `effort`, plus runner-reported
  `runtime_model`/`runtime_effort` for verification.
- Recovery does not require browser devtools, raw database credentials, or a
  second partial endpoint. The debug surface reads the same durable sessions
  row that drives session-list catch-up and exposes only bounded row metadata,
  not transcript contents or provider credentials.

Evidence:
- `backend-go/cmd/tank-operator/handlers_debug_session_list_test.go`
  (`TestDebugRowJSONCarriesRecoveryRunConfig`) covers invisible-row recovery
  metadata.
- `backend-go/cmd/tank-operator/handlers_debug_session_list_test.go`
  (`TestDebugSessionListStateAdminGate`) covers the admin/service-admin gate.

## Tank-Owned Session Run Options

Status: complete

Intent:
Make Tank the source of truth for create-time session modes and provider
model/effort choices, so browser UI and MCP tools cannot guess or hardcode a
model string that the backend cannot run.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- `GET /api/session-run-options` and
  `GET /api/internal/session-run-options` expose the accepted create modes,
  SDK chat modes, provider model allowlists, effort allowlists, and defaults.
  The browser fetches this metadata before enabling new-session creation; MCP
  exposes it through `get_session_run_options` for agents that want to pick a
  non-default run shape.
- Tank validates create, turn, runtime-config, and mid-session
  run-config-update model/effort values against the same provider-owned
  allowlists. Rejections are hard `400` responses with an actionable
  allowed-value list instead of a silent runner default. (`run_config_update`
  is the surface label for the mid-session `PUT /api/sessions/{id}/run-config`
  path — see the Mid-Session Model/Effort Change capability below.)
- `antigravity_gui` sessions stamp the validated create-time model into the
  pod manifest as `TANK_SESSION_MODEL`; the runner must pass that exact value
  runtime-config endpoint. Missing model env is a startup failure, not a silent
  provider default.
- `codex_exec_gui` and `codex_app_server` are retired create modes. Existing
  historical rows can still render as chat sessions, but new creates through
  browser, internal, or MCP paths must use `codex_gui`.
- Codex sessions must carry an explicit account-supported model. The unsupported
  bare `gpt-5.3-codex` string is not advertised; the supported Spark variant is
  `gpt-5.3-codex-spark`.
- Browser-stored and profile-stored run preferences are reconciled against the
  Tank metadata before they can seed a new session, so an agent-created session
  cannot poison the user's next-session default with an invalid model string.

Evidence:
- `backend-go/cmd/tank-operator/handlers_run_options_test.go` covers the
  browser and internal metadata endpoints and asserts retired Codex modes and
  bare `gpt-5.3-codex` are not advertised.
- `backend-go/cmd/tank-operator/handlers_sessions_test.go`,
  `handlers_turns_test.go`, and `handlers_internal_test.go` cover retired-mode,
  unknown-mode, unsupported-model, missing-model, and unsupported-effort
  rejection paths.
- `backend-go/internal/sessionmodel/sessionmodel_test.go` covers
- `backend-go/cmd/tank-operator/observability_test.go` covers the
- `frontend/src/modelEffortDefaults.test.ts` covers the SPA's Tank metadata
  fetch, Claude provider-key normalization, preference reconciliation, and
  create-time readiness guard.
- `mcp-tank-operator/tests/test_tools.py` and `tests/test_client.py` cover the
  MCP `get_session_run_options` tool and assert MCP schemas do not carry local
  hardcoded model/mode enums.
- Metric: `tank_session_run_config_rejected_total{surface,provider,reason}`.

## Mid-Session Model/Effort Change

Status: complete

Intent:
Let a user change a running Claude or Codex GUI session's model/effort from the
composer, so model selection is no longer locked at create time. The change
applies to the next turn and preserves the conversation.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- `PUT /api/sessions/{id}/run-config` updates the durable user-chosen
  `model`/`effort` columns (the create-time-immutable assumption is retired).
  The turn handler already overrides each `submit_turn` with the registered
  config, so the next turn carries the new values; validation reuses the
  create/turn allowlist choke point and rejects under the `run_config_update`
  surface. Antigravity is rejected (its model is an `agy` process-start arg).
- The runner re-pins at an idle turn boundary, never mid-turn: the claude-runner
  tears down and rebuilds `query()` with provider-session resume + the new
  options; the codex app-server transport drops and re-resumes its thread.
  model/effort are sealed within a turn, re-pinnable between turns (see the
  Agent Runners contract). The composer model chip is an interactive dropdown
  for Claude/Codex (read-only for Antigravity); a pick applies to the next turn
  silently and never interrupts a running turn.
- The runner-reported context window is latest-observed-wins so a switch to a
  model with a different window updates the durable UI denominator.
- Per-turn model history: because the model can change between turns, the
  resolved model/effort for a turn is stamped onto its `user_message.created`
  and `turn.submitted` payloads (the event payload is additionalProperties, so
  no schema change) and surfaced on the projected user-message entry. The Turns
  surface shows the model each turn actually ran on — distinct from the composer
  chip, which only reflects the next turn's selection. A historical turn keeps
  showing its own model after later re-pins.

Evidence:
- `backend-go/cmd/tank-operator/handlers_run_config_test.go` covers the
  run-config route: Claude switch, model-only-preserves-effort, unsupported
  model/effort, missing Codex model, Antigravity rejection, and not-found.
- `claude-runner/src/runner.test.ts` covers scheduling a re-pin on a differing
  turn and `performRebuild` rebuilding with resume + the new model.
- `codex-runner/src/appServerTransport.test.ts` covers re-resuming the thread
  under a new model on a mid-session change.
- `frontend/src/modelEffortDefaults.test.ts` covers the Claude/Codex-gated
  composer dropdown, the run-config `PUT` (option-a next-turn apply), and the
  per-turn model capture-and-render guard.
- Per-turn model history:
  `backend-go/internal/conversation/contract_test.go` (model/effort stamped on
  both boundary events' payloads) and
  `backend-go/cmd/tank-operator/transcript_projection_test.go`
  (`SurfacesPerTurnModel` — surfaced on the projected user-message entry).
- Metrics: `tank_session_run_config_rejected_total{surface="run_config_update"}`,
  `tank_runner_options_repinned_total{model,effort}`.

## SpireLens MCP Session Capability

Status: in progress

Intent:
Let a user opt a pod-backed session into the SpireLens game-host MCP without
making tailnet membership or host tools part of every Tank session.

Affected contracts:
- Session Lifecycle
- Auth And Streams
- Observability

Contract impact:
- Session creation must persist the selected capability in the session row and
  pod annotation so refresh, reconnect, and debugging do not depend on browser
  state.
- No-pod session modes must reject the capability because there is no pod
  lifecycle boundary where the tailnet join can occur.
- The session pod must join the tailnet with projected `auth.romaine.life`
  identity only; no static auth key or manual relay is allowed.
- The MCP listener must remain absent unless the session selected the capability
  and the deployment configured a host upstream.

Evidence:
- `backend-go/internal/sessionmodel/sessionmodel_test.go` covers capability
  normalization and SpireLens pod manifest wiring.
- `backend-go/internal/sessions/manager_test.go` covers manager admission,
  configured/unconfigured behavior, no-pod rejection, and pod annotations/env.
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go` covers the
  opt-in bootstrap exchange chain and userspace `tailscaled` launch.
- `backend-go/cmd/tank-operator/handlers_session_list_events_test.go` and
  `frontend/src/sessionStore.test.ts` cover capabilities on the session row
  update wire.
- `claude-container/mcp-auth-proxy/tests/test_server.py` covers conditional
  SpireLens listener activation.
- `helm template tank-operator ./k8s` covers chart rendering of the deployment
  env and derived `mcp.spirelens.json` ConfigMap entry.

## Test-Slot Session Image Override

Status: in progress

Intent:
Let a developer iterating in a Glimmung test slot make NEWLY-created sessions
boot branch-built session-runner code, not just update the already-running
pods. The slot's orchestrator stamps a per-scope override image onto new pods
the same way production stamps its chart-pinned image — no runtime overlay, full
accuracy. Production is never affected.

Affected contracts:
- Session Lifecycle
- Observability

Contract impact:
- New session pods stamp the chart-pinned SESSION_IMAGE / CODEX_SESSION_IMAGE
  UNLESS a durable override exists for the session's scope; the override is the
  only thing that can change a new session's image.
- The override is keyed by `session_scope`, is refused for the production scope
  (`"default"`) at both the write endpoint and the create-time resolver, and the
  resolver is wired only when the test-env gate
  (`SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED`) is on. Production orchestrators never
  read or apply it.
- A lookup failure falls back to the pinned image rather than failing session
  creation.
- The override is durable (survives orchestrator rollout), covers Claude,
  scope, so a slot override can never bleed into prod or another slot.

Evidence:
- `backend-go/internal/sessions/manager_image_override_test.go` covers
  apply/no-op for slot vs prod scope, gate-off, resolver-error fallback, and the
  per-mode image family.
- `backend-go/cmd/tank-operator/handlers_session_image_override_test.go` covers
  the endpoint happy path, prod-scope refusal, gate-off 403, missing-image 400,
  and human-role rejection.
- `backend-go/internal/pgstore/migrations.go` migration 0086 +
  `internal/pgstore/session_image_overrides.go` store.
- Metrics `tank_session_image_override_applied_total{scope,image_kind}` and
  `tank_session_image_override_write_total{action}`.
- Operator flow: `docs/testing.md` → "Making new slot sessions inherit a change".


Status: in progress

Intent:
mounted the harvested OAuth blob (refresh token included) into the
Claude/Codex providers use.

Affected contracts:
- Session Lifecycle
- Observability

Contract impact:
  launch script seeds a placeholder token (`access_token:
  never refreshes in place.
  proxy pod), injects the real access token per request, single-flight-refreshes
  against `oauth2.googleapis.com` on upstream 401, and writes rotated blobs back
- The credential authority is a single production deployment; validation slots

Evidence:
- `backend-go/internal/sessionmodel/sessionmodel_test.go`
  + `oauth-gateway-ca` mount.
  (form-encoded refresh, KV-sourced client secret), `expiry` blob patch, and
  `expiry`-based freshness.
- Migration guards: `scripts/check-removed-chat-runtime.mjs` blocks the retired
  surface; `scripts/check-test-slot-provider-credentials.sh` asserts slots route
  `tank-api-proxy` ServiceMonitor; the provider-generic `TankApiProxy*` alerts
  cover it).


Status: in progress

Intent:
sessions. The pod-level `mcp-auth-proxy` sidecar and chart-managed

Affected contracts:
- Session Lifecycle
- Agent Runners

Contract impact:
  `/opt/tank/session-config/mcp.json` into
- The launch script validates the source is a JSON document with an
  `mcpServers` object and fails the runner if the mounted config is missing or
  failure, not a degraded mode.
- The runtime authority remains the chart-managed session config plus the
  `mcp-auth-proxy` sidecar. No MCP bearer or upstream credential is written into

Evidence:
  initialized a fake HTTP MCP server and issued `tools/list`.
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  script materializes the native MCP config and fails before runner exec when
  `mcp.json` is absent or malformed.


## Durable idle-session reaper

Status: shipped (2026-06-12, issue #1079 item 1)

Intent:
Truly abandoned sessions (left running past `idleTimeoutSeconds`, default
7 days) are deleted without ever endangering unattended-but-live work.
The prior reaper lived in sessions.Manager on per-replica in-memory
state: its WebSocket guard (`TrackWS`) had zero call sites, its only
activity feed was the SPA's visible-tab 30s touch endpoint, and its
clocks reset on every deploy. It would have deleted any unattended live
session (autonomous agent mid-task, a session parked on a durable
ScheduleWakeup, an MCP `spawn_run_session`) after idleTimeout of replica
uptime, while never reaping anything across frequent deploys. Pod
deletion is terminal by design, which made that shape a latent destroyer
of live work.

The replacement is durable end to end. `sessionregistry.ClaimIdleForReap`
evaluates idleness and claims the row in ONE conditional UPDATE: visible
row with a pod, `updated_at` past the cutoff (every accepted turn, runner
event, status transition, and read-state refresh bumps it through the
sessions-row writers), settled activity status (`ready`/`error`; a
working-ish status defers to the stranded-turn sweep, whose terminal
restarts the idle clock), and no pending scheduled wakeup /
background-task wake / undispatched launch turn (a parked agent's clock
is a liveness promise, not idleness). Claiming marks the row invisible
before any pod deletion, so concurrent activity defeats the reaper
atomically and both replicas collapse on the claim with no leader
election. `Manager.Delete` is reused as the executor (pod delete +
idempotent re-mark + sidebar tombstone publish). The SPA's `/touch`
endpoint and loop were retired with the in-memory state.

Affected contracts:
- Session Lifecycle ("session-pod deletion is terminal"; the reaper now
  consumes only durable session state)
- Observability (`tank_idle_sessions_reaped_total{result}`)

Retirement note:
Do not reintroduce browser-presence signals (WebSocket counts, tab
touches) into the reap predicate. Browser disconnects are explicitly
inside the durability boundary, so presence can never be evidence of
abandonment. If reap latency ever matters, lower the interval, not the
evidence bar.

## Conversation resurrection (transcript capture)

Status: in progress (Stages 1–3 implemented: capture, resurrection
endpoints/runner-resume/SPA trigger, and the contract amendments; Codex parity
and a durable `resurrected_from` lineage column are deliberate follow-ups)

Intent:
Make a session's *conversation* survivable across pod death. Session pods are
`emptyDir`-backed; a node drain (AKS node-image upgrade, eviction) destroys the
pod and the Claude SDK's on-disk JSONL transcript — the only resume-faithful
record, because it carries `thinking`/`redacted_thinking` blocks + signatures
that `session_events` deliberately drops. The owner accepts losing the
`/workspace` filesystem (uncommitted edits) but not the conversation. Full
design: [docs/session-transcript-capture.md](../../session-transcript-capture.md).

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- Capture is additive and changes no existing behavior: the claude-runner runs
  an in-process, read-only, crash-isolated snapshotter that ships whole-file
  JSONL snapshots to an orchestrator-internal endpoint, which stores them in a
  private Azure Blob container. `session_events` remains the display projection;
  the transcript blob is the separate resume-faithful record. This closes the
  Agent Runners gap where the resume-faithful record lived ONLY on the pod.
- Capture is best-effort: when transcript storage is unconfigured the endpoint
  answers `503` and the runner counts a skip and retries — never an error, never
  a turn-loop fault.
- Resurrection is a NEW explicit lifecycle, not a revival: `POST
  /api/sessions/{id}/resurrect` (Claude-only) creates a new session that
  re-clones the same repos and whose runner fetches the dead session's captured
  transcript (brokered `GET .../resume-transcript`, orchestrator re-verifies
  ownership) and `resume`s it. Pod death stays terminal; the workspace is still
  gone; SDK-version mismatch starts fresh rather than a corrupt resume. Lineage
  currently rides pod env (`TANK_RESURRECT_SOURCE_SESSION_ID`); a durable
  `resurrected_from` column + UI badge is a follow-up.

Evidence:
- `claude-runner/src/transcriptCapture.test.ts` (capture scan/dedup core) and
  `claude-runner/src/resumeBootstrap.test.ts` (materialize, version-mismatch,
  path containment, fetch-failure-starts-fresh).
- `backend-go/internal/transcriptstore/store_test.go` (Put/Get/Latest, buffer
  isolation, last-write-wins).
- `backend-go/cmd/tank-operator/handlers_internal_transcript_test.go`
  (path-traversal guard, blob-key/metadata sanitization, header decode).
- Auth reuses `requireInternalSessionPodCaller` (SA TokenReview + live pod
  lookup); resurrect uses `requireAuth` owner scoping.
- Infra: `infra/transcript_storage.tf`. Chart: `k8s/values.yaml`
  `transcriptStorage` + `k8s/templates/deployment.yaml` env.
- Metrics: `tank_runner_transcript_capture_total{result}` +
  `tank_runner_transcript_capture_lag_ms` +
  `tank_runner_transcript_resume_total{outcome}` (runner);
  `tank_transcript_upload_total{result}` + `tank_session_resurrect_total{result}`
  (orchestrator).

## Governed Git publish path

Status: in progress

Intent:
Tank sessions should not rely on an agent remembering to push, open a PR, or
watch checks. For selected GitHub repos, session startup creates a
Tank-owned `tank/session/<session-id>/<repo>` branch and draft PR. Every local
commit is auto-published through the Tank MCP `publish_current_head` path,
which owns the GitHub write token inside the session sidecar, records the
commit in the control-action ledger, and starts CI/mergeability watching. When
an agent opens a PR itself, `gh pr create` on a session branch is delegated to
the same governed sidecar path (it is not blocked) — see "Governed
`gh pr create`" below — so the agent uses normal `git`/`gh` verbs and never
holds a write credential.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- `repo-cloner` prepares the governed branch and draft PR before the repo is
  marked cloned.
- Service-principal session creation (`spawn_run_session`,
  `spawn_test_slot_session`, and the backing `/api/internal/sessions` route)
  defaults every repo-capable session into `restricted_git`, even when the
  agent omits `capabilities` or supplies only unrelated capabilities. Direct
  orchestration phase spokes do the same before calling the session manager.
  Agents that need privileged Git use the audited Git break-glass request/grant
  flow inside a governed session instead of spawning an unrestricted child
  session. The browser splash's human opt-out remains a deliberate
  create-time exception and is surfaced in the session bar as unrestricted Git.
- The user-facing sandbox has no normal direct-push path. The `pre-push` hook
  fails loudly, and the localhost GitHub MCP proxy denies write-capable
  `mint_clone_token` and file/PR write tools in restricted mode (a read-only
  `mint_clone_token` is allowed through so reads keep working — see "Restricted
  Session Read-Only Git Access"). The one write an agent routinely needs —
  opening its PR — is **delegated, not denied**: see "Governed `gh pr create`".
- The `post-commit` hook calls the Tank MCP publish tool rather than printing
  reminder-only guidance.
- **Governed `gh pr create`.** The in-pod `gh` wrapper detects `gh pr create`
  on a session branch and delegates it to a governed sidecar endpoint
  (`POST /create-session-pr` on the break-glass listener,
  `_handle_create_session_pr_wrapper`). That handler holds the GitHub credential,
  ensures an open **draft** PR for the branch (idempotent on an existing open PR
  and on GitHub's "already exists" 422; a `no commits between` create is a clean
  422, not a crash), recomputes the base from the repo's default branch, records
  the `github.pull_request.open` control-action event, sets the session PR link,
  and returns the PR URL to the wrapper. The agent uses the normal `gh pr create`
  verb and **never receives a write token** — the credential and policy stay in
  the sidecar, the same boundary as the read-only mint. This is the explicit-verb
  counterpart to commit-auto-publish (`git commit` publishes the branch,
  `gh pr create` opens its PR, both through the governed path) and it removes the
  post-merge stranding where a squash-merge deletes the branch and nothing
  re-opened a PR — the next `gh pr create` re-opens one. This transparent,
  no-grant path covers the session's **own** branch; a genuinely *parallel* PR
  (on a different branch) and PR-own writes (`gh pr edit/ready/comment`) go
  through the unified branch-lane break-glass grant, which brokers
  `gh pr create|edit|ready|comment` on its scoped branches via `/pr-write` (see
  "Branch Lane Grants" — this replaces the retired `create_pr_lane`). Every other
  `gh` verb (e.g. `gh pr merge`, issues) still falls through to the read-only /
  break-glass path unchanged.
- Commit publication, CI state, PR creation, and mergeability are durable
  `control_action_events`, so the UI can show PR/commit evidence after reload.
- Control-action ledger writes (`POST /api/internal/sessions/{id}/control-actions`)
  are authorized solely off the caller's **verified per-session service-principal
  subject** — `svc:tank:<id>` for production sessions, `svc:tank:slot-<n>-session-<id>`
  for test-slot sessions — which auth.romaine.life mints from the pod's
  `tank-operator/session-id` annotation (the same identity `nats-auth-callout`
  trusts). The subject is scope-bound to the backend (`localSessionScope`), so a
  production identity cannot write a slot session's ledger or vice versa. A
  caller-asserted request header is never an authorization input: requiring one
  (#1207) silently 403'd every already-running session pod and froze the ledger
  system-wide on 2026-06-16 — the `TankControlActionAuthorizationDenied` alert
  (`result="forbidden"`) and the `check-control-action-authz` guard exist so that
  regression cannot recur silently.
- The governed PR's title, body, and merge are mutated only through Tank MCP
  tools — `rename_current_session_pr`, `update_current_session_pr_body`, and
  `merge_current_session_pr` — each recorded as a `github.pull_request.*`
  control-action event (`tank_control_action_events_total`).
  `update_current_session_pr_body` exists because the `check-pr-body` workflow
  validates the Feature Contracts section in `pull_request.body`, which a
  restricted-git agent otherwise cannot edit without break-glass (a PR comment
  does not satisfy the check). The tool writes the full body via the sidecar's
  governed GitHub token and previews the `check-pr-body` result so the body can
  be corrected before CI runs.

Open hardening:
- Network/RBAC policy still needs a dedicated pass if the threat model includes
  a deliberately direct in-cluster call to `mcp-github`. The normal agent path
  is governed, but this capability is not a complete adversarial sandbox until
  direct service egress is denied or scoped to the sidecar.
- Break-glass does not advertise privileged Git options in the normal MCP tool
  list. The visible normal-mode surface is the narrow
  `request_git_break_glass` request tool, which records the request and returns
  a Tank approval URL (`/sessions/{id}?break_glass_request={event_id}`) without
  minting or revealing a token. Grants are stored as
  `github.break_glass.grant` control-action events with repo, operation, request
  event id, and TTL scope. Denials are stored as `github.break_glass.deny`.
  Workflow-file pushes are an explicit GitHub break-glass flavor:
  `request_git_break_glass(workflows=true)` records the extra `workflows`
  operation, and `mint_full_git_token(workflows=true)` is refused unless the
  active grant includes it. Branch/count-scoped break-glass git tokens are
  contents(+workflows)-write only, not full App-scope tokens, and stay on the
  governed (brokered, scope-enforced) write path rather than handing a raw token
  to the shell. An **unlimited-branch** grant is the wide escape hatch: it
  carries the `full_github_api` operation and mints the App's FULL permission set
  (pull requests, issues, merges) so the agent's `gh`/`git` get full raw GitHub
  API write automatically — see "Break-glass full GitHub API elevation (unlimited
  grants)" below.

  **Unified by Branch Lane Grants (see that capability below).** The branch-lane
  unification makes a single `request_git_break_glass` grant cover the whole life
  of a branch's work — push (fast-forward) **and** open + own its draft PR
  (`gh pr create|edit|ready|comment`) — for the scoped branches, brokered
  server-side. The agent asks once and a human approves once; after approval
  plain `git push` and `gh pr …` just work for the granted branches. The prior
  two-call activation ritual — "calling `request_git_break_glass` again activates
  a separate `tank-git-break-glass` MCP server and an agent may need to reload its
  MCP registry before it sees `mint_full_git_token` / `push_current_head`" — is
  **retired**: there is no second `request_*` call, no MCP-registry reload, and no
  agent-visible choice between `push_current_head` / `publish_current_head` / a
  separate PR-lane tool. The grant's scope (`named` / `count` / `unlimited`)
  bounds *which* branches may be touched, never *whether* push and PR-open work.
  Brokered use is still audited (`github.break_glass.*` control-action events);
  Tank's browser UI renders the approval panel and writes the grant or denial,
  and auth.romaine.life only authenticates the admin JWT that Tank verifies. When
  a grant is persisted, Tank writes the activation state itself so the privileged
  path is live with no follow-up agent action.
- Break-glass approval chip + panel (added 2026-06-14). A started
  `github.break_glass.request` with no unexpired grant for its repo is surfaced
  as a "chip": the composer pull-request button turns amber with an alert dot,
  and its popup menu exposes an "Approve break glass" entry linking to the Tank
  session deep link. The composer also renders `BreakGlassApprovalIndicator`;
  if the URL carries `break_glass_request`, the pane fetches that exact request
  from Tank (`GET /api/sessions/{id}/break-glass-requests/{event_id}`) so an
  admin can approve another user's request without relying on the owner's
  control-action list. Approve/deny POST to
  `/api/sessions/{id}/break-glass-requests/{event_id}/{approve|deny}`. The
  break-glass popover is portaled to `<body>` with fixed positioning so the
  composer input-group's `overflow: hidden` cannot clip it. The composer git
  chip, separately, is a single-click shortcut to the dedicated
  `/pull-requests` page (the complete, durable list — see "Durable session
  pull-request projection" below): one click opens the page — there is no PR
  popover — and the page is where Merge in Tank lives and where a PR explicitly
  linked via `set_pull_request_link` (test/rollout) is surfaced when it isn't in
  the session's own durable list.

## Durable session pull-request projection (git chip + /pull-requests page)

- **Status:** shipped.
- **Intent:** surface EVERY pull request a session has touched in the composer
  git chip and the dedicated `/pull-requests` page. Before this, the SPA
  re-derived the PR list in-browser from the newest-100 `/control-actions` feed
  (a recent-activity list mixing commit/CI/PR-lane/break-glass rows);
  `github.pull_request.open` rows are the *oldest* events in a session, so on a
  commit-heavy session early PRs silently fell off the back of the window even
  with only 2-3 PRs total. A page titled "PRs touched by this session" that
  quietly drops PRs is the user-trust failure this closes.
- **Durable source:** `sessions.pull_requests jsonb` on the session's own row
  (migration `0181`, backfilled from the complete `control_action_events` ledger
  in `0182`). One ref per PR (`{repo, number, url, action, status, state,
  updated_at}`), deduped by url with last-sighting-wins state. NULL/absent means
  "no PR touched" or a pre-column session; like `spawned_sessions` it is a
  display-only projection, never load-bearing — a malformed row decodes to nil
  rather than breaking the session list.
- **Write path:** every `github.pull_request.*` control action flows through the
  one internal endpoint `POST /api/internal/sessions/{id}/control-actions`
  (`handleInternalAppendControlAction`) — even `mcp-github`'s merge posts there.
  After the durable Append it calls `Manager.AppendSessionPullRequest`, which
  id-deduped-upserts the ref (by url) and republishes the row. **Best-effort:** a
  projection-write failure is logged + counted but never fails the
  control-action write — the ledger stays the source of truth.
- **Runtime behavior:** the append bumps `row_version` and `publishRow` fans the
  updated row out on the per-owner session-list NATS subject, so the chip count
  and the `/pull-requests` page converge over SSE without a manual refresh — the
  same path `spawned_sessions` uses. The SPA reads PRs ONLY from
  `session.pull_requests` (normalized at the store boundary); the old in-browser
  re-derivation from the capped control-action feed is **deleted, not kept as a
  fallback**. Page commits stay derived from the recent feed (a commit list is
  inherently recent-activity).
- **Observability:** `tank_session_pull_request_link_total{result=ok|error}`
  (bounded 2 series) — a rising error rate means sessions are silently losing PR
  links and the chip/page under-report, the same user-trust diagnostic class as
  `tank_session_spawn_link_total`.
- **Evidence:**
  - migration `0181`/`0182`; `sessionmodel.SessionPullRequestRef` /
    `DecodeSessionPullRequests` (`session_pull_requests_test.go`).
  - `sessionregistry.AppendSessionPullRequest` (id-deduped upsert, row_version
    bump) read back through `Get`/`List`/`fetchSessionRowsAfter` and carried on
    the snapshot `Info` + `RowPublisher` wire shapes.
  - `sessionPullRequestRefFromControlAction` filter + the
    `recordSessionPullRequestSighting` hook
    (`control_actions_pull_requests_test.go`).
  - frontend `normalizeSessionPullRequests` (`pullRequests.test.ts`),
    `pullRequestsFromDurable`, and the single-click `PullRequestMenuButton`
    shortcut to the `/pull-requests` page (`AgentGitActivityScreen`, which also
    hosts Merge in Tank).
- **Non-goal:** PR *write* authority (opening/merging PRs) is unrelated — that is
  the governed publish path / break-glass surface above. This projection is
  read-side display only.

## Branch Lane Grants

Status: complete (Stages 0–3 — unified model + brokering primitives, the atomic
cutover that retired the separate PR-lane surface, and observability). Full
design: [docs/branch-lane-grants.md](../../branch-lane-grants.md).

Intent:
Unify the two parallel governed-write mechanisms a restricted
(`TANK_RESTRICTED_GIT=true`) session used to expose — break-glass git
(`request_git_break_glass` → push) and PR lanes (`request_pr_lane` →
`create_pr_lane`) — into **one** grant. A break-glass git grant is permission to
do work on a branch (existing or not): create + push (fast-forward) it **and** open &
own its draft PR (edit title/body, mark ready, comment) through review. The
agent asks once (`request_git_break_glass`), a human approves once, and then
plain `git push` and `gh pr create|edit|ready|comment` just
work for the scoped branches — brokered server-side, scope-enforced, audited.
This closes the core defect of the old split: a branch-scoped break-glass grant
could push a branch but not open its PR (the PR half lived only in the separate
`create_pr_lane` mechanism the agent had no signal to use), so an agent taking
the least-privilege instinct got approved, pushed, and was silently stranded
with commits and no PR. Scope (`named` / `count` / `unlimited`) bounds *which*
branches a grant covers; it never bounds *whether* the grant works. `unlimited`
is simply the widest version — the whole-repo / full-GitHub-API escape hatch —
not a different mechanism and not a precondition for basic branch work.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- A single `request_git_break_glass` grant authorizes **push + PR open/own** for
  the branches in its scope. On approval Tank provisions the lane (creates or
  adopts the branch and opens its draft PR, reusing the old PR-lane branch+PR
  provisioning) and writes the activation state itself, so the privileged path
  is live with **no second `request_*` call and no MCP-registry reload**. The
  agent never names `push_current_head` / `publish_current_head` /
  `create_pr_lane` / a branch scope it has to reason about — it states *why*, the
  human decides *how much*, and `git`/`gh` do the rest.
- GitHub installation tokens are scoped by repo+permission, never by branch, so a
  branch-scoped grant cannot be honored by handing a raw token to the shell
  (that token would push every branch and edit every PR). Tank **brokers** the
  writes server-side and enforces the branch scope itself: `git push` goes
  through a governed push (create-if-absent, **fast-forward only**) for in-scope
  branches; `gh pr create|edit|ready|comment` route through a governed PR-write
  endpoint that resolves the PR to its head branch, verifies head ∈ lane scope,
  performs the write with Tank's credential, and audits it. No raw token and no
  denylist wall for the scoped case. **Force-push / history rewrite is not
  available on a scoped grant** (the brokered push is fast-forward only) — it
  requires an `unlimited` grant (native token); with squash-merge, scoped work
  forward-fixes or merges instead of rewriting. `unlimited` grants additionally
  surface the whole-repo GitHub API (the `full_github_api` / full-maintainer
  escape hatch).
- The control-action audit ledger is **kept**; every brokered operation records a
  `github.break_glass.*` event (`request` / `grant` / `push` / `pr_write`, plus
  `github.pull_request.open` for opens), with `operations` as the explicit
  audited capability set (`push_current_head`, `mint_full_git_token`, and
  `full_github_api` present only on `unlimited` grants). The
  `unlimited` / `full_api` whole-repo escape hatch, server-side branch-scope
  enforcement, `publish_current_head` normal session-branch auto-publish, and the
  restricted-mode raw-mcp-github write denylist are all unchanged.
- **Retired (no live route, tool, event, UI, or test may remain):** the
  `request_pr_lane` / `create_pr_lane` MCP tools + handlers + routes; the
  `github.pr_lane.*` event family and its reader/writer/auto-approval logic; the
  "scoped grant returns `{"active": false}` / `full_github_api` only on
  `unlimited`" split that made scoped grants useless for branch work; the
  separate PR-lane approval UI surface; and the old PR-lane / two-call-activation
  tests. Merge-to-base stays the separate, CI-gated step
  (`merge_current_session_pr`) — a branch lane gets work *to* review, not
  *through* it.

Acceptance evidence (the contract this capability must prove):
- A **branch-scoped** grant can **push the branch AND open + own its PR** — no
  `unlimited` required, and the agent is never silently stranded with commits and
  no PR.
- The agent reaches it in **one request + one approval**, with **no second
  `request_*` call and no MCP-registry reload**; `git push` / `gh pr …` are the
  only commands it runs, and no governed-tool names leak into its workflow.
- The retired `request_pr_lane` / `create_pr_lane` / `github.pr_lane.*` paths are
  gone from live code, tests, and UI, and the reintroduction guard
  `scripts/check-removed-pr-lane.mjs` (merged into
  `scripts/check-removed-chat-runtime.mjs`'s CI run) fails if
  `request_pr_lane`, `create_pr_lane`, `github.pr_lane.`, `_TANK_PR_LANE_TOOL`,
  `_TANK_CREATE_PR_LANE_TOOL`, or a `handle*PRLane` handler reappears in live
  source.
- Observability: `tank_break_glass_grant_total{result}`,
  `tank_break_glass_push_total{result}`, `tank_break_glass_pr_open_total{result}`,
  `tank_break_glass_pr_write_total{result}`, and
  `tank_break_glass_retired_path_total` (any increment means a retired `pr_lane`
  path was exercised and is a counted bug).

## Locked-by-default Azure MCP (break-glass)

The `azure-personal` MCP (Postgres `pg_query`/`pg_execute`, Key Vault secrets,
Cosmos, ARM/AKS, Entra/UAMI) is locked by default for every session. Normal
feature development never needs Azure access; obtaining it requires an
approved, time-bounded, audited break-glass grant.

Affected contracts:
- Session Lifecycle
- Observability

Enforcement is in the server we own, not the sidecar:
- `mcp-azure-personal` requires a valid auth.romaine.life JWT identifying the
  caller's session **and** an active azure break-glass grant. With no grant it
  returns an MCP-shaped JSON-RPC refusal — **not** a bare HTTP 403, which the
  Claude MCP SDK treats as an OAuth challenge (it falls into an
  `authenticate`/`complete_authentication` flow instead of failing cleanly) —
  including on a direct in-cluster call from the agent shell, not just the
  localhost MCP path.
- A sidecar gate cannot be the boundary here: every session pod shares the
  `claude-session` ServiceAccount (so RBAC cannot express per-session
  break-glass), and the `mcp-auth-proxy` sidecar shares the pod (IP + SA) with
  the agent container (so no NetworkPolicy can allow the sidecar but deny the
  agent). Only the server requiring an unforgeable per-session grant both
  revokes by default and grants per session. This is the canonical pattern:
  *first-party MCP servers check the Tank grant.* Git break-glass's in-sidecar
  `tank-git-break-glass` wrapper is the external-resource exception (github.com
  cannot be taught our grants, so Tank must mint a real GitHub token); a named
  follow-up folds git into this pattern.

Contract impact:
- The visible normal-mode surface is the narrow `request_azure_break_glass`
  tool (proxy-injected into the mcp-tank-operator surface, independent of
  restricted-git). For **every** session it records an
  `azure.break_glass.request` control-action event and returns the Tank approval
  deep-link shape (`/sessions/{id}?break_glass_request={event_id}`) without
  granting access or revealing a token. Approval is always an explicit human
  admin action — there is no auto-approval path for any session.
- Grants are stored as `azure.break_glass.grant` control-action events
  (`target_kind=azure_mcp`, `target_ref=azure-personal`) with TTL scope, in the
  same `control_action_events` ledger as git break-glass. They are not
  repo-scoped: a grant authorizes the whole azure-personal MCP for the session.
  Denials are stored as `azure.break_glass.deny`.
- After an admin approves in Tank, `mcp-azure-personal` reads the grant through
  `GET /api/internal/sessions/{id}/azure-break-glass/grant` (short-cached) on
  every list/call, so expiry re-locks automatically. Each privileged call is
  recorded as `azure.break_glass.use`. All three actions increment
  `tank_control_action_events_total`.
- The proxy forwards the auth.romaine.life service JWT (`X-Auth-Romaine-Token`)
  and the caller-session headers to port 9991 so the server can identify the
  session and look up its grant.
- **Client surfacing (mirrors git break-glass).** `azure-personal` is absent
  from the default session `.mcp.json` while locked, so the harness never
  connects to a locked server (no 403/OAuth noise). On an approved grant,
  `request_azure_break_glass` activates it back into `.mcp.json` / Codex / Claude
  settings (`_activate_azure_break_glass_mcp_config`) — the reconnect trigger
  that surfaces its tools, exactly how git break-glass surfaces
  `mint_full_git_token`. Enforcement (server-side grant check) and surfacing
  (`.mcp.json` activation) are separate concerns and the design needs both: a
  server-side gate alone is invisible to the harness, with no event to reconnect
  on after a grant.
- **Live surfacing (event-driven).** A mid-session SDK reconnect does *not*
  re-register an MCP server's tools, so on grant the orchestrator also POSTs
  `mcp-azure-personal`'s `/internal/grant-activated {session_id}`
  (`internal/azurepersonal`, fired from `enqueueAzureBreakGlassApprovalTurn` —
  best-effort + async, counter `tank_azure_grant_activated_total`). The stateful
  azure-personal server emits `notifications/tools/list_changed` on that session's
  live stream and the SDK auto-refreshes `tools/list`, so the tools surface with
  no reconnect or re-request. That endpoint is off the kube-rbac SA gate
  (kube-rbac-proxy `--ignore-paths`) and authorized by the orchestrator's
  auth.romaine.life service principal (`svc:tank-operator:orchestrator`), keeping
  it on the same identity plane as the rest of the ecosystem.

Open hardening:
- Hermes (the only other subject on `mcp-azure-personal`'s RoleBinding) was
  retired, so its subject was dropped from the RoleBinding and no exemption is
  configured. `breakGlass.exemptSubjects` remains for any future unattended
  automation that legitimately needs Azure without a per-session grant.
- Tank owns the public azure break-glass approval panel and approve/deny API;
  auth.romaine.life remains the identity provider for the admin JWT, not the
  approval workflow owner.

## Non-Restricted Session Full Git Access

Status: complete

Intent:
A non-restricted session (`TANK_RESTRICTED_GIT` false/unset) is a trusted
workspace and should have full, automatic git access — `git clone`/`fetch`/`pull`/
`push` to any repo the session's installation can reach, with no manual token
handling. Restricting git is an opt-in (`TANK_RESTRICTED_GIT=true`) governed
mode, not the default posture.

Affected contracts:
- Session Lifecycle
- Agent Runners

Contract impact:
- `install-agent-git-template.sh` is mode-aware. Restricted sessions install the
  governed hook templates (post-commit auto-publish + pre-push block) exactly as
  before. Non-restricted sessions install `git-credential-tank.sh` as the global
  git credential helper (`credential.helper` + `credential.useHttpPath=true`).
- `git-credential-tank.sh` mints a short-lived GitHub App token on each git op
  via the in-pod `mcp-github` MCP (`127.0.0.1:9992`), authenticated with the
  pod's projected `auth.romaine.life` service-account token, scoped per-repo via
  the request path, requesting the App's full permission set. It grants nothing
  the session cannot already mint through the MCP tool surface; it removes the
  manual step.
- `gh` is durable the same way: the session image bakes a `gh` wrapper at
  `/usr/local/bin/gh` (ahead of the apk `/usr/bin/gh` on PATH) that mints a fresh
  token (scoped to the `/workspace` repos plus any `--repo`/`-R` arg) and execs
  the real gh — so `gh` never needs a manual re-auth. The scope is mode-aware:
  full read/write in non-restricted mode, read-only in restricted mode (see
  "Restricted Session Read-Only Git Access" below). See
  `session-images/gh-tank-wrapper.sh`.
- `repo-cloner` keeps no local `credential.helper` override in either mode, so
  the clone inherits the global mode-aware helper (full in non-restricted,
  read-only in restricted). (An empty local `credential.helper` clears the helper
  list — it was the reason a non-restricted clone could not push, and would also
  disable restricted-mode reads, so it is no longer written.)
- The helper ships in the `tank-session-config` ConfigMap and is mounted into
  every session pod under `/opt/tank/session-config/`; it is wired up in both
  modes (mode-aware scope).

Evidence:
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  (`TestInstallAgentGitTemplateScriptInstallsCredentialHelperOutsideRestrictedGit`)
  covers non-restricted⇒helper-wired / no governed hooks, and
  (`TestInstallAgentGitTemplateScriptRunsUnderSh`) covers restricted⇒hooks **and**
  the read-only helper wired alongside them.
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  (`TestGitCredentialTankHelperMintsToken`) covers the helper's mode-aware mint
  request shape (full vs read-only), SSE reply parsing, and non-github bail.

## Restricted Session Read-Only Git Access

Status: complete (in-pod path; now reached only by sessions the wall does NOT front
— test slots, which have no per-slot wall, plus the rare wall-IP-unresolved
fallback. Every non-slot session — restricted or not — routes through "GitHub
Egress Proxy (the wall)" below, which observes and governs server-side and
supersedes this in-pod minting for them. Retiring this path for slots is tracked
under the egress lockdown work.)

Intent:
A restricted session (`TANK_RESTRICTED_GIT=true`) governs *writes* — pushes go
through `publish_current_head` / break-glass, not a raw token in the shell — but
it must not feel like *reads* are disabled. The agent already has full GitHub
read access through the GitHub read MCP tools and the pre-cloned `/workspace`
repos; restricted sessions additionally get automatic **read-only** git/`gh`
access so the familiar tools (`gh pr view`, `gh run view`, `git fetch`/`clone`)
just work and an agent does not wrongly conclude "read is disabled here". A
read-only token grants nothing the session can't already do via the read MCP
tools and cannot push, so it does not weaken the governed-write invariant.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- `git-credential-tank.sh` and the `gh` wrapper are **mode-aware**: in restricted
  mode they request `mint_clone_token` with no `write`/`workflows`/`full`, so
  mcp-github mints a `{contents: read, metadata: read}` token. Non-restricted
  mode is unchanged (full read/write).
- **Break-glass elevation (restricted mode).** Before the read-only mint, both
  wrappers first POST to the in-pod break-glass server's `POST /mint-git-token`
  endpoint (`:9999`, the grant source of truth). If an active, repo-covering,
  **unlimited-branch** break-glass grant exists, that endpoint mints the App's
  FULL permission set (`full=true`) and audits the use, so the wrappers hand the
  shell a full token and `gh pr edit`/`ready`/merge, issues, and `git push`
  "just work" automatically while the grant is live. With no qualifying grant
  the endpoint returns `{"active": false}` and the wrappers keep the read-only
  default unchanged. See "Break-glass full GitHub API elevation (unlimited
  grants)" below.
  **Retired by Branch Lane Grants (see below):** the description that a
  *branch-scoped* grant stays read-only for raw `git`/`gh` and only an
  unlimited-branch grant lets a session push or run `gh pr edit|ready` no longer
  holds. Under the unified branch lane, a branch-scoped grant pushes
  (fast-forward) and opens + owns its PR for the granted branches through Tank's
  server-side brokering — no raw token required for the scoped case. `unlimited`
  is reserved for the whole-repo / full raw GitHub API need, not as the
  precondition for branch work. With no grant at all, restricted sessions remain
  read-only exactly as described here.
- **Fail loud, never silent (elevation).** The wrappers treat only a clean
  `{"active": true, "token": …}` (elevate) or `{"active": false}` over HTTP 200
  (quiet, expected no-grant) as recognized answers. Any other shape — a JSON-RPC
  error such as `{"error": {"code": -32600, "message": "invalid MCP request"}}`
  (what the `:9999` MCP catch-all returns when the `mcp-auth-proxy` sidecar
  predates the `/mint-git-token` route, i.e. an image/version skew), an HTTP
  error, a timeout, or any unrecognized body — is reported to **stderr** before
  the read-only fallback, instead of being silently collapsed to read-only. A
  silent downgrade here is what made the sidecar-skew regression undiagnosable
  (an active grant produced a read-only token with no signal, so `gh pr close`
  failed with `Resource not accessible by integration`); the silent collapse is
  treated as part of the bug, not an acceptable fallback. The break-glass
  curl also uses a timeout with headroom (`-m 8`) so the cold full-mint path
  (Tank grant lookup + GitHub App mint) is not misread as "no grant".
- `install-agent-git-template.sh` installs the credential helper in **both**
  modes; the elevated cluster kubeconfig stays non-restricted-only.
- `repo-cloner.sh` no longer writes an empty local `credential.helper` in
  restricted mode, so cloned repos inherit the global read-only helper.
- The mcp-auth-proxy keeps `mint_clone_token` and the file/PR write tools on the
  restricted-mode denylist, but **allows a read-only `mint_clone_token`** through
  to mcp-github (`_is_read_only_clone_token_request`); `write`/`workflows`/`full`
  mints are still blocked with the governed-path error. This carve-out is the
  only thing that lets the mode-aware helper/wrapper mint at all.
- Writes stay governed regardless: the `pre-push` hook still fails direct pushes
  and a read-only token cannot push even if the hook is bypassed.
- Observability:
  `tank_mcp_auth_proxy_github_write_tool_decision_total{tool,decision}` counts
  `allowed_read_only` vs `blocked` decisions on the denylist.

Evidence:
- `claude-container/mcp-auth-proxy/tests/test_server.py`
  (`test_read_only_mint_clone_token_is_forwarded_in_restricted_mode`,
  `test_write_mint_clone_token_is_blocked_in_restricted_mode`,
  `test_is_read_only_clone_token_request`).
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  (`TestGitCredentialTankHelperMintsToken`, `TestGhTankWrapperMintsModeAwareToken`
  assert the read-only request shape in restricted mode;
  `TestInstallAgentGitTemplateScriptRunsUnderSh` asserts the helper installs in
  restricted mode).
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  (`TestGitCredentialTankHelperBreakGlassElevation`,
  `TestGhTankWrapperBreakGlassElevation`) cover all three elevation cases against
  a live mock break-glass server: active grant → full token (no read-only
  fallback), no grant → quiet read-only fallback, and `error mint response fails
  loud and falls back to read-only` (the JSON-RPC `invalid MCP request` shape →
  stderr diagnostic + read-only fallback, the fail-loud guard).

## GitHub Egress Proxy (the wall): server-side governed GitHub access

Status: in progress (per-request mint governance, the REST/GraphQL write-hole
closure, gh REST + GraphQL, break-glass, asset hosts, and — as of the
observation/restriction decoupling — universal observation of EVERY non-slot
session have shipped and are enforced by the Calico NetworkPolicy that denies
direct GitHub egress; the remaining scope is bringing test slots under a wall and
retiring the in-pod credential-helper / `gh`-wrapper mint path — "Restricted
Session Read-Only Git Access" above — both tracked under the egress lockdown work).

Intent:
EVERY session must reach GitHub through ONE governed outbound chokepoint — the
agent never holds a GitHub token, and every PR open/merge is recorded — because
that observation is not surveillance: it is the data source the product runs on
(the durable PR projection → git chip / `/pull-requests` page / Merge-in-Tank, and
the CI/mergeability wake loop). A session the wall does not see is a session the
app cannot drive. So OBSERVATION is universal — restricted AND unrestricted
sessions route through the wall — while RESTRICTION (least-privilege mint, branch
lane, merge-deny) is scoped to `restricted_git` sessions. An unrestricted session
is minted FULL and confined by nothing, but is still routed, observed, and has its
token injected server-side. The egress proxy ("the wall") is an Envoy listener
that TLS-terminates `github.com` / `api.github.com` / the asset hosts (leaf cert
signed by the cluster `claude-oauth-ca-issuer`) plus a Python ext_proc sidecar —
the pure, unit-tested `github_governor` core and the `github_ext_proc` IO shell.
Per request it reads the pod's relayed auth.romaine.life service JWT, fetches the
session's egress context (repos + restricted, fail-closed to restricted), picks
the mint scope (least-privilege for restricted, full for unrestricted), RELAYS the
JWT to mcp-github's `mint_clone_token` (actor→installation routing mints the
owner's App token), overwrites `Authorization`, and records `github.*` control
actions. The agent's JWT never reaches GitHub and the minted App token never
reaches the agent. This is the server-side evolution of the in-pod "Restricted
Session Read-Only Git Access" model, which it supersedes for non-slot sessions
(test slots still exclude the wall and keep the in-pod path during migration).

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- **Observation is universal; restriction is scoped.** EVERY session whose wall IP
  resolves routes through the wall — the `egressProxyGit` gate is no longer welded to
  `restricted_git` — so the wall records its PRs and injects its token regardless of
  mode. The wall fetches each session's restricted status from Tank
  (`GET /api/internal/sessions/{id}/egress-context`, cached, fail-closed to
  restricted) and mints least-privilege for restricted sessions, FULL for
  unrestricted ones; the merge-deny and branch-lane checks run for restricted sessions
  only. Previously observation was welded onto `restricted_git`, so unrestricted
  sessions bypassed the wall entirely and were invisible to the projection/CI loop the
  product runs on — that was the bug this decoupling fixes.
- **Per-request least privilege** (`evaluate_policy`, restricted sessions). The agent never holds the
  token, but the SCOPE still matters because a coarse token would let the agent
  reach the whole repo through the API, bypassing the lane/merge governance that
  only inspects the git transport. So write capability is granted ONLY for the
  two governed write paths: git push (receive-pack — BOTH the `info/refs?service=
  git-receive-pack` advertisement leg and the `git-receive-pack` upload leg, or
  the push 403s before it starts) mints `contents:write`, lane-confined in the
  body phase; PR open (`POST /pulls`) mints the App's full set (needs
  `pull_requests:write`) and is the one REST write the wall records. EVERYTHING
  else — clone/fetch and the entire REST + GraphQL surface, reads and ungoverned
  writes alike — mints READ.
- **Read is broad by GitHub's design, write is withheld.** A `{contents:read,
  metadata:read}` installation token reads pulls/actions/issues/checks AND serves
  GraphQL queries, but cannot mutate. So GitHub itself 403s an ungoverned REST
  write and refuses a GraphQL mutation: the read mint IS the enforcement, not an
  endpoint denylist. This is the REST/GraphQL analog of the git lane check (git
  governed by inspecting refs; the API governed by withholding write capability),
  and it closes the ungoverned REST API write hole.
- **`gh` works** (the regression closer). `gh` authenticates with the `token`
  scheme (not `Bearer`); the wall extracts the pod credential from `token`,
  `Bearer`, and Basic `x-access-token`. gh REST (`gh api`, `gh run list`) mints
  read scoped to the URL's repo. gh GraphQL (`gh pr list` / `view` / `status`)
  POSTs to `/graphql`, which carries no repo in the URL, so the wall scopes a
  READ mint to the session's durable create-time repos (`sessions.repos`, read
  from `GET /api/internal/sessions/{id}/repos` and cached per session). A GraphQL
  mutation gets that read token and is refused, so writes stay governed.
- **Break-glass through the wall.** An out-of-lane push or a would-be-denied API
  write (merge, raw REST write) is allowed only when an active, repo-covering
  break-glass grant covers it, read through the SAME `git-break-glass/grant`
  endpoint the in-pod minter used (so the wall and the minter never diverge). An
  `unlimited` grant (advertising `full_github_api`) elevates a read REST write
  back to a full token and unlocks merge; every grant-covered out-of-lane push is
  recorded as `github.break_glass.push` so a `count` grant's budget is tallied.
- **100% capture.** A raw `gh` / `git` PR open or merge through the wall records
  the same `github.pull_request.*` control action an mcp-github PR would, so the
  durable PR projection captures agent PRs the agent cannot skip.
- **Asset hosts.** `codeload.github.com` and `*.githubusercontent.com` are
  fronted by the wall with `Authorization` / `Cookie` stripped (public asset
  fetches need no mint), so clone and release-asset paths work end to end.
- **Fail-soft during cutover.** A request with no parseable credential, or whose
  credential does not exchange to a session identity, passes through untouched;
  enforcement that unidentified egress is rejected is the NetworkPolicy layer.

Evidence:
- `api-proxy/tests/test_github_governor.py` — the pure governor contract:
  identity parse, per-request mint scope (push both legs → write, PR open → full,
  REST/GraphQL → read, every ungoverned REST write → read), `is_graphql`,
  `is_rest_write` excluding `/graphql`, `session_repos_url`,
  `parse_session_repos_response` (fail-closed), multi-repo mint payload, the
  `token`/`Bearer`/Basic auth schemes, and break-glass grant matching.
- `backend-go/cmd/tank-operator/handlers_internal_test.go`
  (`TestHandleInternalSessionReposReturnsDurableRepos`,
  `TestHandleInternalSessionReposNotFoundForUnknownSession`) — the session-repos
  endpoint returns the durable create-time set and 404s unknown sessions.
- The IO shell is validated by live deploy (it injects real tokens and walls real
  egress, which a unit test cannot prove): each stage is validated in a restricted
  session against the deployed wall — gh REST + GraphQL succeed, an ungoverned
  REST write and a GraphQL mutation are refused, in-lane push succeeds and
  out-of-lane is denied absent a grant.

## Break-glass full GitHub API elevation (unlimited grants)

Status: complete

Intent:
"Unlimited break-glass should give EVERY GitHub permission." An UNLIMITED
break-glass grant (`repo_scope: all_repos`, `branch_scope: unlimited`,
admin-approved + TTL-bounded) is the explicit "this session needs to operate as
a full maintainer" escape hatch. Before this capability it only widened the
repo/branch reach of `git push`; the permission ceiling stayed hardcoded to
`contents`, so the agent still could not do `gh pr edit`/`ready`/merge, issue,
or other GitHub API writes, and `gh`/`git` never consulted the grant at all.
This capability closes that gap: while an unlimited grant is active, the
session's `gh` and `git` operate with the GitHub App's FULL permission set
automatically, with no manual `GH_TOKEN` juggling. Branch/count-scoped grants
are unchanged — they stay least-privilege on the governed push path.

This is a deliberate, admin-consented blast-radius widening: a full raw GitHub
App token bypasses the governed PR ledger (the `rename_current_session_pr` /
`update_current_session_pr_body` / `merge_current_session_pr` Tank tools and
their `github.pull_request.*` control-action records). It is therefore gated on
an explicit, audited, TTL-bounded grant and surfaced in the approval prompt,
the admin panel, and the audit ledger so the approving human accepts it
knowingly.

Affected contracts:
- Session Lifecycle
- Observability

Enforcement / source of truth:
- The grant's `full_github_api` operation is the authorization signal. It is
  **bound to the branch scope, not the request**: the orchestrator's
  `normalizeBreakGlassOperations` force-ADDs it for unlimited-branch grants and
  force-STRIPs it for branch/count-scoped grants, so a request cannot smuggle
  full-API write into a least-privilege grant and an unlimited grant always
  advertises it (`backend-go/cmd/tank-operator/control_actions.go`).
- The break-glass MCP server (`mcp-auth-proxy` sidecar, `:9999`) is the single
  source of truth the wrappers consult. It already performs the durable Tank
  grant lookup (`GET /api/internal/sessions/{id}/git-break-glass/grant`) for
  `mint_full_git_token` / `push_current_head`; the wrappers reach it at the
  dedicated `POST /mint-git-token` route. That route deliberately does NOT
  require the agent-facing MCP activation marker — the durable, admin-approved,
  TTL-bounded grant is the authorization; the marker only governs whether the
  agent-facing privileged MCP tools are surfaced. Expiry re-locks automatically
  because the grant is re-looked-up on every mint.
- **Hot-path cost/scale.** The wrapper mint runs on every restricted git/`gh`
  op (overwhelmingly the no-grant case), so the `:9999` server caches the grant
  lookup RESULT (not a token) with a ~10s TTL, keyed by repo. A burst of ops in
  a no-grant session makes ONE Tank call, not N (negative results are cached);
  the durable grant stays the authz. A cached POSITIVE is never served past the
  grant's own `expires_at`, so expiry re-locks within ~TTL. The wrapper scripts
  also use a short (`-m 3`) timeout on the optional `:9999` pre-check so a slow
  or unreachable Tank falls back to the read-only mint fast instead of stalling
  git; the read-only `mint_clone_token` call keeps its normal timeout.

Contract impact:
- **Clamp 1 — `mint_full_git_token`.** The break-glass MCP mint
  (`_handle_break_glass_mint_token`) now mints with `full=true` for the only
  case it can reach: an unlimited-branch grant (the branch-restricted refusal
  fires first). The raw break-glass git token therefore carries the App's full
  permission set, not just contents(+workflows).
- **Clamps 2 & 3 — the wrappers.** `gh-tank-wrapper.sh` and
  `git-credential-tank.sh` are break-glass-aware in restricted mode: they
  consult `:9999` first and use a full token when an active unlimited grant
  covers the repo(s); otherwise they keep the read-only `mint_clone_token`
  default. No active grant → restricted sessions stay READ-ONLY exactly as
  before.
- **Governance + audit.** The grant operations carry `full_github_api`; the
  agent approval turn (`gitBreakGlassApprovalPrompt`/`DisplayText`) and the
  admin grant panel (`frontend/src/AdminBreakGlassPanel.tsx`) both state "full
  GitHub API write … bypasses the governed PR ledger"; every full-API mint
  (raw MCP path and wrapper path) is recorded as a `github.break_glass.token`
  control-action use via `_record_break_glass_use` (the wrapper path tags
  `channel: wrapper`, `full_github_api: true`), counted by
  `tank_control_action_events_total`.
- **Least privilege preserved.** Branch/count-scoped grants never get
  `full_github_api`, never mint a raw full token, and stay on the governed
  `publish_current_head` / `push_current_head` path.

Deploy caveat:
- This changes the session image (wrappers), the `mcp-auth-proxy` sidecar, and
  the orchestrator, so it only takes effect for NEW sessions after the images
  rebuild + deploy. It does NOT retroactively elevate an already-running
  session.

Evidence:
- `claude-container/mcp-auth-proxy/tests/test_server.py`
  (`test_break_glass_mint_token_unlimited_mints_full_github_api_scope`,
  `test_break_glass_mint_token_refuses_branch_restricted_grant`,
  `test_break_glass_operations_appends_full_api_only_when_unlimited`,
  `test_break_glass_wrapper_mint_full_token_for_active_unlimited_grant`,
  `test_break_glass_wrapper_mint_inactive_without_grant`,
  `test_break_glass_wrapper_mint_inactive_for_branch_restricted_grant`,
  `test_break_glass_wrapper_mint_inactive_when_repo_outside_grant_scope`,
  `test_break_glass_wrapper_mint_negative_result_is_cached`,
  `test_active_break_glass_grant_cached_serves_positive_within_ttl`,
  `test_active_break_glass_grant_cached_does_not_serve_positive_past_expiry`).
- `backend-go/cmd/tank-operator/control_actions_test.go`
  (`TestNormalizeBreakGlassOperationsBindsFullAPIToUnlimitedBranch`,
  `TestGitBreakGlassApprovalTextReflectsFullAPIForUnlimited`, and the updated
  grant/approve tests asserting `full_github_api` for unlimited and its absence
  for count-scoped grants).
- `backend-go/cmd/tank-operator/session_pod_bootstrap_script_test.go`
  (`TestGitCredentialTankHelperBreakGlassElevation`,
  `TestGhTankWrapperBreakGlassElevation`): the wrappers mint full under an
  active grant and fall back to read-only without one.

## Non-Restricted Session Read-Only DB Access

Status: complete

Intent:
Give non-restricted (trusted) sessions arbitrary READ-ONLY SQL against the
tank-operator Postgres DB for diagnostics (the `session_events` ledger,
`profiles`, `session_registry`, `control_action_events`, …) — the durable-ledger
query path `docs/diagnostic-discipline.md` calls for — without putting DB
credentials in the session pod.

Affected contracts:
- Session Lifecycle
- Agent Runners

Contract impact:
- The mcp-auth-proxy injects a `query_tank_db` MCP tool into the
  mcp-tank-operator surface **only for non-restricted sessions**
  (`not RESTRICTED_GIT_ENABLED`). It calls Tank's internal
  `POST /api/internal/sessions/{id}/db-read-query`.
- The endpoint runs the SQL under the orchestrator pool in a **read-only
  transaction** with a `statement_timeout` and a row cap, and refuses
  restricted-git sessions (`podRestrictedGit`). Writes/DDL are rejected by the
  read-only tx; the Flexible-Server admin is not a filesystem superuser, so the
  blast radius is "read the app's own data" — acceptable for the trusted owner's
  non-restricted sessions, and unavailable to restricted/test sessions.
- No DB credential ever lands in a session pod; the orchestrator (the DB's AAD
  admin) proxies the read. (Full `psql` CLI with a dedicated read-only role +
  KV password is a heavier optional follow-up.)

Evidence:
- `claude-container/mcp-auth-proxy/tests/test_server.py`
  (`test_query_tank_db_tool_injected_only_for_non_restricted`,
  `test_handle_query_tank_db_tool_runs_read_query`).
- `backend-go/cmd/tank-operator/handlers_db_read_query_test.go`
  (`TestDBReadQuery_RestrictedRefused`, `TestDBReadQuery_NonRestrictedRequiresPool`).

## Orchestrate Hub Launch (Self-Grant + Durable Spoke Config)

Status: in progress

Intent:
Let a user turn their own GUI chat session into the hub of a spoke fleet: one
confirm persists the fleet's run config, grants the hub full git authority, and
kicks off the `/orchestrate` skill — so the session can delegate task slices to
fresh spoke sessions that report back to the hub instead of the user. See the
Orchestrate feature folder and `app-chrome` for the surface that drives it.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- New durable `sessions.spoke_config jsonb` column (migration `0180`) threaded
  through the registry write/read, `SessionRecord`, `Info`, the `RowPublisher`
  snapshot, and the session-list-events read — the same full path
  `rollout_state`/`spawned_sessions` ride, so the hub flag survives reload and
  rides SSE. It is NULL until the launch endpoint sets it; there is no
  pod-annotation source (hub state is server-owned, not runtime-reported).
- `POST /api/sessions/{id}/orchestrate` is the launch endpoint. Gating is the
  **human session owner only**: service principals are rejected outright (`403`
  — this is a human-initiated full-power git grant, not a service path), and
  ownership is the write-class per-owner `GetByOwner` gate, **not** the
  admin-liftable read gate, so an admin cannot launch orchestrate on another
  user's session. The hub must itself be an SDK chat (GUI) session so a spoke's
  `send_prompt` ping-back wakes it as a new turn.
- The spoke config (`provider` / `surface` (gui|cli) / `model` / `effort`, no
  repo) is validated through the same provider allowlists as session create —
  rejections increment `tank_session_run_config_rejected_total{surface="orchestrate"}`.
  `provider`+`surface` derive the concrete spoke session mode the hub passes to
  `spawn_run_session`.
- On confirm the endpoint, in order: persists `spoke_config`; **self-grants git
  break-glass with no approval round-trip** — `all_repos` / `unlimited` branch /
  full ops (`mint_full_git_token`+`push_current_head`+`workflows`+`full_github_api`)
  / 24h — reusing `appendGitBreakGlassGrant` with a `source: "orchestrate-self-grant"`
  audit marker on the durable `github.break_glass.grant` control-action event;
  and enqueues the `/orchestrate` kickoff turn (spoke config, the hub's own id
  for ping-backs, break-glass status + expiry, plan-first reminder) over the
  exact `enqueueSDKTurn` path a spoke ping-back later uses.
- The 24h grant ceiling is hard (the break-glass writer clamps to 86400s): a run
  longer than 24h needs a human re-confirm (re-POST), which appends a fresh
  grant — orchestrate invents no renewal model. `all_repos` full-API suspends the
  governed PR flow for the grant's life; the confirm surface states that blast
  radius.

Evidence:
- `backend-go/cmd/tank-operator/handlers_orchestrate.go` (handler + spoke-config
  validation + kickoff prompt) with
  `backend-go/cmd/tank-operator/orchestrate_launch_test.go` covering the
  service-reject, non-owner `404`, non-GUI-hub reject, invalid-spoke-config,
  the full-power grant shape + `orchestrate-self-grant` marker + persisted
  spoke_config + single kickoff command, and re-confirm-appends-grant paths.
- Column threading: `backend-go/internal/pgstore/migrations.go` (`0180`),
  `internal/sessionmodel/sessionmodel.go`, `internal/sessionregistry/registry.go`
  + `write.go` (`SetSpokeConfig`), `internal/sessions/sessions.go` + `manager.go`,
  `internal/sessioncontroller/row_publisher.go`, and
  `cmd/tank-operator/handlers_session_list_events.go`, with
  `internal/sessionmodel/spoke_config_test.go`.
- The skill the kickoff turn loads:
  `k8s/session-config/skills/common/orchestrate/SKILL.md`.
- Metrics: `tank_orchestrate_launch_total{result}` (launch outcomes) and the
  reused `tank_control_action_events_total` (the self-grant) +
  `tank_session_run_config_rejected_total{surface="orchestrate"}`.
