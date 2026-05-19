# tank-operator

Web frontend over a thin K8s orchestrator that spawns ephemeral session pods on demand. The launcher creates GUI chat and terminal agent sessions backed by Kubernetes pods. See [README.md](README.md) for the layout.

## Product position

**The pod is the product**, not any specific agent UI. Pods ship pre-configured with private networking + an MCP gateway + (eventually) docs RAG; the user runs whatever agent they prefer inside it (Claude Code today because it's common, but Codex/Gemini/aider/`vim` should all work). North Star is "don't make users learn our system" — they bring their existing tooling into a pre-baked environment. Closest analog is Coder, with MCP-aware envs as the wedge no one else ships.

**Strategic purpose — LLMs-as-a-service.** Beyond personal use, the platform is designed to hand a hosted dev session to other people. Near-term: collaborate with friends without their own Claude Max subscription (gated by `ALLOWED_EMAIL`). Long-term: enterprise multi-tenant LLM delivery — same shape, with billing/quotas/tenancy. Design preserves this: per-session pod isolation, SA-token-scoped MCP access, no shared filesystem state, email-allowlist auth that can grow into roles/orgs.

**Why not Anthropic Managed Agents (shipped 2026-04-08)?** Covers most of the env-spec primitives but is (a) Claude-only — breaks the agent-agnostic position, (b) API-billed per-token — breaks the "share my Max subscription with friends" economics, (c) explicitly forbids shipping a Claude-Code-styled UI. Tank-operator's defensible edges are the subscription-token sharing model and the agent-agnostic shape; the runtime layer itself is now commoditized.

## Stack

Go orchestrator (`backend-go/`), Vite + React frontend (`frontend/`), multi-stage Dockerfile, Helm chart in `k8s/` synced by ArgoCD. Two namespaces: `tank-operator` (long-lived orchestrator Deployment) and `tank-operator-sessions` (ephemeral session Pods).

The orchestrator was a Python FastAPI + kubernetes-asyncio app until 2026-05-11 (#373), when it was rewritten in Go. The repo has been mostly compiled-language since (Go orchestrator, Go session runner under construction). The api-proxy ext_proc is still Python (small, single-purpose).

## Container build verification

Session pods intentionally do not ship Docker or a container runtime. Do not
report missing local Docker as a blocker. Run available repo checks first
(`go test`, `npm test`, `helm template`, etc.). The normal container
build gate is PR CI: `.github/workflows/docker-build-check.yaml` performs
throwaway Docker builds for every repo-owned image with `push: false`. If an
image-packaging change needs feedback before a PR is ready, manually dispatch
that workflow with `git_ref`. Release/deploy workflows are the only path that
publishes images.

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

Read [docs/diagnostic-discipline.md](docs/diagnostic-discipline.md) before
investigating any bug or incident on this repo. The other three docs above
describe the quality bar for *writing* code; this one describes how to
*investigate* a system so the writing has the right target. Specifically:
when a user reports a behavior claim, query the durable ledger
(`session_events` in Postgres for run state) before looking at logs or
metrics. Logs are loud; the durable ledger is quiet — but it's the
contract. Skipping this step is how nelsong6/tank-operator#532 got its
prior session's diagnosis pointed at a noisy-but-not-causal NATS error
instead of the actual silent-stop bug.

## Observability

Every service in this repo (orchestrator, agent-runner, codex-runner,
api-proxy, mcp-auth-proxy) exposes Prometheus metrics under the `tank_*`
namespace and is scraped by the kube-prometheus-stack in the `monitoring`
namespace. The orchestrator HTTP middleware emits a structured
`slog.Error` line on every 5xx with `method`, `route`, `email`, and the
response body's `detail` field — that log is the first stop for any
"endpoint X returned 500" investigation. The Grafana dashboard
auto-loads from a ConfigMap; PrometheusRule alerts cover the user-trust
failure modes (refresh storms, schema-rejected events, SA-token read
failures). The full taxonomy, cardinality rules, and the "adding a new
metric" recipe live in [docs/observability.md](docs/observability.md);
the migration guard at `scripts/check-removed-chat-runtime.mjs` blocks
re-introduction of the deleted expvar surface (the ad-hoc JSON metrics
endpoint that preceded `/metrics`).

## Migration audit checklist (procedural)

The migration-policy doc is not values — it is a checklist. Before declaring
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
  being used again" — `docs/quality-timeframes.md` names observability as
  a completion requirement, not a follow-up.
- Confirm the cutover is atomic: no fallback layer, no parallel path that
  "works for now," no read-side silent skip on old data shapes.

Skipping this checklist is the failure mode that left dual-path debt in
the SDK migration. The checklist takes ~10 minutes and runs *before*
ExitPlanMode, not after merge.

## Agentic flows ship as multi-stage LLM splits

When a project on this platform ships an autonomous change workflow
(Glimmung native-k8s runs in Tank, Ambience, etc.),
the LLM work should be **split into stages** — typically a plan / test-design
phase, an implementation phase, and a verification phase — each running as
its own narrowly-scoped LLM call with its own prompt, tool permissions,
timeout, and JSON+Markdown handoff artifacts. See
[docs/agent-llm-task-splitting.md](docs/agent-llm-task-splitting.md) for the
rationale and stage shape.

Glimmung workflow runtime is database-backed. The live workflow shape is the
Workflow row registered in Glimmung's Cosmos database, not a GitHub Actions
workflow and not a file read from this repo at dispatch time. Tank does not
keep a `.glimmung/workflows/default.yaml` manifest; update the live
registration through Glimmung's admin API/MCP surface when the workflow shape
changes.

A single LLM doing code + tests + screenshots in one run carries each
phase's noise into the next decision. The split is the load-bearing
context-reduction mechanism for autonomous work — don't let it drift back
into a monolith.

**Per-user profile and conversation ledger store: Azure Postgres Flexible Server** — `infra/postgres.tf` provisions a B1ms single-AZ instance with the orchestrator's `claude-credentials-refresher-identity` UAMI as the Entra ID administrator (no password auth in steady state; admin password lives in KV for break-glass only). Schema is owned by `backend-go/internal/pgstore/` and applied via `RunMigrations` at startup — tables are `profiles` (per-user metadata, keyed by email), `session_registry` + `session_counter` (the session list), `conversation_read_state` (per-session last-read cursor), and `session_events` (the app-owned chat ledger, partition-keyed by `tank_session_id` + indexed on `order_key`). Orchestrator code lives under `backend-go/internal/profiles/`, `backend-go/internal/sessionregistry/`, and `backend-go/internal/store/`. A profile row is auto-created on `/api/auth/exchange` and exposed at `/api/auth/me`. The stores boot in degraded "stub" mode if `POSTGRES_HOST` is unset (first-install ordering before tofu has applied) — login still works, profile fields are null. Replaces the prior Cosmos DB SQL API account, retired in #466 because Cosmos serverless write costs (~$73/mo) dominated the actual workload that a B1ms (~$15/mo flat) handles with point-in-time recovery and the same workload-identity auth shape.

**Session-JWT signing key: KV Key (RSA 2048, sign-in-vault)** — `infra/jwt_signing_key.tf` provisions the key; orchestrator signs each minted session/install-state JWT via the KV Sign operation and caches the public key for in-process verification. Private bytes never leave KV; a compromised orchestrator can verify but cannot forge. Rotation is `az keyvault key rotate` — the verifier resolves whichever `kid` a JWT carries from KV, so old tokens keep working through a rolling rotation.

## Sessions

Session pods expose two supported interaction surfaces: GUI chat through the pod-side SDK runner, and terminal sessions through the sandbox-agent/Ghostty stack. The session image bootstrap seeds agent state to skip onboarding prompts, exports the MCP bearer token from the projected SA token, and performs mode-aware credential setup. Claude, Codex, and Pi sessions use separate SHA-pinned images (`session.image`, `session.codexImage`, and `session.piImage`) built from the same Dockerfile with agent-specific CLIs and support packages baked in, so startup does not fetch skills/extensions at runtime.

Claude and Codex subscription auth are proxy-owned. Codex CLI/GUI session pods write placeholder bearer credentials and route provider hosts through in-cluster Envoy/ext_proc services: `claude-api-proxy` for `api.anthropic.com`, `codex-api-proxy` for `chatgpt.com/backend-api/codex`. The proxies mount the real OAuth blobs from orchestrator-namespace Secrets, inject current access/account headers, single-flight refresh on upstream 401, and write rotated blobs back to KV. Codex CLI/GUI session pods must not mount the real Codex refresh-token Secret. Pi CLI compatibility is isolated from the Codex subscription secret path.

GUI sessions use long-lived pod-side SDK runners: `agent-runner` for Claude GUI and `codex-runner` for Codex GUI. The SPA submits user turns to `POST /api/sessions/{session_id}/turns`; the backend publishes `user_message.created` and `turn.submitted` to the NATS JetStream session bus, then publishes a durable `submit_turn` command keyed by `client_nonce`. Runners consume per-session/per-provider JetStream commands, call `working()` while long turns are active, and ack commands only after a durable terminal Tank event has been published. The backend session-bus persister owns Postgres writes: it consumes runner/backend events from JetStream, upserts them into the `session_events` table, then publishes a NATS wake for `/api/sessions/{session_id}/events` SSE streams. The UI renders from `/timeline` and the durable SSE stream; browser polling is not a transcript-live path. Stop publishes an `interrupt_turn` command and remains `stopping` until the durable `turn.interrupted` event arrives. Claude AskUserQuestion replies publish `input_reply` commands targeted at the provider item; the runner turns those commands into tool-result input and completes them after `tool.approval_resolved`. Terminal sessions use `/cli-process` and the sandbox-agent terminal WebSocket instead of the chat runtime. This command/event fabric is scoped to browser disconnects, orchestrator rollouts, and runner-process restarts inside an otherwise live session pod. Session-pod deletion or death is a terminal session lifecycle event and an explicit non-goal for messaging durability: the session is dead, its `emptyDir` workspace is gone, and Tank does not try to resurrect it. Do not describe session-pod resurrection as a durability goal or product gap unless the session lifecycle goal changes. Claude `ScheduleWakeup` turns use a pod-local `setTimeout` in the agent-runner: the runner extracts the tool_use call, sleeps `delaySeconds`, then publishes a normal `submit_turn` command (`source=schedule-wakeup`) onto the regular command subject. State lives in the runner process and survives orchestrator rollouts; a runner-process restart inside a live pod loses pending wakeups, which is within the durability boundary documented above (the pod, not Postgres, owns runtime scheduler state). NATS itself runs in a separate `nats` namespace as a shared platform service deployed by `nelsong6/infra-bootstrap`'s `k8s/nats/` chart, with JetStream memory-only (no PVC). The orchestrator's `sessionBus.url` resolves to `tank-nats.nats.svc.cluster.local:4222`. Session-list change wakes (for SSE on `/api/sessions/events`) flow over a per-owner NATS subject (`tank.live.sessions.<email_token>.wake`), replacing the previous in-process EventBus.

**Session pods are multi-container** (`claude` + `mcp-auth-proxy` sidecar). Any `pods/exec` call against them MUST pass `container="claude"` — the apiserver returns 400 "a container name must be specified" otherwise, which surfaces to the browser as a 1006 reconnect loop. Same gotcha for ad-hoc `kubectl exec` debugging: use `-c claude`.

## Auth flow

Microsoft sign-in is delegated to **auth.romaine.life** (Better Auth + Microsoft social provider, in the `nelsong6/auth` repo). Tank-operator never speaks to Microsoft directly: it carries no Entra app registration of its own and no Microsoft JWKS code. The access gate is the platform `role` claim on the auth.romaine.life JWT — no per-app email allowlist.

- **SPA boot** (`frontend/src/auth.ts → bootstrapAuth`): check the stored tank-operator JWT and validate it via `/api/auth/me`. If that fails, try a silent exchange — fetch a JWT from `auth.romaine.life/api/auth/token` (the `.romaine.life` session cookie auto-attaches), then POST it to `/api/auth/exchange`. If both fail, render the Sign-in button.
- **Sign-in** (`startLogin`): top-level redirect to `auth.romaine.life/api/auth/sign-in/social/microsoft?callbackURL=<tank-origin>`. After Microsoft callback, auth.romaine.life sets a `.romaine.life` session cookie and bounces back to tank; the next bootstrap hits the silent-exchange path and succeeds.
- **Exchange** (`POST /api/auth/exchange`, body `{"auth_jwt": "<jwt>"}`): orchestrator verifies the RS256 signature against `auth.romaine.life/api/auth/jwks`, asserts `iss = https://auth.romaine.life`, then gates on the `role` claim. This human browser exchange accepts `admin` and `user` — `pending` (auth.romaine.life's default for any fresh Microsoft sign-in) and any unknown role get a 403. Service principals use auth.romaine.life's `/api/auth/exchange/k8s` path and present that JWT directly to tank-operator. The admin promotes pending users via auth.romaine.life's `/admin` console; there is no per-tank-operator allowlist.
- **Mint** stamps a tank-operator-signed session JWT via the `tank-operator-jwt-signing` KV Key (7-day TTL, `kid` per rotation). The role claim rides along so every protected endpoint can verify against this service's signing key alone, no round-trip to auth.romaine.life on the read path.
- **Session JWT** comes back as response body (frontend → `localStorage[tank-operator-jwt]`) and as an httpOnly cookie (`auth_token`). REST uses Bearer; terminal WebSocket upgrades use the cookie since browsers can't set `Authorization` on WS upgrades.
- Every protected endpoint verifies the role claim is accepted (`admin`, `user`, or `service`). Human-facing write routes should continue to scope by the caller identity; service-only routes must use the service-principal gate and require `actor_email`.
- **OnboardingWall bypass**: admins skip the wall — the host installation of the GitHub App covers their MCP-github access (see "Two GitHub Apps live alongside each other" below). Service principals also skip the wall for test automation and session-pod handoffs. Standard `user` callers still need to install the App on their own account before `installation_id` lands in Postgres.
- No oauth2-proxy. Session pods authenticate to in-cluster MCP servers via the projected SA token (read fresh per request by the `mcp-auth-proxy` sidecar in each session pod); MCP servers do Azure work via their dedicated UAMIs.
- **Test-slot sign-in**: tank-operator slot hostnames (`https://tank-operator-slot-N.tank.dev.romaine.life`) are *not* listed in `auth.romaine.life`'s static `PROD_TRUSTED_ORIGINS` array. Instead, glimmung owns the slot-origin allowlist for every project that opts in. Tank-operator's glimmung project row carries `managed_auth_origins.enabled=true`; glimmung's reconciler (`internal/server/managed_origins.go`) derives `https://*.{native_standby_dns.record_base}` from the project metadata and PUTs it to `auth.romaine.life/api/admin/origins/tank-operator`. The wildcard then unions into Better Auth's `trustedOrigins` and Hono's CORS allowlist on `/api/auth/*` at request time (60s in-process cache). See [nelsong6/glimmung#142](https://glimmung.romaine.life/i/glimmung/142) for the cross-repo architecture. To add a new slot DNS shape, update tank-operator's glimmung project metadata (or `native_standby_dns.record_base` if the slot space is moving) and re-issue any project trigger (scale, register) so the reconciler runs; no manual PR against `nelsong6/auth` required.
- **Test-slot SPA auth**: service-principal tokens (`role=service`, with `actor_email`) are valid authenticated callers for test automation and session-pod handoffs. They bypass the GitHub onboarding wall even when `installation_id=null`; do not ask a service account to install the user-facing GitHub App. See [`docs/testing.md`](docs/testing.md) for the test-slot workflow notes agents should read before browser validation.

**GitHub App install flow (#57 stage 2).** When `/api/auth/me` returns `role=user` with `installation_id=null`, the SPA renders an onboarding wall (`frontend/src/App.tsx → OnboardingWall`) instead of the main shell. Clicking the install CTA hits `/api/github/install/url`, which mints a 10-min state JWT (custom audience `tank-operator/github-install`) bound to the caller's email and 302s to `https://github.com/apps/<github.appSlug>/installations/new?state=...`. After GitHub install consent, GitHub redirects to `/api/github/install/callback`; the callback validates *both* the state JWT and the caller's `auth_token` cookie agree on email (defense-in-depth against a phishing flow where an attacker tricks a victim into installing under the attacker's profile), then upserts `installation_id` on the Postgres profile row and 302s back to `/`. Validation failures redirect to `/?install_error=<reason>` for an SPA banner. The user-facing App lives at `https://github.com/apps/tank-operator-romaine-life` (`tank-operator` slug was taken globally on github.com) — `github.appSlug` in `k8s/values.yaml` carries the actual slug.

**Break-glass CLI auth (admin only).** When tank's UI is broken and an admin needs to reach the API from a curl: load `https://auth.romaine.life/admin` → click **Mint bot token** → copy the 24h JWT → `curl -H "Authorization: Bearer <jwt>" https://tank.romaine.life/api/sessions/8/events`. The bot token carries `role=admin` + `purpose=bot` and goes through the same JWKS verifier the cookie path uses; admin cross-user reads (below) accept it for any session. Revoke before natural expiry with `az keyvault key rotate auth-jwt-signing`, which rolls the auth-side signing key and invalidates every outstanding JWT — fine for a rare-event surface. See `nelsong6/auth/README.md → "Admin bot tokens"`.

**Admin cross-user reads (the why for role=admin in this codebase).** Read-only session endpoints — events list/SSE, single-session metadata, read-state cursor, file/MCP/skill listings, session list (via `?owner=<email>`) — bypass the per-owner gate when the caller has `role=admin`. Read-side handlers go through `authorizeSessionRead` (`auth_session.go`) which returns 404 (not 403) on cross-user denial so the API doesn't leak the existence of sessions a non-admin can't read. **Writes intentionally stay per-owner**: turns, file uploads/edits, terminal attach, name/test/rollout/credential patches, and delete all continue to call `mgr.GetByOwner` or `s.resolveSessionPod` (the write helper), so an admin token cannot mutate someone else's session. Admin lift is a read-only contract by construction — if a new write handler ever needs admin reach, it must explicitly opt in by writing a separate code path, not by switching to the read helper. Observed via the `tank_admin_cross_user_session_{reads,lists}_total` counters; structured slog lines on the session-list SSE handler carry both `caller` and `owner` when they differ.

## In-cluster MCP servers

The HTTP MCP servers live in standalone repos. Each MCP repo owns its Python
source, image build workflow, and Helm chart with the `kube-rbac-proxy`
sidecar. Runtime identities (UAMI + federated credential + role assignments +
KV-published client ID) are migrating into their respective MCP repos —
`nelsong6/mcp-azure-personal/infra/` is the first; `mcp-tank-operator` and
`mcp-auth` still live in `infra/mcp.tf` here pending the same migration. The
cross-MCP `mcp-tenant-id` KV secret stays here as a shared convenience.
Inbound auth: claude-session SA token validated via TokenReview +
SubjectAccessReview against the synthetic
`mcp.tank-operator.io/servers/<name>` resource. Currently:

- `nelsong6/mcp-azure-personal` — first-party personal Azure MCP server and chart; runtime naming is `mcp-azure-personal` / `azure-personal`
- `nelsong6/mcp-github` — custom GitHub-App-backed
- `nelsong6/mcp-k8s` — read-only kubectl/helm
- `nelsong6/mcp-argocd` — read-only ArgoCD via Dex SA-token exchange (no static API tokens)

**Two GitHub Apps live alongside each other.** `romaine-life-app` is the host's dev/automation bot - it authors PRs from session pods, runs cluster-side automation, etc. The user-facing App is `tank-operator-romaine-life`, intended for friends to install on their own accounts; its credentials live in KV under `tank-operator-app-*`. `mcp-github` now reads the user-facing App keys, resolves the inbound session caller through tank-operator's internal caller API, mints tokens for that caller's installation, and falls back to the host installation when a non-host user needs host-owned repo access.

### mcp-github write surface: no caller-provided SHAs

Every mutation in `nelsong6/mcp-github/src/mcp_github/tools.py` resolves base refs and blob shas server-side at call time — `create_branch(base="main")`, `create_or_update_file(branch=…)`, `delete_file(branch=…)`, `commit_to_branch(branch=…, base="main", files=…)`. There is intentionally no `from_sha` / `sha` parameter on the public surface. The reason this matters: a prior Claude session reverted a merged PR by branching off a *cached* SHA — it had read `main`'s HEAD early in the session, merged a PR, then made a second PR from the cached pre-merge SHA. The narrow fix (caller still supplies SHA, but tool requires it) doesn't help, because the caller's cache is the bug. The actual fix is to never let the caller supply identifiers for "where am I branching from" or "what version of the file am I overwriting" — the server reads fresh on every call. `commit_to_branch` is the preferred path for any multi-file change because it lands one coherent commit instead of N consecutive `create_or_update_file` calls.

Pair with: prefer the MCP write tools above over `git push` for routine mutations — they resolve refs server-side and dodge the cached-SHA footgun by construction. `mint_clone_token` defaults to a read-only token (`contents: read`); pass `write=True` only when a working-tree push is the right shape (large lockfiles, tool-driven multi-file refactors, anything awkward to enumerate as an explicit `files` array). Push tokens still can't force-push to protected branches — branch protection is the second line of defense, not this scope. The image deliberately doesn't ship a credential helper for `https://github.com`; the inline `x-access-token:<token>@github.com/...` URL form is the only auth path.

### azure-mcp config keys

The `mcr.microsoft.com/azure-sdk/azure-mcp` image binds inbound JWT validation and the OAuth metadata document from **ASP.NET Core hierarchical config keys**, not the `AZURE_AD_*` names some Microsoft Bicep templates use. Source: `microsoft/mcp` repo → `Microsoft.Mcp.Core/src/Areas/Server/Commands/ServiceStartCommand.cs`.

- **Inbound auth + metadata:** `AzureAd__Instance`, `AzureAd__TenantId`, `AzureAd__ClientId`
- **Outgoing OBO calls (DefaultAzureCredential / WorkloadIdentityCredential):** `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` (the resource Entra app's clientID — federation is on the resource app, not a separate MI), `AZURE_FEDERATED_TOKEN_FILE`, `AZURE_AUTHORITY_HOST`
- `AZURE_MCP_DANGEROUSLY_ENABLE_FORWARDED_HEADERS=true` — required behind any TLS-terminating proxy so the OAuth metadata advertises `https://` resource URLs.
- The image entrypoint is already `./server-binary server start`, so pod `args:` should only contain flags. Default ASP.NET Core bind is `localhost:5000` — set `ASPNETCORE_URLS=http://+:8080` (or your port) so kubelet probes and the Service can reach it.
