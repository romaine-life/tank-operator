# Auth And Streams Capabilities

This ledger names auth behavior that crosses browser, orchestrator, Kubernetes,
and provider credential boundaries.

## Shared Provider Credential Authority For Test Slots

Status: active

Intent:
Let validation slots use the host Claude/Codex subscriptions without creating
independent refreshers that can consume or overwrite production OAuth refresh
chains.

Affected contracts:
- Auth And Streams
- Session Lifecycle
- Observability

Contract impact:
- Production api-proxy deployments are the only live refreshers for the
  host-wide Claude and Codex OAuth blobs.
- Validation-slot session pods route `api.anthropic.com`, `chatgpt.com`, and
  `platform.claude.com` to production service DNS through the existing
  hostAlias mechanism.
- Validation slots do not render provider credential ExternalSecrets, provider
  credential Secret mounts, provider api-proxy deployments, or provider
  credential KV write env vars. Slot save-credentials therefore fails closed
  instead of writing production KV secrets.
- Slot sessions trust the production proxy leaf by reflecting production's
  public `claude-oauth-ca` into the slot sessions namespace.

Evidence:
- `scripts/check-test-slot-provider-credentials.sh` guards hot and warm slot
  Helm renders.
- `helm template tank-operator ./k8s`, `helm template ... --set
  renderMode=warm`, and `helm template ... --set renderMode=hot` cover
  production and slot chart shape.
- `docs/api-proxy-auth.md` documents the final ownership model and incident
  diagnosis path.

## API Proxy Leaf Cert Rotation Through Envoy SDS

Status: active

Intent:
Provider API proxy TLS leaf rotations must be absorbed by the running Envoy
listener without relying on a manual pod recycle.

Affected contracts:
- Auth And Streams
- Observability

Contract impact:
- `claude-api-proxy`, `codex-api-proxy`, and `antigravity-api-proxy` Envoy
  downstream listeners consume their cert-manager leaf Secrets through
  file-based SDS.
- The mounted TLS Secret directory is watched at `/etc/envoy/tls`, matching
  Kubernetes Secret symlink rotation semantics.
- Static downstream `tls_certificates` file references are retired for these
  proxies; a Ready `Certificate` can no longer leave Envoy serving a stale
  in-memory leaf until a human restarts the pod.
- The ext_proc metrics sidecar polls Envoy's localhost admin stats and
  re-exports bounded SDS counters through the existing `/metrics` endpoint, so
  failed filesystem key rotations are visible without exposing Envoy admin on
  the pod IP.

Evidence:
- `scripts/check-api-proxy-envoy-sds.sh` renders the production Helm chart and
  rejects missing SDS config, missing `watched_directory`, or reintroduced
  static downstream `tls_certificates` blocks.
- `api-proxy/tests/test_metrics.py` pins the Envoy SDS stat parser and
  re-exported metric labels.
- `helm template tank-operator ./k8s` covers chart rendering.
- `docs/api-proxy-auth.md` documents the SDS rotation path and the reason
  Deployment rollouts are not the source of truth for cert uptake.

## Public Tank HTTP Redirects To HTTPS

Status: active

Intent:
Make browser entry to Tank secure by construction. Plain HTTP is allowed only
as a redirect surface; app traffic must terminate on the HTTPS listener before
it reaches the Tank backend.

Affected contracts:
- Auth And Streams
- App Chrome
- Session Lifecycle, for validation-slot public routes

Contract impact:
- Production `tank.romaine.life` and validation-slot
  `*.tank.dev.romaine.life` backend routes attach only to HTTPS listener sets.
- Routes attached to the shared port-80 Gateway listener must use
  `RequestRedirect` to `https` with status `301`.
- A port-80 `HTTPRoute` must not contain `backendRefs`; serving app bytes over
  plain HTTP is a user-trust regression, not a supported fallback.

Evidence:
- `scripts/check-tank-http-route-security.mjs` renders the production chart and
  a representative validation-slot chart, rejects any HTTP-listener backend
  route, and requires the HTTPS redirect route.
- The guard workflow runs the script's self-test plus the rendered-chart check.
