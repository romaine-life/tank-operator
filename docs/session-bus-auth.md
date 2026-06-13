# Session-bus authentication (per-session NATS credentials)

Issue: tank-operator#1128. Code: `backend-go/cmd/nats-auth-callout`,
chart `k8s/templates/nats-auth-callout.yaml`, server config in
infra-bootstrap `k8s/nats/values.yaml`.

## Why

Until #1128, every session pod mounted the same fleet-wide NATS token
(`tank-nats-auth`), so any pod could publish to any session's subjects on
either stream — one compromised pod could forge durable events or commands
fleet-wide, contradicting the per-session pod isolation preserve.

## How it works

NATS `authorization.auth_callout` routes every client connect that is not a
static `auth_users` entry to `$SYS.REQ.USER.AUTH`. The
`nats-auth-callout` service answers with a user JWT signed by the issuer
account nkey:

- **Session pods** connect with `user=<storage key>`, `pass=<projected SA
  token>` (audience `auth.romaine.life` — the MCP-gateway trust root). The
  callout validates the token via audience-pinned `TokenReview`, takes the
  **bound pod name from the token's claims**, reads the pod's
  orchestrator-written labels (`tank-operator/session-id`, `-scope`), and
  issues permissions for exactly that session:
  - publish `tank.session.<scope>.<sid>.events`
  - the `TANK_SESSION_COMMANDS` consumer API (`$JS.API.CONSUMER.{DURABLE.
    CREATE,CREATE,INFO,MSG.NEXT}`) for the session's own per-provider
    durables (data + control planes), plus `$JS.API.INFO`
  - subscribe `_INBOX.>`
  The claimed username is only ever checked for equality with the pod's
  label binding — identity comes from the cluster, not the client.
- **Legacy pods** (pre-flip session images) present the shared fleet token;
  the callout grants them the old unrestricted permissions until they age
  out (`NATS_CALLOUT_LEGACY_TOKEN`, wired to the same `tank-nats-auth`
  secret). Removing that env is the final security flip.
- **The orchestrator and the callout itself** are static `auth_users` in
  the NATS server config and never route through the callout — a callout
  outage cannot take down the command plane. Existing connections keep
  their authorization; a NEW pod's connect retries forever and JetStream
  redelivers, so the blast radius of a callout outage is "new session pods
  connect late".

Outcomes are counted in `tank_nats_auth_callout_total{result}`:
`session` / `legacy` / `denied_*` / `error`. `legacy` hitting zero for a
full pod-age window is the signal that stage 4 (drop the legacy grant) is
safe.

## Staged rollout

1. **Deploy the issuer** — tank-operator chart, `natsCallout.enabled=true`.
   Seed KV first:
   ```sh
   nk -gen account > issuer.nk          # seed (SA…); public key via: nk -inkey issuer.nk -pubout
   az keyvault secret set --name tank-nats-callout-issuer-seed  --value "$(cat issuer.nk)" ...
   az keyvault secret set --name tank-nats-callout-password     --value "$(openssl rand -hex 24)" ...
   ```
   Nothing routes through the callout yet.
2. **Flip the NATS server config** (infra-bootstrap `k8s/nats`):
   `authorization.users` (orchestrator + callout static users; orchestrator
   password = the existing fleet token value) + `auth_callout { issuer:
   <account public key>, auth_users: [tank-operator, nats-auth-callout] }`.
   The orchestrator deployment simultaneously gains `NATS_USER`; session
   pods (legacy token) now authenticate THROUGH the callout's legacy grant.
   Watch `tank_nats_auth_callout_total{result="legacy"}` count the fleet.
3. **Flip session credentials** — `sessionmodel.go` injects the storage key
   + SA-token path instead of `NATS_TOKEN`; runners send user/pass. New
   pods only (migration policy, pre-deploy-pod clause); old pods keep
   working via the legacy grant for ≤7d (idle reaper bound).
4. **Drop the legacy grant** once `legacy` flatlines: remove
   `NATS_CALLOUT_LEGACY_TOKEN` from the callout deployment and delete the
   `tank-nats-auth` ExternalSecret from the sessions namespace. This is the
   point where forging another session's events stops being possible.

Rollback at any stage: stage 2 reverts to `authorization.token` (one config
sync); stages 3–4 revert by re-adding the env/secret. The subject and
durable shapes the callout grants are pinned against
`internal/sessionbus` + `runner-shared/sessionBus.js` by
`TestSessionDurableNamesMirrorRunnerShared` and the embedded-server
permission tests in `cmd/nats-auth-callout`.
