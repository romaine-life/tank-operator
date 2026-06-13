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
  token>` (audience `https://auth.romaine.life` — the same platform audience
  used by auth.romaine.life's exchange path and the MCP gateway). The callout
  validates the token via audience-pinned `TokenReview`, resolves the
  authenticated ServiceAccount subject into a concrete session authority, takes
  the **bound pod name from the token's claims**, reads the pod's
  orchestrator-written labels (`tank-operator/session-id`, `-scope`) in that
  authority's namespace, and issues permissions for exactly that session:
  - publish `tank.session.<scope>.<sid>.events`
  - the `TANK_SESSION_COMMANDS` consumer API (`$JS.API.CONSUMER.{DURABLE.
    CREATE,CREATE,INFO,MSG.NEXT}`) for the session's own per-provider
    durables (data + control planes), plus `$JS.API.INFO`
  - subscribe `_INBOX.>`
  The claimed username is only ever checked for equality with the pod's
  label binding — identity comes from the cluster, not the client.
- **Session authorities** are closed and explicit. Production accepts only
  `system:serviceaccount:tank-operator-sessions:claude-session` and requires
  pod scope `default`. Glimmung validation slots share the production NATS
  broker and production auth-callout; a slot token is accepted only when its
  subject is `system:serviceaccount:<slot>-sessions:<slot>-session` where
  `<slot>` has the `tank-operator-slot-<N>` shape, the namespace is labelled as
  a Glimmung `tank-operator` test slot whose native slot name is `<slot>`, and
  the bound pod carries
  `tank-operator/session-scope=<slot>`. Slot Helm renders grant the production
  `tank-operator/nats-auth-callout` ServiceAccount `get pods` only in that
  slot's sessions namespace; the callout does not receive cluster-wide pod
  read.
- **The orchestrator and the callout itself** are static `auth_users` in
  the NATS server config and never route through the callout — a callout
  outage cannot take down the command plane. Existing connections keep
  their authorization; a NEW pod's connect retries forever and JetStream
  redelivers, so the blast radius of a callout outage is "new session pods
  connect late".

Outcomes are counted in `tank_nats_auth_callout_total{result}`:
`session` / `denied_*` / `error`. Denials are bounded to credential,
subject-authority, pod-binding, and claimed-identity failures so slot auth
regressions are visible without high-cardinality labels. The callout has no Service (it answers
NATS, not HTTP), so the counter is scraped by the `tank-nats-auth-callout`
PodMonitor in `k8s/templates/observability.yaml`; `TankNatsAuthCalloutDenials`
surfaces the `denied_*`/`error` new-pod auth-failure class.

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
   pods created before stage 3 used the old shared token and had to be
   recycled before the final cleanup.
3. **Flip session credentials** — `sessionmodel.go` injects `NATS_USER`
   (the storage key) + `NATS_PASSWORD_FILE` (the projected
   auth.romaine.life-audience SA token path) instead of `NATS_TOKEN`;
   runners send user/pass. JavaScript runners read the password file from
   the NATS authenticator so reconnects see token rotation; Antigravity's
   Go runner exits/restarts on permanent auth closure and reads the file on
   boot.
4. **Drop the legacy grant** — completed after stage 3: the callout has no
   shared-token branch, the callout deployment has no legacy-token env, and
   the chart no longer renders a
   session-namespace `tank-nats-auth` ExternalSecret. The remaining
   `tank-nats-auth` Secret in the orchestrator namespace is only the static
   password for `NATS_USER=tank-operator`.

Rollback at any stage: stage 2 reverts to `authorization.token` (one config
sync). The subject and durable shapes the callout grants are pinned against
`internal/sessionbus` + `runner-shared/sessionBus.js` by
`TestSessionDurableNamesMirrorRunnerShared` and the embedded-server
permission tests in `cmd/nats-auth-callout`.
