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

This is the pod's identity. `auth.romaine.life` (the `romaine-life/auth` IdP) exposes
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
receives `TAILNET_HTTP_PROXY=http://127.0.0.1:1055` and applies it only to the
SpireLens upstream request path, so the normal in-cluster `.svc` MCPs never route
through the tailnet proxy.

## Tank Operator session capability

Tank exposes this path as an explicit create-time session capability:
`spirelens_mcp`. It is rare infrastructure access, so it is opt-in per session,
not part of the default pod surface.

The orchestrator behavior is:

- `POST /api/sessions` and the internal create paths accept
  `capabilities: ["spirelens_mcp"]`.
- handlers normalize and reject unknown capability names.
- `Manager.Create` refuses `spirelens_mcp` unless
  `SESSION_SPIRELENS_TAILSCALE_OIDC_CLIENT_ID`, `SESSION_SPIRELENS_TAILSCALE_TAILNET`,
  and `SESSION_SPIRELENS_HOST` are configured.
- `PodManifest` persists the capability as `tank-operator/capabilities`, mounts
  `mcp.spirelens.json` over `/workspace/.mcp.json`, sets the bootstrap tailnet
  env on the user container, and sets `SPIRELENS_MCP_UPSTREAM` plus
  `TAILNET_HTTP_PROXY` on `mcp-auth-proxy`.
- `session-pod-bootstrap.sh` joins the tailnet only when
  `SPIRELENS_MCP_ENABLED=true`.
- `mcp-auth-proxy` opens `127.0.0.1:9997` only when
  `SPIRELENS_MCP_UPSTREAM` is set.
- `/api/config` exposes `spirelens_mcp_available=true|false`; the SPA shows the
  home-screen toggle only when the deployment is configured.

Chart defaults live under `session.spirelens` in `k8s/values.yaml`:

```yaml
session:
  spirelens:
    oidcClientId: T6vFBk1dAa11CNTRL-kf6kJRvG5T11CNTRL
    tailnet: "-"
    authTag: tag:spirelens-orchestrator
    host: nelsonlaptop
    port: 15527
```

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

## Running commands on the host (the IdP SSH CA)

To run *setup/admin* commands on the game host (e.g. install the supervised
MCP-server scheduled task), a session pod opens an SSH session — authenticated by
the **same IdP**, not a hand-distributed key. `auth.romaine.life` is the sole SSH
CA; the host's `sshd` trusts it via `TrustedUserCAKeys` (`GET
https://auth.romaine.life/api/ssh/ca`). The session SA
`tank-operator-sessions/claude-session` is **already allowlisted** for the cert
exchange (`auth/k8s/values.yaml` `sshCertSaAllowlist`), and principal
`spirelens-agent` matches `sshCertPrincipalPattern` — so a session pod can mint a
host login cert with no new surface.

Proven recipe (run from a session pod; needs the baked `openssh-client` +
`tailscale`):

```sh
# 1. ed25519 keypair + an auth.romaine.life-signed user cert (principal spirelens-agent)
ssh-keygen -t ed25519 -N '' -f /tmp/hk
cert=$(curl -fsS -X POST https://auth.romaine.life/api/auth/exchange/ssh-cert \
  -H "Authorization: Bearer $(cat /var/run/secrets/auth.romaine.life/token)" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg pk "$(cat /tmp/hk.pub)" '{public_key:$pk,key_id:"tank-session",principals:["spirelens-agent"],extensions:["permit-pty"],ttl_seconds:600}')" \
  | jq -r .certificate)
printf '%s\n' "$cert" > /tmp/hk-cert.pub

# 2. join the tailnet (self-mint authkey as above) → resolve the host by tag
host_ip=$(tailscale --socket=$SOCK status --json | jq -r '.Peer[]|select((.Tags//[])|index("tag:spirelens-host"))|.TailscaleIPs[]|select(test("^100\\."))' | head -1)

# 3. SSH as the local account `nelsonlaptopuser` (NOT spirelens-agent — that's the
#    cert PRINCIPAL, matched by the host's AuthorizedPrincipalsFile). Under userspace
#    networking, dial through tailscale, e.g. ProxyCommand `tailscale nc` or a SOCKS5
#    proxy (`tailscaled --socks5-server`).
ssh -i /tmp/hk -o CertificateFile=/tmp/hk-cert.pub \
    -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=accept-new \
    -o "ProxyCommand=tailscale --socket=$SOCK nc %h %p" \
    nelsonlaptopuser@"$host_ip" "pwsh -NoProfile -Command -" <<'PS'
  whoami
PS
```

Gotchas learned the hard way (so you don't relearn them):
- **SSH username = `nelsonlaptopuser`**, cert **principal = `spirelens-agent`** (`scripts/glimmung-native/lib.sh` `native_ssh_user`). Connecting *as* `spirelens-agent` fails auth.
- The host's `D:\repos\spire-lens-mcp` is **run-managed** — it's only `git pull`ed during a glimmung run and is `reset --hard` per run, so it can sit on a stale commit (it was pre-PR-#11 here) and carry discardable local edits that block `git pull --ff-only`. To update it manually: `git -C D:\repos\spire-lens-mcp fetch origin <ref>; git -C ... reset --hard origin/<ref>`.
- A pod *without* `openssh-client` baked yet can drive the same flow from Python
  (`paramiko` cert auth + `pysocks` over `tailscaled --socks5-server`).

## Proven end-to-end (2026-05-31)

From an unmodified session pod, using only IdP-issued credentials, the full chain
was validated live against the real game host: mint service JWT + tailnet key +
SSH cert → join tailnet → SSH the laptop → run `server.py --transport http
--bind-host <ts-ip> --bind-port 15527 --auth-mode jwt` → MCP `initialize` from the
pod over the tailnet returned **401 without** the `auth.romaine.life` bearer and
**200 + `serverInfo{"name":"spire-lens-mcp"}` with** it. Two server-side fixes were
required and are on `spire-lens-mcp` PR #12: the `--auth-mode jwt` validator, and
disabling the MCP SDK's DNS-rebinding Host check (it 421s remote binds otherwise).

## Supervised MCP-server task on the host (installed)

The server runs as a Windows Scheduled Task **`SpireLens MCP HTTP`** (machine env
`SPIRELENS_HOST_MCP_TASK` names it, mirroring `SPIRELENS_HOST_STS2_LAUNCH_TASK`).
It is logon-triggered for `nelsonlaptop\nelsonlaptopuser`, `InteractiveToken` /
`HighestAvailable`, no execution time limit, `IgnoreNew` multiple-instances, and
restart-on-failure (1 min, 999×) — so it comes back after a crash or sign-in. Its
action runs a launcher that resolves the tailnet IP at start (it isn't known
until tailscale is up) then serves jwt-mode:

```powershell
# D:\automation\spirelens-mcp\run-mcp-http.ps1
$ErrorActionPreference='Stop'
$ts=$null
for($i=0;$i -lt 60;$i++){ $ts=(& tailscale ip -4 2>$null | Select-Object -First 1); if($ts){break}; Start-Sleep 5 }
if(-not $ts){ throw 'tailscale IP unavailable' }
Set-Location 'D:\repos\spire-lens-mcp\mcp'
& uv run --frozen python server.py --transport http --bind-host $ts --bind-port 15527 --auth-mode jwt
```

Register it (cmdlet API — `Register-ScheduledTask -Xml` is finicky about schema/encoding):

```powershell
$action    = New-ScheduledTaskAction -Execute (Get-Command pwsh.exe).Source -Argument '-NoProfile -WindowStyle Hidden -File D:\automation\spirelens-mcp\run-mcp-http.ps1'
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User 'nelsonlaptop\nelsonlaptopuser'
$principal = New-ScheduledTaskPrincipal -UserId 'nelsonlaptop\nelsonlaptopuser' -LogonType Interactive -RunLevel Highest
$settings  = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -RestartInterval (New-TimeSpan -Minutes 1) -RestartCount 999 -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
$settings.ExecutionTimeLimit = 'PT0S'
Register-ScheduledTask -TaskName 'SpireLens MCP HTTP' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force
New-NetFirewallRule -DisplayName 'SpireLens MCP 15527' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 15527 -ErrorAction SilentlyContinue
[Environment]::SetEnvironmentVariable('SPIRELENS_HOST_MCP_TASK','SpireLens MCP HTTP','Machine')
Start-ScheduledTask -TaskName 'SpireLens MCP HTTP'
```

The host's `spire-lens-mcp` checkout must be current (it's run-managed and can lag);
`--auth-mode jwt` requires PR #12. To-do for full reproducibility: version-control
this launcher (e.g. `spire-lens-mcp/host/`) instead of only living on the laptop.

## What is console-managed (outside any repo)

These live in the Tailscale admin console / `romaine-life/auth`, not in this repo,
and must be set/confirmed out of band:

- **The OIDC IdP trust** in Tailscale (trusting `auth.romaine.life`, the OIDC
  client id, and which **tags** it may assign). Session pods reuse the existing
  trust; they assign `tag:spirelens-orchestrator` (the client already permits it).
- **`tagOwners`** for `tag:spirelens-host` and `tag:spirelens-orchestrator`
  (both `autogroup:admin`).
- **ACL grants.** The host only accepts what the policy allows. Current grants:
  - `tag:spirelens-orchestrator → tag:spirelens-host:22` (SSH; the glimmung-native run flow)
  - **`tag:spirelens-orchestrator → tag:spirelens-host:15527`** — required for the
    HTTP MCP.

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
