"""Localhost reverse proxy that injects fresh SA-token bearer auth.

Sidecar to the claude-container in each session pod. Listens on per-MCP
localhost ports; .mcp.json points claude at these instead of at the
in-cluster MCP Services directly. Every request reads the projected
ServiceAccount token from disk and forwards upstream with
Authorization: Bearer <fresh>.

The bug this exists to fix: kubelet rotates the SA token file in-place
(eager renewal at ~50 min, well inside the default 1h TTL), but env
vars set from that file at pod start go stale. The previous wiring
exported MCP_*_BEARER in the session startup scripts, then substituted
them into .mcp.json's Authorization headers at harness startup — so any
MCP call past the 1h boundary 401'd until the session was recreated.
This proxy reads the file fresh on every request, so token rotation is
invisible to claude.

Same shape as api-proxy (the in-cluster header-injecting proxy for
api.anthropic.com), just localized to the pod because the SA token is
per-pod identity. Hardcoded LISTENERS map mirrors the entries in
k8s/session-config/mcp.json — keep them in sync; both sides describe the
same set of MCPs from opposite ends.

OAuth discovery short-circuit: Claude CLI's MCP SDK probes several
well-known paths to discover whether the server speaks OAuth:
`/.well-known/oauth-authorization-server` (RFC 8414),
`/.well-known/oauth-protected-resource` (RFC 9728),
`/.well-known/openid-configuration` (OIDC discovery), and
`POST /register` (RFC 7591 Dynamic Client Registration).
Our MCP servers don't speak OAuth — the upstream returns kube-rbac-
proxy's plain-text "Not Found", which crashes the SDK's JSON parser
("Unexpected identifier 'Not'") and leaves the connection unrecoverable
across upstream pod rotations. We answer all of those paths locally
with a JSON-shaped 404 so the SDK falls through cleanly to the bearer-
auth POST that this proxy already injects.
"""
from __future__ import annotations

import logging
from pathlib import Path

from aiohttp import ClientSession, ClientTimeout, web

log = logging.getLogger(__name__)

TOKEN_PATH = Path("/var/run/secrets/kubernetes.io/serviceaccount/token")

# (port, upstream URL). Mirrors k8s/session-config/mcp.json. Adding an
# MCP means: append here, append a port mapping in mcp.json, ship.
#
# Port allocation (next free: 9996):
#   9991 — mcp-azure-personal
#   9992 — mcp-github
#   9993 — mcp-k8s
#   9994 — mcp-argocd
#   9995 — mcp-glimmung
LISTENERS: list[tuple[int, str]] = [
    (9991, "http://mcp-azure-personal.mcp-azure.svc:80"),
    (9992, "http://mcp-github.mcp-github.svc:80"),
    (9993, "http://mcp-k8s.mcp-k8s.svc:80"),
    (9994, "http://mcp-argocd.mcp-argocd.svc:80"),
    (9995, "http://mcp-glimmung.mcp-glimmung.svc:80"),
]

# Headers we strip from the inbound request before forwarding. Host is
# rebuilt by aiohttp for the upstream; Authorization gets replaced with
# the fresh SA token; hop-by-hop and content-length are recomputed.
_STRIP_REQUEST_HEADERS = frozenset(
    {"host", "authorization", "content-length", "connection", "transfer-encoding"}
)
# Same idea on the way back — let aiohttp set framing headers on the
# response we stream to the client.
_STRIP_RESPONSE_HEADERS = frozenset(
    {"transfer-encoding", "content-encoding", "connection", "content-length"}
)


def _read_token() -> str:
    return TOKEN_PATH.read_text().strip()


# OAuth discovery paths the MCP SDK probes. RFC 8414 (auth server),
# RFC 9728 (protected resource), and OIDC discovery — the SDK tries
# all of these before/after a transport failure to decide whether OAuth
# is available. Answering locally with a JSON-shaped 404 keeps the
# SDK's parser from crashing on upstream's plain-text "Not Found" body.
_OAUTH_DISCOVERY_PATHS = (
    "/.well-known/oauth-authorization-server",
    "/.well-known/oauth-protected-resource",
    "/.well-known/openid-configuration",
)


async def _oauth_discovery_not_configured(request: web.Request) -> web.Response:
    return web.json_response(
        {
            "error": "not_found",
            "error_description": (
                "OAuth not configured on this MCP server; bearer SA-token "
                "auth is injected by the mcp-auth-proxy sidecar."
            ),
        },
        status=404,
    )


def _make_handler(upstream: str, http: ClientSession):
    upstream = upstream.rstrip("/")

    async def handler(request: web.Request) -> web.StreamResponse:
        try:
            token = _read_token()
        except OSError:
            log.exception("could not read SA token at %s", TOKEN_PATH)
            return web.Response(status=503, text="SA token unavailable")

        forwarded_headers = {
            k: v for k, v in request.headers.items() if k.lower() not in _STRIP_REQUEST_HEADERS
        }
        forwarded_headers["Authorization"] = f"Bearer {token}"

        body = await request.read()
        url = upstream + request.path_qs
        try:
            async with http.request(
                request.method,
                url,
                headers=forwarded_headers,
                data=body,
                allow_redirects=False,
            ) as upstream_resp:
                response = web.StreamResponse(
                    status=upstream_resp.status,
                    headers={
                        k: v
                        for k, v in upstream_resp.headers.items()
                        if k.lower() not in _STRIP_RESPONSE_HEADERS
                    },
                )
                await response.prepare(request)
                async for chunk in upstream_resp.content.iter_any():
                    await response.write(chunk)
                await response.write_eof()
                return response
        except Exception:
            log.exception("upstream request to %s failed", url)
            return web.Response(status=502, text="upstream request failed")

    return handler


async def run() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    # Long total timeout — MCP tool calls can be minutes. Connect timeout
    # short so a dead upstream Service surfaces fast instead of hanging
    # the user-visible MCP call.
    http = ClientSession(timeout=ClientTimeout(total=600, sock_connect=5))
    runners: list[web.AppRunner] = []
    try:
        for port, upstream in LISTENERS:
            app = web.Application()
            for discovery_path in _OAUTH_DISCOVERY_PATHS:
                app.router.add_route("GET", discovery_path, _oauth_discovery_not_configured)
            # RFC 7591 Dynamic Client Registration — also intercepted so the
            # SDK gets a JSON 404 rather than kube-rbac-proxy's plain-text one.
            app.router.add_route("POST", "/register", _oauth_discovery_not_configured)
            app.router.add_route("*", "/{tail:.*}", _make_handler(upstream, http))
            runner = web.AppRunner(app)
            await runner.setup()
            site = web.TCPSite(runner, "127.0.0.1", port)
            await site.start()
            log.info("listening on 127.0.0.1:%d → %s", port, upstream)
            runners.append(runner)
        # Park forever; container lifecycle owns us.
        import asyncio

        await asyncio.Event().wait()
    finally:
        for runner in runners:
            await runner.cleanup()
        await http.close()
