# Tailnet host access for session pods

How a tank session pod reaches a host on the Tailscale tailnet (today: the
SpireLens game laptop, `tag:spirelens-host`) using only the OIDC identity every
session already carries — **no pre-shared keys, no per-pod secrets**.

Written 2026-05-30 while wiring session-pod access to the SpireLens game-host
MCP. The two trust relationships below were configured but undocumented; this is
the missing reference (the human's standing ask: "if that's not documented,
please do so"). The verification recipe at the end was run end-to-end from a
live session pod before any of this shipped.

## Why this doc exists

Some hosts a session needs live **outside the cluster, on the tailnet** — e.g.
the Windows laptop that runs Slay the Spire 2 and the `spire-lens-mcp` server.
Before this, a session pod had no durable path there; access depended on an
SSH-relay binary hand-copied into `/tmp`, which "may or may not exist in your
fresh pod." This doc replaces that with a first-class, OIDC-authenticated path.

The design reuses what session pods already have — the projected
`auth.romaine.life` token (see [CLAUDE.md → Auth flow](../CLAUDE.md)) — and what
Tailscale already trusts. Nothing new is minted, stored, or distributed per pod.

## The two trust relationships

### 1. Pod identity: the projected `auth.romaine.life` token

Every session pod is handed a projected Kubernetes service-account token,
audience-pinned to `https://auth.romaine.life`, by
`backend-go/internal/sessionmodel/sessionmodel.go` (`PodManifest`):

- mount path: `/var/run/secrets/auth.romaine.life/token`
- audience: `https://auth.romaine.life`, `expirationSeconds: 3600` (kubelet rotates in place)

This is the pod's identity. `auth.romaine.life` (the `nelsong6/auth` IdP) exposes
two exchanges that consume it:

- `POST /api/auth/exchange/k8s` → a `role=service` JWT (RS256, `iss
  https://auth.romaine.life`, JWKS `…/api/auth/jwks`, carries `actor_email`)
  that in-cluster services accept. This is the existing MCP-auth path — the
  `mcp-auth-proxy` sidecar mints it and forwards it as the bearer / a
  `X-Auth-Romaine-Token` header for the in-cluster MCP servers.
- `POST /api/auth/exchange/federation` → a JWT minted for an **external**
  `audience` (e.g. Tailscale). This is the basis of the tailnet path below.

### 2. Network identity: Tailscale trusts `auth.romaine.life` (OIDC federation)

Tailscale is configured to trust `auth.romaine.life` as an **OIDC identity
provider** — this is *OIDC federation*, **not** a classic OAuth client with a
secret. The identifier `T6vFBk1dAa11CNTRL-kf6kJRvG5T11CNTRL` is the **OIDC
client id of that trust** (the `aud` the federation JWT targets); it is **not a
secret** and nothing was hand-created per consumer. It is read from config
(glimmung publishes it as `GLIMMUNG_TAILSCALE_OIDC_CLIENT_ID`, tailnet
`GLIMMUNG_TAILSCALE_TAILNET="-"`).

A pod turns its identity into tailnet membership with this exchange chain (all
verified working from a `claude-session` pod):

1. **Federate** the pod's `auth.romaine.life` token for the Tailscale audience:
   ```
   POST https://auth.romaine.life/api/auth/exchange/federation
   Authorization: Bearer <…/auth.romaine.life/token>
   {"audience": "api.tailscale.com/T6vFBk1dAa11CNTRL-kf6kJRvG5T11CNTRL"}
   → {"token": "<RS256 JWT, iss=https://auth.romaine.life, aud=api.tailscale.com/…>"}
   ```
2. **Exchange** that JWT for a short-lived Tailscale API access token:
   ```
   POST https://api.tailscale.com/api/v2/oauth/token-exchange
   client_id=<oidc client id>&jwt=<federation JWT>
   → {"access_token": "tskey-token-…", "expires_in": …, "scope": "all"}
   ```
   Tailscale validates the JWT against `auth.romaine.life`'s JWKS — this is the
   trust the human means by "Tailscale trusts the OIDC token issued to
   tank-operator/glimmung pods."
3. **Mint** an ephemeral, pre-authorized, **tagged** auth key:
   ```
   POST https://api.tailscale.com/api/v2/tailnet/-/keys
   Authorization: Bearer <access_token>
   {"capabilities":{"devices":{"create":{"ephemeral":true,"preauthorized":true,
     "reusable":false,"tags":["tag:spirelens-orchestrator"]}}},"expirySeconds":…}
   → {"key": "tskey-auth-…"}
   ```

Glimmung does steps 1–3 **server-side** for *run* pods and hands them the key
via `GLIMMUNG_TAILSCALE_AUTHKEY_URL` (run-callback-scoped). Interactive session
pods are not runs, so they perform the same exchange **themselves** in the pod
bootstrap — the federation endpoint authorizes the session identity directly
(verified), so no broker is required.

## How a session pod joins the tailnet (self-mint)

The session image bakes the `tailscale`/`tailscaled` binaries; the bootstrap
runs userspace networking (pods have no `NET_ADMIN` / `/dev/net/tun`):

```sh
tailscaled --tun=userspace-networking \
  --statedir=/workspace/.tailscale-state --socket=/tmp/tailscaled.sock \
  --outbound-http-proxy-listen=127.0.0.1:1055 &
authkey=$(… steps 1–3 above …)
tailscale --socket=/tmp/tailscaled.sock up --authkey="$authkey" \
  --hostname="session-<id>" --accept-routes=false --accept-dns=false
```

`--outbound-http-proxy-listen` matters: under userspace networking a bare
`curl http://100.x:<port>` does **not** route; an HTTP client must go through
tailscaled's local HTTP proxy (or `tailscale nc`). The `mcp-auth-proxy` sidecar
sets `HTTP_PROXY=http://127.0.0.1:1055` so its outbound requests reach the
tailnet.

## Reaching the game-host MCP

The game host runs `spire-lens-mcp`'s `server.py --transport http --bind-port
15527` (see that repo's README → "Remote clients"). The agent reaches it like
any other MCP — a static localhost entry in the session `.mcp.json` →
`mcp-auth-proxy` → tailnet:

```
agent → 127.0.0.1:9997 (mcp-auth-proxy)
          │ injects Authorization: Bearer <auth.romaine.life role=service JWT>
          │ outbound via HTTP_PROXY=127.0.0.1:1055 (tailscaled)
          ▼
        tag:spirelens-host : 15527  (validates the JWT: --auth-mode jwt)
```

The credential is the same `auth.romaine.life` service token the proxy already
mints for other MCPs — **no shared secret**. The server validates it against
`auth.romaine.life`'s JWKS (issuer + `role=service`, optional `actor_email`
allowlist); see `spire-lens-mcp` `--auth-mode jwt`.

## What is console-managed (outside any repo)

These live in the Tailscale admin console / `nelsong6/auth`, not in this repo,
and must be set/confirmed out of band:

- **The OIDC IdP trust** in Tailscale (trusting `auth.romaine.life`, the OIDC
  client id, and which **tags** it may assign). Session pods reuse the existing
  trust; they assign `tag:spirelens-orchestrator` (the client already permits it).
- **`tagOwners`** for `tag:spirelens-host` and `tag:spirelens-orchestrator`
  (both `autogroup:admin`).
- **ACL grants.** The host only accepts what the policy allows. Current grants:
  - `tag:spirelens-orchestrator → tag:spirelens-host:22` (SSH; the glimmung-native run flow)
  - **`tag:spirelens-orchestrator → tag:spirelens-host:15527`** — required for the
    HTTP MCP; add this accept rule.

## Verifying (run from a session pod)

```sh
# 1. identity → federation JWT
TOKEN=$(cat /var/run/secrets/auth.romaine.life/token)
CID=T6vFBk1dAa11CNTRL-kf6kJRvG5T11CNTRL
fed=$(curl -fsS -X POST https://auth.romaine.life/api/auth/exchange/federation \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"audience\":\"api.tailscale.com/$CID\"}" | jq -r .token)
# 2. → Tailscale access token  3. → ephemeral key  (see steps above)
# 4. tailscale up (userspace), then:
tailscale --socket=/tmp/tailscaled.sock status         # shows the tag:spirelens-host peer
tailscale --socket=/tmp/tailscaled.sock nc <host-ip> 15527   # blocked until the ACL grant lands
```

A `:15527` probe that hangs→times out means the ACL grant is missing; a fast
connect (or an MCP `initialize` returning `200` + `serverInfo`) means the path
is open end-to-end.
