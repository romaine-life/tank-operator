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

Two deployments share the same code (`api-proxy/src/tank_api_proxy/server.py`)
with different `ProxyConfig` env: `claude-api-proxy` fronts
`api.anthropic.com`; `codex-api-proxy` fronts `chatgpt.com/backend-api/codex`.
Session pods reach these via in-pod hostAlias entries pointing the provider
hostname at the proxy Service.

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
   `claude-code-credentials` and `codex-credentials` secrets. Validation slots
   use slot-owned secrets named `<slotName>-claude-code-credentials` and
   `<slotName>-codex-credentials`. Each secret is the source of truth for one
   refresh-token chain across that deployment's restarts. Provisioned by
   `infra/keyvault.tf`; only the proxy's UAMI and the credentials-refresher
   UAMI have write access.

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

2. **External client consumed the chain.** A different deployment or
   break-glass process with write access to the same KV secret rotated and
   either wrote back to KV stale or failed to write back. Test slots must not
   share production credential KV secrets; a rendered slot that points at
   `claude-code-credentials` or `codex-credentials` is invalid. Tell from logs:
   the production proxy never logged a successful rotation, but the KV version
   history shows a write the production proxy didn't make. Recovery: run the
   wizard; investigate the other writer.

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
  `no_refresh_token`, `request_failed`. A spike in `http_error` with no
  matching `success` is the hot-loop signature.
- `tank_api_proxy_kv_persist_total{result}` — `success`, `failure`,
  `skipped`. Persistent `failure` is the restart-bomb precursor: the
  proxy is rotating fine in memory but the next restart will read stale.
- `tank_api_proxy_upstream_status_total{status}` — provider response
  codes seen on Envoy's data-plane side (chatgpt.com or api.anthropic.com).
  Elevated `401` means injected tokens are getting rejected.
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

## Slot credential ownership

Every live proxy deployment owns exactly one refresh-token chain. Production
owns `claude-code-credentials` and `codex-credentials`. A validation slot owns
`<slotName>-claude-code-credentials` and `<slotName>-codex-credentials`.

Slots are intentionally prod-shaped: each slot runs its own api-proxy
deployments, ExternalSecrets, K8s Secrets, and config-mode save path. The
credential wizard must be run once per slot/provider to seed those slot-owned
KV secrets. Copying a production OAuth blob into a slot secret is invalid
because it duplicates the same single-use refresh token into two independent
refresh coordinators.

The chart and `scripts/check-test-slot-provider-credentials.sh` guard this
contract. Hot slot renders must set `CLAUDE_CREDENTIALS_KV_KEY` and
`CODEX_CREDENTIALS_KV_KEY` to slot-owned names; warm slot renders must mirror
the same slot-owned KV keys through ExternalSecret. The proxy and
save-credentials handler fail fast when the credential KV env is absent rather
than choosing a production default.

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
