# Session Lifecycle Capabilities

This ledger names session lifecycle behavior that crosses browser,
orchestrator, database, and pod boundaries.

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
  Codex, and Antigravity session images, and lives in shared Postgres keyed by
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

## Antigravity proxy-owned OAuth (credential boundary)

Status: in progress

Intent:
Keep the real Antigravity (Gemini-Ultra / Google) OAuth refresh token off the
model/tool-capable runtime's filesystem. Previously `antigravity_gui` pods
mounted the harvested OAuth blob (refresh token included) into the
`antigravity-runner` container and copied it into agy's home — a prompt-injected
agy could exfiltrate it. The fix gives agy the same proxy-owned boundary the
Claude/Codex providers use.

Affected contracts:
- Session Lifecycle
- Observability

Contract impact:
- `antigravity_gui` pods never mount the `antigravity-credentials` Secret. The
  launch script seeds a placeholder token (`access_token:
  "managed-by-tank-operator"`, far-future `expiry`, no refresh token) so agy
  never refreshes in place.
- agy's `cloudcode-pa.googleapis.com` traffic is host-aliased to the production
  `antigravity-api-proxy`, which owns the refresh token (mounted only in the
  proxy pod), injects the real access token per request, single-flight-refreshes
  against `oauth2.googleapis.com` on upstream 401, and writes rotated blobs back
  to KV. agy (a Go binary) trusts the proxy leaf via `SSL_CERT_FILE`.
- The credential authority is a single production deployment; validation slots
  route to it and render no antigravity credential Secret / KV key (same slot
  contract as claude/codex). `antigravity_config` (interactive login) is the one
  antigravity mode NOT proxied — it reaches real Google to mint the token.

Evidence:
- `backend-go/internal/sessionmodel/sessionmodel_test.go`
  (`TestPodManifestAntigravityGUIRunnerProxiedNoCredMount`): no `antigravity-cred`
  mount/volume, no `ANTIGRAVITY_CRED_FILE` env, has the `cloudcode-pa` hostAlias
  + `oauth-gateway-ca` mount.
- `api-proxy/tests/test_server.py`: the `antigravity`/`google` provider config
  (form-encoded refresh, KV-sourced client secret), `expiry` blob patch, and
  `expiry`-based freshness.
- Migration guards: `scripts/check-removed-chat-runtime.mjs` blocks the retired
  `ANTIGRAVITY_CRED_FILE` / `/var/run/antigravity-cred` / `AntigravityCredentialsSecret`
  surface; `scripts/check-test-slot-provider-credentials.sh` asserts slots route
  antigravity to the production proxy and mount no antigravity credential.
- Mechanism reference: `docs/api-proxy-auth.md` → "Antigravity (Gemini-Ultra) provider".
- Metrics: `tank_api_proxy_*{provider="antigravity"}` (scraped via the
  `tank-api-proxy` ServiceMonitor; the provider-generic `TankApiProxy*` alerts
  cover it).
