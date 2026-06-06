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
