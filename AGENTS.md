# tank-operator

Web frontend over a thin K8s orchestrator that spawns ephemeral session pods on demand. The launcher creates GUI chat and terminal agent sessions backed by Kubernetes pods. See [README.md](README.md) for the layout.

## Product position

**The pod is the product**, not any specific agent UI. Pods ship pre-configured with private networking + an MCP gateway + (eventually) docs RAG; the user runs whatever agent they prefer inside it (Claude Code today because it's common, with Codex plus ordinary tools like aider/`vim` also supported). North Star is "don't make users learn our system" â€” they bring their existing tooling into a pre-baked environment. Closest analog is Coder, with MCP-aware envs as the wedge no one else ships.

**Strategic purpose â€” LLMs-as-a-service.** Beyond personal use, the platform is designed to hand a hosted dev session to other people. Near-term: collaborate with friends without their own Claude Max subscription (gated by `ALLOWED_EMAIL`). Long-term: enterprise multi-tenant LLM delivery â€” same shape, with billing/quotas/tenancy. Design preserves this: per-session pod isolation, SA-token-scoped MCP access, no shared filesystem state, email-allowlist auth that can grow into roles/orgs.

**Why not Anthropic Managed Agents (shipped 2026-04-08)?** Covers most of the env-spec primitives but is (a) Claude-only â€” breaks the agent-agnostic position, (b) API-billed per-token â€” breaks the "share my Max subscription with friends" economics, (c) explicitly forbids shipping a Claude-Code-styled UI. Tank-operator's defensible edges are the subscription-token sharing model and the agent-agnostic shape; the runtime layer itself is now commoditized.

## Stack

Go orchestrator (`backend-go/`), Vite + React frontend (`frontend/`), multi-stage Dockerfile, Helm chart in `k8s/` synced by ArgoCD. Two namespaces: `tank-operator` (long-lived orchestrator Deployment) and `tank-operator-sessions` (ephemeral session Pods).

The orchestrator was a Python FastAPI + kubernetes-asyncio app until 2026-05-11 (#373), when it was rewritten in Go. The repo has been mostly compiled-language since (Go orchestrator, Go session runner under construction). The api-proxy ext_proc is still Python (small, single-purpose).

## Container build verification

Session pods intentionally do not ship Docker or a container runtime. Do not
report missing local Docker as a blocker. Run available repo checks first
(`go test`, `npm test`, `helm template`, etc.). The normal container
build gate is PR CI: `.github/workflows/docker-build-check.yaml` builds every
repo-owned image. For normal same-repo PRs it logs into ACR, checks for an
existing fingerprinted proof image, and pushes the missing proof image to ACR;
those proof images prime later merge/deploy work. If an image-packaging change
needs feedback before a PR is ready, manually dispatch that workflow with
`git_ref`.

Do not confuse PR proof images with deploy image publication. Release/deploy
workflows are the paths that publish chart-consumed production tags. For Tank
session pods specifically, `.github/workflows/session-images-build.yml`
publishes the `claude-container` and `codex-container` tags used by
session-image repointing and by the main-branch chart bump.

## Quality timeframe

Read [docs/quality-timeframes.md](docs/quality-timeframes.md) before planning
substantial work. This repo follows the long-term, heavy-solution operating
mode described there. Do not ship a light or medium version when the complete
architecture, hardening,
observability, and migration cleanup are already understood. If the complete
solution must be split across PRs, write the full plan first and keep every
stage coherent by itself.

Read [docs/migration-policy.md](docs/migration-policy.md) before any migration
or cleanup work. Read
[docs/product-inspirations.md](docs/product-inspirations.md) when making
product or architecture decisions.

Read [docs/features/README.md](docs/features/README.md) and the relevant
feature contract before substantial work on a core feature. The policy docs set
the repo-wide quality bar; the feature contracts translate that bar into
feature-specific invariants and acceptance evidence. A PR that changes a core
feature should name the affected contract and explain how the implementation
proves the contract still holds.

Before opening a PR, fill out the Feature Contracts section from the PR
template. If the PR touches a contracted feature, name the affected contracts
and provide concrete evidence for each one. "Tests pass" is not enough unless
the test directly proves the contract invariant. Do not mark a PR ready, ask
for merge, or merge it yourself when the Feature Contracts section is missing
or incomplete.

In restricted Git mode (`TANK_RESTRICTED_GIT=true`) the PR body is not directly
editable through the raw GitHub MCP tools. Set it — including the filled Feature
Contracts section — with the Tank MCP `update_current_session_pr_body` tool,
which writes the body of this session's governed PR and previews whether the
`check-pr-body` workflow will pass. A PR comment does not satisfy that check; it
only reads `pull_request.body`. Do not file a break-glass request just to edit
the PR body.

Always wait for all CI checks/tests to complete successfully on GitHub before merging a PR. Merging a PR before checks finish will break the image build/tagging workflow on main. This includes the PR body checklist check (check-pr-body) — do not bypass or ignore these failures.

Tank session repos use a governed Git flow. `repo-cloner` creates a
Tank-owned `tank/session/<session-id>/<repo>` branch and draft PR at session
start. The post-commit hook auto-publishes every local commit through the Tank
MCP `publish_current_head` tool so Tank can record the commit and watch
GitHub CI/mergeability from the first SHA. Direct `git push` is blocked in
normal mode; use the Tank MCP publish tool to retry a failed auto-publish.
The governed PR itself is mutated only through Tank MCP tools, each recorded in
the control-action ledger: `rename_current_session_pr` (title),
`update_current_session_pr_body` (body/description), and
`merge_current_session_pr` (merge after CI/mergeability verification).
For ad-hoc existing worktrees that predate the template, run:

```sh
scripts/install-agent-post-commit-reminder.sh
```

The hook templates live at `.githooks/post-commit` and `.githooks/pre-push`.
The post-commit hook is an auto-publish trigger, not a reminder. The pre-push
hook intentionally fails direct pushes so GitHub write credentials stay inside
Tank-controlled MCP/server paths. CI and mergeability failures must be treated
as unfinished work unless explicitly called out in the final handoff. If the
governed path is insufficient, call the Tank MCP `request_git_break_glass` tool.
Normal mode only returns an auth.romaine.life approval URL. After a short-lived
grant exists, calling the request tool again activates the separate
`tank-git-break-glass` MCP server for that session/repo; an existing agent may
need to reload its MCP registry before it sees `mint_full_git_token` or
`push_current_head`.

When adding or substantially changing named product behavior, check whether the
relevant feature folder needs a `capabilities.md` entry. Capability ledgers are
for behavior a future agent should be able to name, audit, or retire without
reconstructing the intent from chat history.

Read [docs/diagnostic-discipline.md](docs/diagnostic-discipline.md) before
investigating any bug or incident on this repo. The other three docs above
describe the quality bar for *writing* code; this one describes how to
*investigate* a system so the writing has the right target. Specifically:
when a user reports a behavior claim, query the durable ledger
(`session_events` in Postgres for run state) before looking at logs or
metrics. Logs are loud; the durable ledger is quiet â€” but it's the
contract. Skipping this step is how romaine-life/tank-operator#532 got its
prior session's diagnosis pointed at a noisy-but-not-causal NATS error
instead of the actual silent-stop bug.

## Observability

Every service in this repo (orchestrator, claude-runner, codex-runner,
api-proxy, mcp-auth-proxy) exposes Prometheus metrics under the `tank_*`
namespace and is scraped by the kube-prometheus-stack in the `monitoring`
namespace. The orchestrator HTTP middleware emits a structured
`slog.Error` line on every 5xx with `method`, `route`, `email`, and the
response body's `detail` field â€” that log is the first stop for any
"endpoint X returned 500" investigation. The Grafana dashboard
auto-loads from a ConfigMap; PrometheusRule alerts cover the user-trust
failure modes (refresh storms, schema-rejected events, SA-token read
failures). The full taxonomy, cardinality rules, and the "adding a new
metric" recipe live in [docs/observability.md](docs/observability.md);
the migration guard at `scripts/check-removed-chat-runtime.mjs` blocks
re-introduction of the deleted expvar surface (the ad-hoc JSON metrics
endpoint that preceded `/metrics`).

## Migration audit checklist (procedural)

The migration-policy doc is not values â€” it is a checklist. Before declaring
any phase of a migration done, run these literal greps. Treat each as a
gate, not a guideline; a non-zero match means the phase is incomplete and
needs a blocker report per `docs/migration-policy.md`.

- Grep for every name, route, type, env var, and constant the old system
  owned. Anything still present is unmigrated.
- Grep the changed package for `legacy`, `compat`, `fallback`, `temporary`,
  `exception`, `TODO`, `FIXME`. Each is a deletion target.
- Grep the test suite for tests that pin the old behavior. They block the
  cutover; delete them or convert them into "retired path stays out" guards
  in `scripts/check-removed-chat-runtime.mjs`.
- Confirm `scripts/check-removed-chat-runtime.mjs` lists the new forbidden
  patterns so a future PR can't reintroduce them.
- Confirm an observability counter or alert exists for "the old path is
  being used again" â€” `docs/quality-timeframes.md` names observability as
  a completion requirement, not a follow-up.
- Confirm the cutover is atomic: no fallback layer, no parallel path that
  "works for now," no read-side silent skip on old data shapes.
- For wire-format changes affecting **durable JetStream consumers**
  (`filter_subject`, `filter_subjects`, consumer name, or any other
  server-immutable `ConsumerConfig` field), confirm the cutover includes
  an explicit remediation for existing consumers — a deploy alone cannot
  repair them. The runner's `ensureConsumer` at
  `runner-shared/sessionBus.js` only mutates `ack_wait`, `max_deliver`,
  `max_ack_pending`, `inactive_threshold` on existing durables, and
  pre-existing session pods do not auto-recreate per the session
  lifecycle contract. Cutover options: (a) ship an orchestrator-startup
  step that updates each existing durable to the new shape, or (b)
  delete the OLD session pods before the new producer publishes a
  single message in the new shape. Verify by enumerating durables on a
  staging deploy and confirming filter/name parity with the new code.
- Validation MUST exercise a **pre-deploy session pod**, not only a
  freshly-created one. "Live new-session SSE smoke" passes when only the
  producer side of the new wire format is exercised; it cannot detect a
  regression where existing pods still emit on the OLD subject shape or
  subscribe on the OLD filter. Both the SDK migration cutover and
  romaine-life/tank-operator#652 (the `ea70777` "Fix sidebar activity
  readiness indicators" change) shipped with new-session-only validation
  and silently broke chat for every pre-deploy session — the exact
  Agent Runners contract violation ("orchestrator rollout must not
  cancel runner work while the session pod and runner remain alive" /
  "silent strandings are a counted bug class").

Skipping this checklist is the failure mode that left dual-path debt in
the SDK migration and that broke chat delivery for every pre-`ea70777`
session pod in 2026-05-25. The checklist takes ~10 minutes and runs
*before* ExitPlanMode, not after merge.

## Agentic flows ship as multi-stage LLM splits

When a project on this platform ships an autonomous change workflow
(Glimmung runner-k8s runs in Tank, Ambience, etc.),
the LLM work should be **split into stages** â€” typically a plan / test-design
phase, an implementation phase, and a verification phase â€” each running as
its own narrowly-scoped LLM call with its own prompt, tool permissions,
timeout, and JSON+Markdown handoff artifacts. See
[docs/agent-llm-task-splitting.md](docs/agent-llm-task-splitting.md) for the
rationale and stage shape.

Glimmung workflow runtime is database-backed. The live workflow shape is the
Workflow row registered in Glimmung's Postgres database, not a GitHub Actions
workflow and not a file read from this repo at dispatch time. Update
`tank-operator.default` through Glimmung's admin/control-plane workflow path
when the workflow shape changes.

A single LLM doing code + tests + screenshots in one run carries each
phase's noise into the next decision. The split is the load-bearing
context-reduction mechanism for autonomous work â€” don't let it drift back
into a monolith.

**Per-user profile and conversation ledger store: Azure Postgres Flexible Server** — `infra/postgres.tf` provisions a B1ms single-AZ instance with the orchestrator's `claude-credentials-refresher-identity` UAMI as the Entra ID administrator (no password auth in steady state; admin password lives in KV for break-glass only). Schema is owned by `backend-go/internal/pgstore/` and applied via `RunMigrations` at startup — tables are `profiles` (per-user metadata, keyed by email), `github_install_states` (opaque GitHub App install nonces), `session_registry` + `session_counter` (the session list), `conversation_read_state` (per-session last-read cursor), and `session_events` (the app-owned chat ledger, partition-keyed by `tank_session_id` + indexed on `order_key`). Orchestrator code lives under `backend-go/internal/profiles/`, `backend-go/internal/sessionregistry/`, and `backend-go/internal/store/`. A profile row is auto-created from the verified auth.romaine.life JWT on `/api/auth/me` and GitHub install completion. The stores boot in degraded "stub" mode if `POSTGRES_HOST` is unset (first-install ordering before tofu has applied). Replaces the prior Cosmos DB SQL API account, retired in #466 because Cosmos serverless write costs (~$73/mo) dominated the actual workload that a B1ms (~$15/mo flat) handles with point-in-time recovery and the same workload-identity auth shape.


## Sessions

Session pods expose two supported interaction surfaces: GUI chat through the pod-side SDK runner, and terminal sessions through the sandbox-agent/Ghostty stack. The session image bootstrap seeds agent state to skip onboarding prompts, exports the MCP bearer token from the projected SA token, and performs mode-aware credential setup. Claude and Codex sessions use separate SHA-pinned images (`session.image` and `session.codexImage`) built from the same Dockerfile with agent-specific CLIs and support packages baked in, so startup does not fetch skills/extensions at runtime.

Claude and Codex subscription auth are proxy-owned. Session pods write placeholder bearer credentials and route provider hosts through in-cluster Envoy/ext_proc services: `claude-api-proxy` for `api.anthropic.com` and `codex-api-proxy` for `chatgpt.com/backend-api/codex`. The proxies mount the real OAuth blobs from orchestrator-namespace Secrets, inject current access/account headers, single-flight refresh on upstream 401, and write rotated blobs back to KV. Session pods must not mount real refresh-token Secrets. Full mechanism (KV -> ESO -> file -> memory pipeline, freshness invariants, failure-mode catalog, wizard recovery path, multi-deployment hazards) lives in [docs/api-proxy-auth.md](docs/api-proxy-auth.md); read it before any work touching the refresh chain.

GUI sessions use long-lived pod-side SDK runners: `claude-runner` for Claude GUI and `codex-runner` for Codex GUI. The SPA submits user turns to `POST /api/sessions/{session_id}/turns`; the backend publishes `user_message.created` and `turn.submitted` to the NATS JetStream session bus, then publishes a durable `submit_turn` command keyed by `client_nonce`. Runners consume per-session/per-provider JetStream commands across **two distinct consumers**: a data-plane consumer (`max_ack_pending=1`, holds `submit_turn` for the duration of the turn via `working()` heartbeats) and a control-plane consumer (`max_ack_pending=16`, short `ack_wait`, handles `interrupt_turn` and `input_reply`). The two planes ride distinct JetStream subjects on the dedicated WorkQueue command stream (`tank.cmd.<scope>.<session>.commands.<provider>` vs `tank.cmd.<scope>.<session>.control.<provider>`; events stay on the `tank.session.>` bus stream) and have distinct durable consumer names. Runners ack commands only after a durable terminal Tank event has been published. The backend session-bus persister owns Postgres writes: it consumes runner/backend events from JetStream, upserts them into the `session_events` table, then publishes a NATS wake for `/api/sessions/{session_id}/events` SSE streams. The UI renders from `/timeline` and the durable SSE stream; browser polling is not a transcript-live path. Stop publishes an `interrupt_turn` command on the control-plane subject and remains `stopping` until the durable `turn.interrupted` event arrives. When the agent invokes AskUserQuestion, the runner pauses the active turn with durable `turn.awaiting_input`; the user's answer is recorded through `POST /api/sessions/{session_id}/turns/{turn_id}/answer` as `turn.input_answered` and delivered to the paused runner as `input_reply`. See [docs/tank-conversation-protocol.md](docs/tank-conversation-protocol.md) -> "AskUserQuestion pauses the same turn". Terminal sessions use `/cli-process` and the sandbox-agent terminal WebSocket instead of the chat runtime. This command/event fabric is scoped to browser disconnects, orchestrator rollouts, and runner-process restarts inside an otherwise live session pod. Session-pod deletion or death is a terminal session lifecycle event and an explicit non-goal for messaging durability: the session is dead, its `emptyDir` workspace is gone, and Tank does not try to resurrect it. Do not describe session-pod resurrection as a durability goal or product gap unless the session lifecycle goal changes. Claude `ScheduleWakeup` turns are backend-owned durable rows in `session_scheduled_wakeups`: the runner registers the provider tool_use item, the orchestrator claims due rows, writes normal `user_message.created` / `turn.submitted` boundaries, and publishes `submit_turn` with `source=schedule-wakeup`. NATS itself runs in a separate `nats` namespace as a shared platform service deployed by `romaine-life/infra-bootstrap`'s `k8s/nats/` chart, with JetStream memory-only (no PVC). The orchestrator's `sessionBus.url` resolves to `tank-nats.nats.svc.cluster.local:4222`. Session-list change wakes (for SSE on `/api/sessions/events`) flow over a per-owner NATS subject (`tank.live.sessions.<email_token>.wake`), replacing the previous in-process EventBus.

**Session pods are multi-container** (`sandbox` + `mcp-auth-proxy` sidecar). Any `pods/exec` call against them MUST pass `container="sandbox"` — the apiserver returns 400 "a container name must be specified" otherwise, which surfaces to the browser as a 1006 reconnect loop. Same gotcha for ad-hoc `kubectl exec` debugging: use `-c sandbox`.

**Per-session repo selection.** The splash page lets the user stage up to 5 `owner/name` GitHub repo slugs at session-create time; the durable list lives on the `sessions.repos text[]` column and rides every snapshot/SSE row payload. The slug regex and 5-repo cap exist on both sides — `repoSlugPattern`/`maxReposPerSession` in `cmd/tank-operator/handlers_repos.go`, `REPO_SLUG_PATTERN`/`MAX_REPOS_PER_SESSION` in `frontend/src/repos.ts` — and the test suites on each side assert the contract so a one-sided change surfaces. The mode predicate `sessionModeSupportsRepos` (claude_gui / codex_gui) gates the picker UI and the handler boundary: only modes whose pods provision a `/workspace` emptyDir accept a non-empty `repos[]`; CLI / config / api_key and retired Codex GUI aliases reject with 400.

**Session run options.** Tank owns the accepted create modes and provider
model/effort allowlists. The browser reads `GET /api/session-run-options`; MCP
reads `GET /api/internal/session-run-options` through its
`get_session_run_options` tool. Do not add browser-only or MCP-only model/mode
enums. Unsupported model strings, account-default aliases, unknown modes, and
retired Codex GUI aliases (`codex_exec_gui`, `codex_app_server`) must hard-error
before runner dispatch and increment
`tank_session_run_config_rejected_total{surface,provider,reason}`.

The picker has two data sources: `GET /api/github/recent-repos` (durable, reads `sessions.repos` directly â€” works the moment the schema migration lands) and `GET /api/github/repos` which proxies through `internal/mcpgithub` to the cluster-internal `mcp-github` service. The proxy uses an on-behalf-of token exchange â€” the orchestrator presents its `auth.romaine.life`-audience projected SA token to `/api/auth/exchange/k8s` with the SPA caller's email in the request body's `actor_email` field, the IdP mints a `role=service` JWT whose `actor_email` claim carries that email, and `mcp-github`'s existing `actor_email â†’ installation_id` lookup routes the call to the right user's installation. The privilege gate lives at the IdP (`tank-operator` namespace is the only consumer with `allowActorOverride: true`) so `mcp-github` needs no changes and there's no custom side channel. The exchange token is cached per-user with a 30-second refresh skew and is single-flighted so a burst of picker opens doesn't fan out to N exchange calls. Counters: `tank_session_repos_selected_total{count_bucket}` on session-create, `tank_github_repo_list_requests_total{result}` + `tank_github_repo_list_duration_seconds` on the All-repos endpoint.

Explicit repo pins in the picker are a per-user profile preference, not browser
storage. `profiles.pinned_repos text[]` is read through `/api/auth/me` and
`GET /api/github/pinned-repos`; `PUT /api/github/pinned-repos` replaces the
durable list after the same `owner/name` slug validation and a separate
64-entry metadata cap. The SPA does not allowlist or read the retired
`tank.homePinnedRepos` localStorage key; visible pin state updates from the
server response. Counter: `tank_github_pinned_repos_update_total{result}`.

The `repo-cloner` init container runs for pod-backed sessions with a non-empty selection: it reads the durable list from the pod manifest, exchanges the pod's auth.romaine.life service-account token, mints a read-only clone token through mcp-github, clones into `/workspace`, and writes per-repo outcome back to `sessions.clone_state jsonb`.

**Repo attribution.** `sessions.repos` is the durable repo attribution source:
the create-time `owner/name` selection the user staged on the splash page. Tank
does not scan `/workspace` for later ad-hoc clones. Runtime repo discovery via
the pod-side polling reporter was retired because polling is not the product
quality bar for this surface; if later-cloned repo attribution becomes a real
need, build it into an explicit MCP-owned clone/report path instead of
reintroducing workspace polling.

## Auth flow

Microsoft sign-in is delegated to **auth.romaine.life** (Better Auth + Microsoft social provider, in the `romaine-life/auth` repo). Tank-operator never speaks to Microsoft directly: it carries no Entra app registration of its own and no Microsoft JWKS code. The access gate is the platform `role` claim on the auth.romaine.life JWT â€” no per-app email allowlist.

- **SPA boot** (`frontend/src/auth.ts -> bootstrapAuth`): check the stored auth.romaine.life JWT (`localStorage[auth-romaine-jwt]`) and validate it via `/api/auth/me`. If that fails, fetch a fresh JWT from `auth.romaine.life/api/auth/token` (the `.romaine.life` session cookie auto-attaches), store it, and validate it directly against `/api/auth/me`. If both fail, render the Sign-in button.
- **Sign-in** (`startLogin`): top-level redirect to `auth.romaine.life/sign-in/microsoft?callbackURL=<tank-origin>`. After Microsoft callback, auth.romaine.life sets a `.romaine.life` session cookie and bounces back to tank; the next bootstrap fetches the upstream JWT and succeeds.
- **Verification**: every protected Tank endpoint verifies the RS256 signature against `auth.romaine.life/api/auth/jwks`, requires `iss = https://auth.romaine.life`, and gates on the `role` claim. `admin`, `user`, and `service` are accepted; `pending` and unknown roles are rejected. Service principals use auth.romaine.life's `/api/auth/exchange/k8s` path and present that JWT directly to tank-operator. The admin promotes pending users via auth.romaine.life's `/admin` console; there is no per-tank-operator allowlist.
- **No local JWT minting**: tank-operator does not mint browser, session, or install-state JWTs, does not publish its own JWKS, and has no Tank-local K8s token exchange route. REST uses `Authorization: Bearer <auth.romaine.life JWT>`. Terminal WebSocket upgrades use the explicit `access_token` query carrier because browsers cannot set `Authorization` on native WebSocket upgrades. Browser EventSource streams use a separate short-lived opaque `stream_ticket`: the SPA mints it with `POST /api/auth/stream-ticket` through normal bearer auth, the ticket is scoped to stream kind/session scope/session id in Postgres, and only SSE handlers accept it.
- Every protected endpoint verifies the role claim is accepted (`admin`, `user`, or `service`). Human-facing write routes should continue to scope by the caller identity; service-only routes must use the service-principal gate and require `actor_email`.
- **OnboardingWall bypass**: admins skip the wall - the host installation of the GitHub App covers their MCP-github access (see "Two GitHub Apps live alongside each other" below). Service principals also skip the wall for test automation and session-pod handoffs. Standard `user` callers still need to install the App on their own account before `installation_id` lands in Postgres.
- No oauth2-proxy. Session pods authenticate to in-cluster MCP servers via the projected SA token (read fresh per request by the `mcp-auth-proxy` sidecar in each session pod); MCP servers do Azure work via their dedicated UAMIs.
- **Test-slot sign-in**: tank-operator slot hostnames (`https://tank-operator-slot-N.tank.dev.romaine.life`) are *not* listed in `auth.romaine.life`'s static `PROD_TRUSTED_ORIGINS` array. Instead, glimmung owns the slot-origin allowlist for every project that opts in. Tank-operator's glimmung project row carries `managed_auth_origins.enabled=true`; glimmung's reconciler (`internal/server/managed_origins.go`) derives `https://*.{runner_standby_dns.record_base}` from the project metadata and PUTs it to `auth.romaine.life/api/admin/origins/tank-operator`. The wildcard then unions into Better Auth's `trustedOrigins` and Hono's CORS allowlist on `/api/auth/*` at request time (60s in-process cache). See [romaine-life/glimmung#142](https://glimmung.romaine.life/i/glimmung/142) for the cross-repo architecture. To add a new slot DNS shape, update tank-operator's glimmung project metadata (`runner_standby_dns.record_base`) and re-issue any project trigger (scale, register) so the reconciler runs; no manual PR against `romaine-life/auth` required.
- **Test-slot SPA auth**: service-principal tokens (`role=service`, with `actor_email`) are valid authenticated callers for test automation and session-pod handoffs. They bypass the GitHub onboarding wall even when `installation_id=null`; do not ask a service account to install the user-facing GitHub App. See [`docs/testing.md`](docs/testing.md) for the test-slot workflow notes agents should read before browser validation.

**GitHub App install flow (#57 stage 2).** When `/api/auth/me` returns `role=user` with `installation_id=null`, the SPA renders an onboarding wall (`frontend/src/App.tsx -> OnboardingWall`) instead of the main shell. Clicking the install CTA hits `/api/github/install/url`, which creates a 10-minute opaque `github_install_states` nonce bound to the caller's email and 302s to `https://github.com/apps/<github.appSlug>/installations/new?state=...`. GitHub redirects to `/api/github/install/callback`; that unauthenticated callback only attaches `installation_id` to the nonce, then redirects to `/?github_install_state=...`. The SPA completes with `POST /api/github/install/complete` using the verified auth.romaine.life bearer token; the backend consumes the nonce only after the JWT email matches the state email, then updates `profiles.installation_id`. This preserves the phishing defense without a Tank-owned cookie. Validation failures redirect to or set `?install_error=<reason>` for an SPA banner. The user-facing App lives at `https://github.com/apps/romaine-life-tank-operator`; it is org-owned by `romaine-life` and public so standard users can install it on their own accounts. `github.appSlug` in `k8s/values.yaml` carries the actual slug.

**Break-glass CLI auth (admin only).** When tank's UI is broken and an admin needs to reach the API from a curl: load `https://auth.romaine.life/admin` -> click **Mint bot token** -> copy the 24h JWT -> `curl -H "Authorization: Bearer <jwt>" https://tank.romaine.life/api/sessions/8/events`. The bot token carries `role=admin` + `purpose=bot` and goes through the same auth.romaine.life JWKS verifier; admin cross-user reads (below) accept it for any session. Revoke before natural expiry with `az keyvault key rotate auth-jwt-signing`, which rolls the auth-side signing key and invalidates every outstanding JWT. See `romaine-life/auth/README.md -> "Admin bot tokens"`.

**Grafana scripted access (same bot token).** The same auth.romaine.life bot token authenticates against `https://grafana.romaine.life` directly — Grafana's `[auth.jwt]` block validates the same JWKS, so there is no separate Grafana SA token to mint. Hit firing alerts with `curl -H "Authorization: Bearer <jwt>" https://grafana.romaine.life/api/datasources/proxy/uid/prometheus/api/v1/alerts`. This is the canonical scripted-diagnosis path that [docs/diagnostic-discipline.md](docs/diagnostic-discipline.md) step 3 invokes; full recipes (PromQL queries, Alertmanager view) live in [docs/observability.md](docs/observability.md) → "Scripted access via Grafana".

## In-cluster MCP servers

The HTTP MCP servers live in standalone repos. Each MCP repo owns its Python
source, image build workflow, and Helm chart with the `kube-rbac-proxy`
sidecar. Runtime identities (UAMI + federated credential + role assignments +
KV-published client ID) are migrating into their respective MCP repos â€”
`romaine-life/mcp-azure-personal/infra/` is the first; `mcp-tank-operator` and
`mcp-auth` still live in `infra/mcp.tf` here pending the same migration. The
cross-MCP `mcp-tenant-id` KV secret stays here as a shared convenience.
Inbound auth (most servers): the calling session pod's SA token validated via
TokenReview + SubjectAccessReview against the synthetic
`mcp.tank-operator.io/servers/<name>` resource, fronted by a `kube-rbac-proxy`
sidecar. **Exception — `mcp-tank-operator`:** as of mcp-tank-operator#31 it runs
no kube-rbac-proxy sidecar and no per-caller RBAC allowlist; authorization is
solely the auth.romaine.life service JWT (forwarded in `X-Auth-Romaine-Token`,
validated by the orchestrator and scoped to `actor_email`). Currently:

- `romaine-life/mcp-azure-personal` â€” first-party personal Azure MCP server and chart; runtime naming is `mcp-azure-personal` / `azure-personal`
- `romaine-life/mcp-github` â€” custom GitHub-App-backed
- `romaine-life/mcp-k8s` â€” read-only kubectl/helm
- `romaine-life/mcp-argocd` â€” read-only ArgoCD via Dex SA-token exchange (no static API tokens)

**Two GitHub Apps live alongside each other.** `tank-operator-host` is the private org-owned host automation App - it authors PRs from session pods, runs cluster-side automation, etc. The public user-facing App is `romaine-life-tank-operator`, also org-owned by `romaine-life`, intended for friends to install on their own accounts; its credentials live in KV under `tank-operator-app-*`. `mcp-github` reads both Apps' keys, resolves the inbound session caller through tank-operator's internal caller API, mints tokens for that caller's installation, and falls back to the host installation when a non-host user needs host-owned repo access.

### mcp-github write surface: no caller-provided SHAs

Every mutation in `romaine-life/mcp-github/src/mcp_github/tools.py` resolves base refs and blob shas server-side at call time â€” `create_branch(base="main")`, `create_or_update_file(branch=â€¦)`, `delete_file(branch=â€¦)`, `commit_to_branch(branch=â€¦, base="main", files=â€¦)`. There is intentionally no `from_sha` / `sha` parameter on the public surface. The reason this matters: a prior Claude session reverted a merged PR by branching off a *cached* SHA â€” it had read `main`'s HEAD early in the session, merged a PR, then made a second PR from the cached pre-merge SHA. The narrow fix (caller still supplies SHA, but tool requires it) doesn't help, because the caller's cache is the bug. The actual fix is to never let the caller supply identifiers for "where am I branching from" or "what version of the file am I overwriting" â€” the server reads fresh on every call. `commit_to_branch` is the preferred path for any multi-file change because it lands one coherent commit instead of N consecutive `create_or_update_file` calls.

Pair with: normal Tank sessions no longer expose raw GitHub write tokens to the
agent shell. The Tank-owned `publish_current_head` MCP path owns session branch
pushes, records each commit, and starts CI/mergeability watching. Direct
`git push`, `mint_clone_token`, and GitHub MCP file/PR write tools are blocked
in normal mode so there is one governed path from commit to CI evidence.

### azure-mcp config keys

The `mcr.microsoft.com/azure-sdk/azure-mcp` image binds inbound JWT validation and the OAuth metadata document from **ASP.NET Core hierarchical config keys**, not the `AZURE_AD_*` names some Microsoft Bicep templates use. Source: `microsoft/mcp` repo â†’ `Microsoft.Mcp.Core/src/Areas/Server/Commands/ServiceStartCommand.cs`.

- **Inbound auth + metadata:** `AzureAd__Instance`, `AzureAd__TenantId`, `AzureAd__ClientId`
- **Outgoing OBO calls (DefaultAzureCredential / WorkloadIdentityCredential):** `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` (the resource Entra app's clientID â€” federation is on the resource app, not a separate MI), `AZURE_FEDERATED_TOKEN_FILE`, `AZURE_AUTHORITY_HOST`
- `AZURE_MCP_DANGEROUSLY_ENABLE_FORWARDED_HEADERS=true` â€” required behind any TLS-terminating proxy so the OAuth metadata advertises `https://` resource URLs.
- The image entrypoint is already `./server-binary server start`, so pod `args:` should only contain flags. Default ASP.NET Core bind is `localhost:5000` â€” set `ASPNETCORE_URLS=http://+:8080` (or your port) so kubelet probes and the Service can reach it.
