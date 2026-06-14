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
boot branch-built session-runner code, not just hot-swap the already-running
pods. The slot's orchestrator stamps a per-scope override image onto new pods
the same way production stamps its chart-pinned image — no runtime overlay, full
fidelity. Production is never affected.

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
## Governed Git publish path

Status: in progress

Intent:
Tank sessions should not rely on an agent remembering to push, open a PR, or
watch checks. For selected GitHub repos, session startup creates a
Tank-owned `tank/session/<session-id>/<repo>` branch and draft PR. Every local
commit is auto-published through the Tank MCP `publish_current_head` path,
which owns the GitHub write token inside the session sidecar, records the
commit in the control-action ledger, and starts CI/mergeability watching.

Affected contracts:
- Session Lifecycle
- Agent Runners
- Observability

Contract impact:
- `repo-cloner` prepares the governed branch and draft PR before the repo is
  marked cloned.
- The user-facing sandbox has no normal direct-push path. The `pre-push` hook
  fails loudly, and the localhost GitHub MCP proxy denies raw write-token and
  file/PR write tools in normal mode.
- The `post-commit` hook calls the Tank MCP publish tool rather than printing
  reminder-only guidance.
- Commit publication, CI state, PR creation, and mergeability are durable
  `control_action_events`, so the UI can show PR/commit evidence after reload.

Open hardening:
- Network/RBAC policy still needs a dedicated pass if the threat model includes
  a deliberately direct in-cluster call to `mcp-github`. The normal agent path
  is governed, but this capability is not a complete adversarial sandbox until
  direct service egress is denied or scoped to the sidecar.
- Break-glass does not advertise privileged Git options in the normal MCP tool
  list. The visible normal-mode surface is the narrow
  `request_git_break_glass` request tool, which records the request and returns
  an auth.romaine.life approval URL without minting or revealing a token. Grants
  are stored as `github.break_glass.grant` control-action events with repo,
  operation, and TTL scope. Once an active grant exists, calling
  `request_git_break_glass` again activates a separate `tank-git-break-glass`
  MCP server for the session/repo and writes runtime MCP config for Codex and
  Claude. That privileged server lists no tools before activation, rechecks the
  grant on every list/call, and records token/push use as
  `github.break_glass.token` or `github.break_glass.push`. The auth.romaine.life
  console is expected to approve by calling Tank's internal grant endpoint;
  that callback does not exist in the auth app yet, so the agent-facing approval
  URL is a dead end in practice.
- In-app break-glass approval (added 2026-06-14). The session owner approves a
  pending break-glass request straight from the Tank UI instead of the dead
  approval URL. A started `github.break_glass.request` with no unexpired grant
  for its repo is surfaced as a "chip": the composer pull-request button turns
  amber with an alert dot, and its popup menu exposes an "Approve break glass"
  action. That action POSTs `/api/sessions/{session_id}/git-break-glass/approve`
  (human-auth route, `requireAuth`), which resolves the pending request from the
  control-action ledger and writes the same `github.break_glass.grant` event the
  internal endpoint would — scoped to the session owner's ledger partition so
  the session's `request_git_break_glass` grant lookup finds it on the next
  call. The pending detection (`pendingBreakGlassRequests`, frontend) and the
  grant write (`appendGitBreakGlassGrant`, shared by the internal and UI
  endpoints) keep the durable grant shape identical regardless of approver.
  Operators can still create the same grant directly via the internal endpoint.
  The same pull-request popup also separates the two PR links the UI already
  tracked: the latest PR the agent opened (control-action git activity) and the
  PR explicitly linked via `set_pull_request_link`.
