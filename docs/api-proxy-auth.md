# API-proxy auth & refresh mechanism

How the Claude and Codex subscription-token chains live, rotate, and recover.
Written 2026-05-24 after the codex refresh-token incident that PRs
[#637](https://github.com/romaine-life/tank-operator/pull/637) and
[#639](https://github.com/romaine-life/tank-operator/pull/639) fixed.

## Why this doc exists

Until now the mechanism was one paragraph in `CLAUDE.md` + a one-liner in
`README.md` + a few load-bearing docstrings inside
`api-proxy/src/tank_api_proxy/server.py`. The failure-mode reasoning lived
entirely in code comments. When the codex chain broke on 2026-05-24,
diagnosing it required re-deriving the design from the code — exactly the
"reach for activity log / state files / vendor docs first" failure that
[diagnostic-discipline.md](diagnostic-discipline.md) is supposed to prevent.
This doc is the missing reference.

## Architecture at a glance

```
                 ┌──────────────────────────┐
                 │   Azure Key Vault         │
                 │   romaine-kv              │
                 │   {claude,codex}-creds    │
                 └──────────┬───────────────┘
                            │ ESO refreshInterval=1m
                            ▼
            ┌─────────────────────────────────────┐
            │  K8s Secret (tank-operator/...)      │
            │  data: auth.json | .credentials.json │
            └──────────────┬──────────────────────┘
                           │ mounted as file
                           ▼
   ┌──────────────────────────────────────────────────────┐
   │  api-proxy pod                                        │
   │    envoy (listener for *.anthropic.com or chatgpt)    │
   │       │                                                │
   │       │ each request -> ext_proc gRPC                  │
   │       ▼                                                │
   │    tank_api_proxy.server (Python)                     │
   │       _cached_access, _cached_refresh, _cached_blob   │
   │       _lock, _refresh_task (single-flight)            │
   │                                                        │
   │       On request:  inject Authorization: Bearer ...   │
   │       On 401:      schedule _refresh task             │
   │       On rotate:   memory FIRST, then write KV        │
   └──────────────────────────────────────────────────────┘
                           │ direct (not via Envoy)
                           ▼
              auth.{openai,anthropic}.com/oauth/token
```

Three deployments share the same code (`api-proxy/src/tank_api_proxy/server.py`)
with different `ProxyConfig` env: `claude-api-proxy` fronts
`api.anthropic.com`; `codex-api-proxy` fronts `chatgpt.com/backend-api/codex`;
Session pods reach these via in-pod hostAlias entries pointing the provider
hostname at the proxy Service.

## Proxy TLS cert rotation

The three provider API proxies terminate TLS for provider hostnames with
cert-manager leaf certificates issued by `claude-oauth-ca-issuer`. Envoy must
consume those leaf Secrets through file-based SDS, not a static
`tls_certificates` file reference. The Secret is still mounted at
`/etc/envoy/tls`, but the downstream listener references the named
`api_proxy_leaf` SDS secret and the `TlsCertificate` sets
`watched_directory: /etc/envoy/tls`.

This is load-bearing for cert-manager rotation. A `Certificate` update changes
the Secret content, not the Deployment pod template; Kubernetes therefore does
not create a new ReplicaSet. File-based SDS makes Envoy observe the mounted
Secret's symlink rotation and update its TLS context in-process instead of
requiring a manual pod recycle. The chart guard
`scripts/check-api-proxy-envoy-sds.sh` renders the production Helm chart and
fails if `claude-api-proxy-envoy`, `codex-api-proxy-envoy`, or
Envoy admin remains bound to localhost; the ext_proc metrics sidecar polls
`127.0.0.1:9901/stats` in-pod and re-exports the bounded SDS counters
`tank_api_proxy_envoy_sds_ssl_context_updates`,
`tank_api_proxy_envoy_sds_key_rotation_failed`, and
`tank_api_proxy_envoy_sds_stats_scrape_total` through the existing
ServiceMonitor-scraped `/metrics` endpoint.

## Why session pods don't own the refresh chain

Session pods write a **placeholder** Bearer token to `~/.claude/.credentials.json`
or `~/.codex/auth.json` at bootstrap time. The real refresh_token never lands
in a session pod's filesystem. The Envoy ext_proc filter overwrites the
`Authorization` header on every request that comes in with the placeholder
bearer — `_on_request_headers` swaps it for the proxy's current cached access
token before the request reaches the provider.

This isolation is the load-bearing security boundary: a compromised session
pod cannot exfiltrate the refresh_token because it doesn't have it. The
session pod can only ride the proxy's already-rotated access tokens, which
have short TTLs.

See `_on_request_headers` ([server.py:309](../api-proxy/src/tank_api_proxy/server.py:309))
for the placeholder-detection and header-injection logic. Calls that arrive
with a non-placeholder Authorization (e.g. claude-code worker_jwt for
`/worker` endpoints) are passed through untouched.

## State pipeline (KV → ESO → file → memory)

The refresh_token has four physical residences and three transitions:

1. **Azure Key Vault** (`romaine-kv` vault). Production uses the historical
   `claude-code-credentials` and `codex-credentials` secrets. Each secret is
   the source of truth for one refresh-token chain across the production proxy
   deployment's restarts. Validation slots do not own provider credential KV
   secrets; their session pods route through the production proxy services
   instead of running independent refreshers. Provisioned by `infra/keyvault.tf`;
   only the proxy's UAMI and the credentials-refresher UAMI have write access.

2. **K8s Secret** in the orchestrator namespace, mirrored from KV by the
   ExternalSecret resource declared in `k8s/templates/externalsecret-*.yaml`
   with `refreshInterval: 1m`. ESO re-pulls KV every minute and re-renders
   the Secret only when the underlying value changes (so non-changing KV
   doesn't cause spurious Secret churn).

3. **File on the proxy pod's filesystem**, mounted from the K8s Secret via
   volumeMount at the path set by `ProxyConfig.credentials_file`. Updates
   propagate via kubelet's projected-volume refresh cycle (eventual,
   usually within seconds).

4. **In-memory state** in the proxy process: `_cached_access`,
   `_cached_refresh`, `_cached_account_id`, `_cached_blob`. Initialized to
   `None` at boot; populated from file on first need.

Transitions:

- `_reload_from_file` ([server.py:489](../api-proxy/src/tank_api_proxy/server.py:489))
  pulls file → memory, but **only if the file is strictly fresher than
  memory**. The defensive check is the load-bearing invariant: if the proxy
  just rotated in-process and KV+ESO haven't propagated back yet, the file
  still holds the pre-rotation tokens. Clobbering memory with that would
  make the next refresh fail because the provider already invalidated the
  pre-rotation refresh_token.

- `_persist_to_kv` ([server.py:600](../api-proxy/src/tank_api_proxy/server.py:600))
  writes memory → KV after a successful rotation. **Best-effort.** Failure
  is logged + metric'd but does not block the rotation itself (memory still
  has the fresh tokens).

- ESO does file ← KV on a 1-minute cadence, transparently to the proxy.

Freshness is determined by `_blob_freshness_ms`
([server.py:421](../api-proxy/src/tank_api_proxy/server.py:421)): the largest
of `expiresAt`/`expires_at` (Claude), `last_refresh` parsed as ISO timestamp
(Codex), and the access JWT's `exp` claim. The proxy picks the max so a
just-re-seeded file with an older `last_refresh` still wins if it carries a
fresher access JWT.

## Single-flight refresh

When the provider responds with `401`, the request that triggered it had its
`Authorization` injected by the proxy (we only treat 401 as an invalidation
signal for requests we actually injected — see `_on_response_headers`
[server.py:372](../api-proxy/src/tank_api_proxy/server.py:372)).
`_access_invalidated` is set, and a `_refresh` task is **scheduled only if
one isn't already in flight**:

```python
if self._refresh_task is None or self._refresh_task.done():
    self._refresh_task = asyncio.create_task(self._refresh())
```

Concurrent waiters that need a token see `_access_invalidated=True` in
`_get_access_token` and `await self._refresh_task` instead of scheduling
their own. Without this dedupe, N concurrent 401s would each schedule their
own `_refresh()`, each successive rotation would single-use-invalidate its
predecessor's refresh_token, and the proxy logs would show a "rotation
storm" of five+ successful rotations in two seconds — followed by every
in-flight rotation except the last one failing on the write-back.

The actual exchange call goes to `auth.{openai,anthropic}.com/oauth/token`
**directly from the proxy pod's egress**, not through the Envoy listener.
Envoy's listener only fronts the provider's data plane (`chatgpt.com` or
`api.anthropic.com`). Direct egress means the refresh exchange isn't
observable to session pods and can't be retried by Envoy's
`retriable_status_codes: [401]` policy.

### Cross-replica single-flight (the refresh lease)

The in-process dedupe above only protects one pod. With
`apiProxy.replicas > 1`, two pods hitting expiry concurrently would each
rotate the SAME single-use refresh_token — and provider reuse detection can
revoke the whole grant family, killing the chain for every pod until a human
re-runs the credential wizard. So before calling the provider, `_refresh`
takes a **rotation-scoped Kubernetes Lease**
([lease.py](../api-proxy/src/tank_api_proxy/lease.py)):

- The lease is named `api-proxy-refresh-<kv-secret-name>` — scoped per
  credential **chain**, not per provider, because `claude-api-proxy` and
  `claude-secondary-api-proxy` both run `PROXY_PROVIDER=claude` but rotate
  unrelated chains backed by different KV secrets.
- This is **not standing leader election**: a pod holds the lease only for
  the seconds one rotation takes (TTL 120s outlives a wedged winner), then
  releases it.
- A pod that loses the lease does **not** call the provider. It polls its
  mounted credentials file (`_await_peer_rotation`, 2s interval, 45s
  budget) until the winner's rotation propagates through KV → ESO → file,
  then adopts it. On timeout it rotates anyway — availability over strict
  exclusivity, e.g. when the winner crashed before the write-back.
- **Fail open**: if the Lease API is unreachable or RBAC is missing
  (`LeaseUnavailable`), the pod proceeds exactly as the pre-lease code did.
  A lease outage degrades to the old risk level, never to "no rotations".
- Every outcome is counted in `tank_api_proxy_refresh_lease_total`
  (`acquired` / `deferred_to_peer` / `proceeded_after_timeout` /
  `unavailable`). The RBAC lives next to the proxy ServiceAccount in
  [k8s/templates/api-proxy.yaml](../k8s/templates/api-proxy.yaml).

`apiProxy.replicas` stays **1** until those metrics show clean
acquired/deferred behavior in production; raising it also renders
`minAvailable: 1` PDBs for the proxy Deployments. See the gating note on
`apiProxy.replicas` in [k8s/values.yaml](../k8s/values.yaml).

## Credential refresh wizard (recovery path)

When the refresh chain dies (see Failure modes below), a human re-seeds it
through the credential-refresh wizard modes:

- `codex_config` — opens a session pod with the codex CLI, terminal boots
  already running `codex login --device-auth`.
- `config` — same shape for claude, runs `claude /login`.

The per-mode launch is wired in `handleCLIProcess`
([handlers_terminal.go](../backend-go/cmd/tank-operator/handlers_terminal.go))
via `cliProcessLaunchForMode`. The on-disk seeding (e.g. forcing
`cli_auth_credentials_store = "file"` for codex so the OS-keychain default
doesn't apply in a Linux container) is in
[session-pod-bootstrap.sh](../k8s/session-config/session-pod-bootstrap.sh).
Both restored after `#650c282` deleted the load-bearing Python bootstrap
as "dead."

The end-to-end flow:

1. User opens a config-mode session from the splash `+` menu.
2. Pod boots; `session-pod-bootstrap.sh` writes the per-mode config files.
3. Sandbox-agent starts; the user's browser attaches a TTY to a shell
   already running the login command.
4. User completes the OAuth device flow; the provider writes the new
   token bundle to `~/.codex/auth.json` (or `~/.claude/.credentials.json`).
5. User clicks **save** in the SPA; orchestrator's `doSaveCredentials`
   ([credentials.go](../backend-go/cmd/tank-operator/credentials.go)) execs
   into the pod, reads the file, validates JSON, and writes the blob to
   the corresponding KV secret.
6. ESO mirrors the new KV value to the K8s Secret within ~1 minute.
7. The proxy's next `_reload_from_file` sees the file is strictly fresher
   than memory, accepts the reload, and starts serving the new chain.

The proxy does **not** require a restart to pick up the new blob.

## Failure modes

### `refresh_token_reused`

Provider error:
```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "refresh_token_reused",
    "message": "Your refresh token has already been used to generate a new access token. Please try signing in again."
  }
}
```

The refresh_token the proxy presented was already exchanged somewhere else
for a new pair. Possible causes, ordered by likelihood:

1. **Restart-after-KV-write-failed.** Proxy rotated in memory, KV write
   failed (`_persist_to_kv` is best-effort), proxy restarted, read the
   stale Secret, replayed the now-used refresh_token. Tell from logs: the
   pre-incident proxy logged "rotated codex successfully" but
   "KV write failed"; the post-restart proxy logs "loaded codex credentials
   from file" then immediate `refresh_token_reused`. Recovery: run the
   wizard.

2. **External client consumed the chain.** A break-glass process or unapproved
   deployment with write access to the same KV secret rotated and either wrote
   back to KV stale or failed to write back. Test slots must not render
   provider credential KV keys or credential ExternalSecrets at all; they share
   credentials by routing traffic through the production proxy authority. Tell
   from logs: the production proxy never logged a successful rotation, but the
   KV version history shows a write the production proxy didn't make. Recovery:
   run the wizard; investigate the other writer.

3. **Provider-side invalidation.** Account-level revocation, security
   action, or maintenance event at the provider side. Tell from logs:
   nothing in tank-operator's history correlates with the timing; the
   KV version history shows no writes; no other deployments touched the
   secret. Recovery: run the wizard.

### `invalid_grant` / `expired_token`

Refresh_token aged out. Codex tokens have a finite refresh_token lifetime
even when used regularly (per CLAUDE.md: "codex refreshes its token bundle
in place on a ~8-day cadence" — the cadence implies the underlying
refresh_token is rotated, not held indefinitely). If the chain sits idle
long enough for the absolute expiry to hit, the next refresh attempt
returns `invalid_grant`. **Distinct from `refresh_token_reused`** — the
error code wires the failure to a different cause. Recovery: run the
wizard.

### `no_refresh_token`

`_reload_from_file` couldn't find a refresh_token in the blob, or the file
was missing entirely. Tells: `record_refresh("no_refresh_token")` metric
fires; log line "no refresh token available; cannot rotate". Cause: KV
secret never seeded, ESO failed to mirror, or volumeMount missing.
Recovery: check the K8s Secret exists and ESO is reconciling it, then run
the wizard if the wallet is genuinely empty.

### Hot-loop / rotation storm

Visible as N "calling oauth/token to rotate" log lines in a few seconds,
all failing. Two distinct mechanisms:

- **All `refresh_token_reused` failures**: the proxy's cached refresh_token
  is already dead and every retry compounds the same `reused` error. The
  3.5-minute hot loop on 2026-05-24T18:45 (82 errors) looked like this.
  Single-flight prevents the proxy itself from compounding the damage —
  each fan-out comes from a new upstream 401, not from N concurrent
  refresh tasks. Recovery: wizard.

- **Mixed `success` then failure**: pre-single-flight pattern (no longer
  applicable since `_refresh_task` was added) where N concurrent refresh
  tasks each rotated, each invalidating the predecessor. Listed here so a
  future engineer reading logs from an older code path can recognize the
  shape.

## Observability

Per [observability.md](observability.md), the proxy exposes Prometheus
metrics on `:9100/metrics`. Key counters:

- `tank_api_proxy_refresh_result_total{result}` — `success`, `http_error`,
  `no_refresh_token`, `request_failed`, `deferred_to_peer`. A spike in
  `http_error` with no matching `success` is the hot-loop signature.
- `tank_api_proxy_refresh_lease_total{result}` — `acquired`,
  `deferred_to_peer`, `proceeded_after_timeout`, `unavailable`. The gate
  for raising `apiProxy.replicas` past 1: sustained `unavailable` means
  the lease RBAC/API is broken (fail-open active, multi-replica unsafe);
  `proceeded_after_timeout` means losers aren't seeing winners' rotations
  propagate within 45s.
- `tank_api_proxy_kv_persist_total{result}` — `success`, `failure`,
  `skipped`. Persistent `failure` is the restart-bomb precursor: the
  proxy is rotating fine in memory but the next restart will read stale.
- `tank_api_proxy_upstream_status_total{status}` — provider response
  codes seen on Envoy's data-plane side (chatgpt.com or api.anthropic.com).
  Elevated `401` means injected tokens are getting rejected.
- `tank_api_proxy_upstream_429_total{provider}` — upstream 429 rate-limit
  responses on injected requests. A sustained rate is the shared account's
  usage cap being exhausted (alert: `TankApiProxyRateLimited`). This is the
  upstream cause of the rate-limit-stall class; pod-side runners convert a
  stuck `api_retry{rate_limit}` storm into a durable
  `turn.failed{reason:"provider_rate_limit"}` and the orchestrator's
  `TankSessionStuckInProgress` catches any that wedge.
- `tank_api_proxy_ext_proc_request_total{result}` — `passthrough`,
  `injected`, `missing_token`. `missing_token` means
  `_get_access_token` returned `"missing"` because cache was empty;
  Envoy's retry will trigger a refresh.

Log lines to grep for in `kubectl -n tank-operator logs codex-api-proxy-* -c ext-proc`:

- `loaded codex credentials from file (access prefix=...)` — first
  successful file load after boot or after a re-seed.
- `calling https://auth.openai.com/oauth/token to rotate codex token` —
  refresh started.
- `rotated codex successfully (access prefix=..., expires in 3600s)` —
  refresh completed; new tokens now in memory.
- `refresh failed: status=401 body={...}` — refresh attempt that the
  provider rejected; inspect body for `code`.
- `wrote rotated blob to https://romaine-kv.vault.azure.net/secrets/codex-credentials` —
  KV write succeeded. Followed by ESO mirroring within ~1m.
- `KV write failed; tokens stay in memory only` — the precursor to a
  restart-bomb if the pod gets recycled before another KV write succeeds.

For grafana, the boards loaded from `k8s/templates/grafana-dashboards/`
include an "api-proxy" panel showing the above counters as rates.

## Per-session attribution (dependency-gated)

The proxy sees every model call but cannot today say *which Tank session*
caused a given upstream 429, latency spike, or `request-id`. Two facts block
per-session attribution at this layer:

- The Claude Agent SDK `Options` (claude-runner) exposes no custom-header /
  fetch hook, so the runner cannot stamp an `x-tank-session-id` (plus owner /
  turn) header on outbound model requests. Without an inbound identity header
  the ext_proc has nothing to attribute by.
- The proxy has no Postgres or session-bus producer, so it cannot itself write
  a durable per-call ledger keyed by session.

Deliberately NOT done: inferring the session from the downstream pod IP or by
post-hoc joining the Envoy access log. Pod IPs churn and a log join is the
"re-scrape of logs" anti-pattern (`docs/product-inspirations.md`); per-session
resolution must live in a durable ledger, never a metric label or a log grep
(`docs/observability.md` cardinality rules).

Path forward when the dependency lands: stamp `x-tank-session-id` + hashed
owner + turn id from the runner, read them in `_on_request_headers`, capture
the upstream `request-id` and `anthropic-ratelimit-*` headers in
`_on_response_headers`, and emit a durable per-call record — or have the runner
emit it from the `rate_limit_event` frame, which already carries the provider
`session_id`. As a down payment, the Envoy access log's `anthropic_req_id`
field was corrected to `%RESP(request-id)%` (it had been
`%RESP(anthropic-request-id)%`, a header the provider never sends, so it logged
`-` on every line) so the upstream request id is at least captured in logs in
the meantime.

## Slot credential ownership

Every refresh-token chain has exactly one live credential authority. Production
owns the Claude and Codex authorities: the production api-proxy deployments,
the production credential ExternalSecrets, and the production KV secrets
`claude-code-credentials` and `codex-credentials`.

Validation slots share those credentials by routing provider traffic to the
production services:

- `CLAUDE_API_PROXY_HOST=claude-api-proxy.tank-operator.svc.cluster.local`
- `CODEX_API_PROXY_HOST=codex-api-proxy.tank-operator.svc.cluster.local`
- `CLAUDE_OAUTH_GATEWAY_HOST=claude-oauth-gateway.tank-operator.svc.cluster.local`

Slot session pods still receive only placeholder provider credentials. They
trust the production proxy TLS leaf because the slot warm render reflects the
production `claude-oauth-ca` public cert into the slot sessions namespace as
the same `claude-oauth-ca` ConfigMap mounted by session pods.

Slots do **not** render api-proxy deployments, provider credential
ExternalSecrets, provider credential K8s Secrets, or `*_CREDENTIALS_KV_KEY`
env vars on the orchestrator. That makes the save-credentials path fail closed
in slots with "`<env> not configured`" instead of giving a config-mode slot a
way to overwrite production's credential KV secrets.

The chart and `scripts/check-test-slot-provider-credentials.sh` guard this
contract. A slot render that reintroduces a slot-local provider proxy, a
slot-owned provider credential KV key, a provider credential Secret mount, or a
credential write env var is invalid.


which previously mounted the real Google OAuth blob — including the refresh
refresh token); the proxy closes it.

What carries over unchanged from claude/codex:

- The session pod writes a **placeholder** token (the launch script
  `access_token: "managed-by-tank-operator"`, a far-future `expiry`, and **no
  `daily-cloudcode-pa.googleapis.com`) is host-aliased to the proxy Service;
  the ext_proc swaps the `Authorization` header for the real access token on
  every request and single-flight-refreshes on upstream 401.
- The real refresh token lives only in the proxy pod + KV


- **`google` `ProxyConfig`** — `token_url=https://oauth2.googleapis.com/token`,
  it out of git). Google's `/token` requires **form-encoding**, so the config
  sets `token_request_form=True` (claude/codex post JSON).
- **Blob shape** — `{token:{access_token, refresh_token, expiry}, auth_method}`.
  `expiry` is an RFC3339 string (not epoch ms), so `_patch_blob` and
  `_blob_freshness_ms` handle `expiry` alongside the existing `expiresAt`/
  `last_refresh` markers. Google does not return a new refresh token on refresh,
  so the proxy reuses the cached one (existing behavior).
  system trust store. The launch script concatenates the mounted
  `oauth-gateway-ca` (same CA as the claude/codex leaves) with the base bundle
  and exports `SSL_CERT_FILE` — the Go analog of claude's `NODE_EXTRA_CA_CERTS`
  / codex's `CODEX_CA_CERTIFICATE`.

interactive Google/Ultra login reaches real Google; `doSaveCredentials` then

## Proactive refresh keeper

Each proxy runs a long-lived **refresh keeper** task (`run_refresh_keeper`,
started in `serve()`) that warms the access token on boot and again before it
reaches `REFRESH_SKEW_MS` of expiry. It exists for two reasons that a purely
reactive (refresh-on-upstream-401) design cannot cover on a low-traffic
provider:

- **Cold-start race.** Without continuous traffic to trigger a reactive
  refresh, a proxy that boots with an already-expired cached token would serve
  that expired token on the first request, 401, and only then start an async
  stream) has already given up. The keeper warms the token before the first
  request, so the first turn succeeds.
- **Cancellation safety.** A reactive refresh is created inside a request
  handler (`_on_response_headers`), so it can be cancelled when that request's
  stream closes — stranding both the rotation and the KV write-back. The keeper
  re-runs the refresh from a task that outlives any single stream, so the
  rotation **and** `_persist_to_kv` always complete. The reactive 401 path
  additionally wakes the keeper (`_refresh_wakeup`) for immediate recovery.

`_refresh` skips the provider round trip when the token is already fresh and not
invalidated, so the keeper and a reactive 401 cannot double-rotate. This was
proxy booted with a stale cached token returned empty, and rotations were not
persisting to KV); it hardens the claude/codex proxies the same way.

## Where to look when investigating

If a user reports "my codex token died," in order:

1. `kubectl -n tank-operator logs codex-api-proxy-* -c ext-proc | grep -E "rotated|refresh failed|loaded codex"` —
   find the most recent successful rotation, the most recent file load,
   and the failure pattern.
2. `az keyvault secret list-versions --vault-name romaine-kv --name codex-credentials -o table` —
   when was the secret last written, by what.
3. `kubectl -n tank-operator get pods -l app.kubernetes.io/name=codex-api-proxy -o jsonpath='{.items[*].status.startTime}'` —
   has the proxy restarted recently?
4. `kubectl -n external-secrets logs external-secrets-* | grep "tank-operator/codex-credentials"` —
   ESO sync events; useful if the K8s Secret content seems stale.
5. Grafana api-proxy board: `refresh_result_total`, `kv_persist_total`,
   `upstream_status_total` — for the timing and shape of the failure.

If none of those identify a specific cause, the chain has died for a
reason outside the proxy's visibility (provider-side event, KV-external
writer, etc.) — file as "unexplained, recovered via wizard" and watch for
recurrence.

## Related

- [observability.md](observability.md) — full metric and dashboard
  taxonomy.
- [diagnostic-discipline.md](diagnostic-discipline.md) — investigation
  methodology; the durable-ledger-first principle applies here (KV
  version history and ESO logs are the durable ledger for this surface).
- `api-proxy/src/tank_api_proxy/server.py` — the implementation.
- `k8s/templates/api-proxy.yaml` — the Envoy + ext_proc Helm template
  for both deployments.
- `k8s/templates/externalsecret-*.yaml` — the ExternalSecret resources
  that mirror KV → K8s Secret.
- `backend-go/cmd/tank-operator/credentials.go` — the save-credentials
  harvest path called by the wizard's **save** button.
