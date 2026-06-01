# SpireLens MCP Access

This file is bundled into Tank sessions at
`/workspace/.tank/docs/spirelens-mcp-access.md`. It exists so an agent in a
warm session can rediscover the SpireLens host-control path without cloning
`nelsong6/tank-operator` or guessing from Glimmung-only environment variables.

## Tank Session Capability

`spirelens_mcp` is a create-time Tank session capability. A session with this
capability:

- mounts a `.mcp.json` that includes `spire-lens-mcp`
- joins the SpireLens tailnet as `tag:spirelens-orchestrator`
- exposes the host-side SpireLens MCP through `127.0.0.1:9997/mcp`
- can mint a short-lived SSH user certificate directly from auth.romaine.life

The MCP entry is local by design:

```json
{
  "mcpServers": {
    "spire-lens-mcp": {
      "type": "http",
      "url": "http://127.0.0.1:9997/mcp"
    }
  }
}
```

## SSH Path

Interactive Tank sessions do not receive `GLIMMUNG_SSH_CERT_URL`; that is only
for Glimmung run pods. A Tank session uses its projected
auth.romaine.life service-account token directly:

- token path: `/var/run/secrets/auth.romaine.life/token`
- cert endpoint: `https://auth.romaine.life/api/auth/exchange/ssh-cert`
- cert principal: `spirelens-agent`
- SSH login user: `nelsonlaptopuser`
- Tailscale socket: `/tmp/tailscaled.sock`
- SSH proxy command: `tailscale --socket=/tmp/tailscaled.sock nc %h %p`

The SSH username is the Windows account. `spirelens-agent` is the certificate
principal accepted by the host's SSH CA configuration, not the login user.

## MCP Tools

Use the Tank MCP tool `get_session_capability_context("spirelens_mcp")` for the
structured version of this document. Use `verify_spirelens_session_access()` to
inspect whether the current session has the capability, the local MCP config,
and the expected SpireLens host lifecycle tools:

- `bridge_health`
- `get_host_status`
- `start_sts2`
- `stop_sts2`
- `restart_sts2`

If the MCP server is reachable but lifecycle tools are stale or missing, use
the SSH path above to update `D:\repos\spire-lens-mcp` on the laptop and restart
the `SpireLens MCP HTTP` scheduled task.

