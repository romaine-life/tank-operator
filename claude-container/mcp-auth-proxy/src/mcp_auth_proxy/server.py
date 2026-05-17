"""Localhost reverse proxy that injects fresh bearer auth.

Sidecar to the claude-container in each session pod. Listens on per-MCP
localhost ports; .mcp.json points claude at these instead of at the
in-cluster MCP Services directly. Most MCPs still receive the projected
ServiceAccount token. GitHub MCP receives a short-lived Tank session
attestation minted from a separate audience-scoped pod token.

The bug this exists to fix: kubelet rotates the SA token file in-place
(eager renewal at ~50 min, well inside the default 1h TTL), but env
vars set from that file at pod start go stale. The previous wiring
exported MCP_*_BEARER in the session startup scripts, then substituted
them into .mcp.json's Authorization headers at harness startup â€” so any
MCP call past the 1h boundary 401'd until the session was recreated.
This proxy reads the file fresh on every request, so token rotation is
invisible to claude.

Same shape as api-proxy (the in-cluster header-injecting proxy for
api.anthropic.com), just localized to the pod because the SA token is
per-pod identity. Hardcoded LISTENERS map mirrors the entries in
k8s/session-config/mcp.json â€” keep them in sync; both sides describe the
same set of MCPs from opposite ends.

OAuth discovery short-circuit: Claude CLI's MCP SDK probes several
well-known paths to discover whether the server speaks OAuth:
`/.well-known/oauth-authorization-server` (RFC 8414),
`/.well-known/oauth-protected-resource` (RFC 9728),
`/.well-known/openid-configuration` (OIDC discovery), and
`POST /register` (RFC 7591 Dynamic Client Registration).
Our MCP servers don't speak OAuth. A plain-text upstream 404 crashes
the SDK's JSON parser
("Unexpected identifier 'Not'") and leaves the connection unrecoverable
across upstream pod rotations. We answer all of those paths locally
with a JSON-shaped 404 so the SDK falls through cleanly to the bearer-
auth POST that this proxy already injects.
"""
from __future__ import annotations

import asyncio
import logging
import os
import time
from datetime import datetime
from pathlib import Path

from aiohttp import ClientSession, ClientTimeout, web

from .metrics import (
    record_auth_romaine_exchange,
    record_github_attestation,
    record_proxy_request,
    record_sa_token_read,
    start_metrics_server,
    upstream_timer,
)

log = logging.getLogger(__name__)

SA_TOKEN_PATH = Path("/var/run/secrets/kubernetes.io/serviceaccount/token")
TANK_ATTESTATION_TOKEN_PATH = Path(
    os.environ.get(
        "TANK_SESSION_ATTESTATION_TOKEN_PATH",
        "/var/run/secrets/tank-operator/token",
    )
)
TANK_OPERATOR_INTERNAL_URL = os.environ.get("TANK_OPERATOR_INTERNAL_URL", "").rstrip("/")
GITHUB_MCP_PORT = 9992
TANK_OPERATOR_MCP_PORT = 9996

# auth.romaine.life service-principal exchange (see
# nelsong6/tank-operator#486). The session pod mounts a projected SA
# token with `audience: https://auth.romaine.life` at this path; this
# sidecar POSTs it to AUTH_ROMAINE_EXCHANGE_URL and receives a JWT with
# role=service that downstream tank-operator endpoints accept.
AUTH_ROMAINE_SA_TOKEN_PATH = Path(
    os.environ.get(
        "AUTH_ROMAINE_SA_TOKEN_PATH",
        "/var/run/secrets/auth.romaine.life/token",
    )
)
AUTH_ROMAINE_EXCHANGE_URL = os.environ.get(
    "AUTH_ROMAINE_EXCHANGE_URL",
    "https://auth.romaine.life/api/auth/exchange/k8s",
).rstrip("/")
# Header name shared with mcp-tank-operator's CallerIdentityMiddleware.
# Changing it requires a cross-repo coordinated deploy.
AUTH_ROMAINE_FORWARD_HEADER = "X-Auth-Romaine-Token"

# (port, upstream URL). Mirrors k8s/session-config/mcp.json. Adding an
# MCP means: append here, append a port mapping in mcp.json, ship.
#
# Port allocation (next free: 9997):
#   9991 â€” mcp-azure-personal
#   9992 â€” mcp-github
#   9993 â€” mcp-k8s
#   9994 â€” mcp-argocd
#   9995 â€” mcp-glimmung
#   9996 â€” mcp-tank-operator
LISTENERS: list[tuple[int, str]] = [
    (9991, "http://mcp-azure-personal.mcp-azure-personal.svc:80"),
    (9992, "http://mcp-github.mcp-github.svc:80"),
    (9993, "http://mcp-k8s.mcp-k8s.svc:80"),
    (9994, "http://mcp-argocd.mcp-argocd.svc:80"),
    (9995, "http://mcp-glimmung.mcp-glimmung.svc:80"),
    (9996, "http://mcp-tank-operator.mcp-tank-operator.svc:80"),
]

# Headers we strip from the inbound request before forwarding. Host is
# rebuilt by aiohttp for the upstream; Authorization gets replaced with
# the fresh SA token; hop-by-hop and content-length are recomputed.
_STRIP_REQUEST_HEADERS = frozenset(
    {"host", "authorization", "content-length", "connection", "transfer-encoding"}
)
# Same idea on the way back â€” let aiohttp set framing headers on the
# response we stream to the client.
_STRIP_RESPONSE_HEADERS = frozenset(
    {"transfer-encoding", "content-encoding", "connection", "content-length"}
)


def _read_token(path: Path) -> str:
    return path.read_text().strip()


class ServiceAccountTokenProvider:
    def __init__(self, token_path: Path = SA_TOKEN_PATH) -> None:
        self._token_path = token_path

    async def token(self) -> str:
        try:
            value = _read_token(self._token_path)
        except Exception:
            record_sa_token_read("failure")
            raise
        record_sa_token_read("success")
        return value


class TankGitHubAttestationProvider:
    def __init__(
        self,
        http: ClientSession,
        *,
        operator_url: str = TANK_OPERATOR_INTERNAL_URL,
        token_path: Path = TANK_ATTESTATION_TOKEN_PATH,
        refresh_skew_seconds: float = 30.0,
    ) -> None:
        self._http = http
        self._operator_url = operator_url.rstrip("/")
        self._token_path = token_path
        self._refresh_skew_seconds = refresh_skew_seconds
        self._cached_token = ""
        self._expires_at = 0.0
        self._lock = asyncio.Lock()

    async def token(self) -> str:
        now = time.time()
        if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
            record_github_attestation("cache_hit")
            return self._cached_token
        async with self._lock:
            now = time.time()
            if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
                record_github_attestation("cache_hit")
                return self._cached_token
            if not self._operator_url:
                record_github_attestation("exception")
                raise RuntimeError("TANK_OPERATOR_INTERNAL_URL is required for GitHub MCP auth")
            try:
                pod_token = _read_token(self._token_path)
                async with self._http.post(
                    f"{self._operator_url}/api/internal/github/attestation",
                    headers={"Authorization": f"Bearer {pod_token}"},
                    json={},
                ) as response:
                    if response.status != 200:
                        detail = (await response.text())[:300]
                        record_github_attestation("http_error")
                        raise RuntimeError(
                            f"Tank GitHub MCP attestation request returned {response.status}: {detail}"
                        )
                    body = await response.json()
            except RuntimeError:
                raise
            except Exception:
                record_github_attestation("exception")
                raise
            token = str(body.get("token") or "")
            expires_at = _parse_expires_at(body.get("expires_at"))
            if not token or expires_at <= time.time():
                record_github_attestation("invalid_response")
                raise RuntimeError("Tank GitHub MCP attestation response was invalid")
            self._cached_token = token
            self._expires_at = expires_at
            record_github_attestation("success")
            return token


class AuthRomaineServiceProvider:
    """Exchanges the pod's auth.romaine.life-audience projected SA token
    for a `role=service` JWT via auth.romaine.life's
    /api/auth/exchange/k8s. Caches the JWT until ~30s before expiry.

    Used to inject the X-Auth-Romaine-Token header on outbound calls to
    mcp-tank-operator (port 9996), enabling its spawn_service_session
    tool. See nelsong6/tank-operator#486.
    """

    def __init__(
        self,
        http: ClientSession,
        *,
        exchange_url: str = AUTH_ROMAINE_EXCHANGE_URL,
        token_path: Path = AUTH_ROMAINE_SA_TOKEN_PATH,
        refresh_skew_seconds: float = 30.0,
    ) -> None:
        self._http = http
        self._exchange_url = exchange_url
        self._token_path = token_path
        self._refresh_skew_seconds = refresh_skew_seconds
        self._cached_token = ""
        self._expires_at = 0.0
        self._lock = asyncio.Lock()

    async def token(self) -> str:
        now = time.time()
        if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
            record_auth_romaine_exchange("cache_hit")
            return self._cached_token
        async with self._lock:
            now = time.time()
            if self._cached_token and self._expires_at > now + self._refresh_skew_seconds:
                record_auth_romaine_exchange("cache_hit")
                return self._cached_token
            if not self._exchange_url:
                record_auth_romaine_exchange("exception")
                raise RuntimeError(
                    "AUTH_ROMAINE_EXCHANGE_URL is required for auth.romaine.life exchange"
                )
            try:
                sa_token = _read_token(self._token_path)
                async with self._http.post(
                    self._exchange_url,
                    headers={"Authorization": f"Bearer {sa_token}"},
                    json={},
                ) as response:
                    if response.status != 200:
                        detail = (await response.text())[:300]
                        record_auth_romaine_exchange("http_error")
                        raise RuntimeError(
                            f"auth.romaine.life exchange returned {response.status}: {detail}"
                        )
                    body = await response.json()
            except RuntimeError:
                raise
            except Exception:
                record_auth_romaine_exchange("exception")
                raise
            token = str(body.get("token") or "")
            expires_at = _parse_expires_at(body.get("expires_at"))
            if not token or expires_at <= time.time():
                record_auth_romaine_exchange("invalid_response")
                raise RuntimeError("auth.romaine.life exchange response was invalid")
            self._cached_token = token
            self._expires_at = expires_at
            record_auth_romaine_exchange("success")
            return token


def _parse_expires_at(value: object) -> float:
    if isinstance(value, (int, float)):
        return float(value)
    if not isinstance(value, str) or not value:
        return 0.0
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        return datetime.fromisoformat(text).timestamp()
    except ValueError:
        return 0.0


# OAuth discovery paths the MCP SDK probes. RFC 8414 (auth server),
# RFC 9728 (protected resource), and OIDC discovery â€” the SDK tries
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
                "OAuth not configured on this MCP server; bearer "
                "auth is injected by the mcp-auth-proxy sidecar."
            ),
        },
        status=404,
    )


def _mcp_server_label(upstream: str) -> str:
    """Extract a bounded label from the upstream URL. Example:
    'http://mcp-azure-personal.mcp-azure-personal.svc:80' → 'mcp-azure-personal'.
    The fallback is the full host string, which is still bounded by the
    LISTENERS map but less Grafana-friendly. Cardinality is the count
    of distinct upstreams (~6), never per-request.
    """
    host = upstream.replace("http://", "").replace("https://", "")
    name = host.split(".", 1)[0]
    return name or host or "unknown"


def _make_handler(
    upstream: str,
    http: ClientSession,
    token_provider,
    *,
    extra_header_provider=None,
):
    """Build the request handler for an MCP upstream.

    `extra_header_provider`, when supplied, is awaited per request to
    obtain an additional header value injected on the way out (today:
    X-Auth-Romaine-Token for mcp-tank-operator). A None return skips
    injection so the upstream sees the request without the extra header.
    An exception in the provider is logged at INFO but does NOT fail
    the request — the upstream still receives the normal Bearer-authed
    call, will reject any service-principal-gated route with 401, and
    the caller surfaces the error end-to-end. See
    nelsong6/tank-operator#486.
    """
    upstream = upstream.rstrip("/")
    mcp_label = _mcp_server_label(upstream)

    async def handler(request: web.Request) -> web.StreamResponse:
        try:
            token = await token_provider.token()
        except Exception:
            log.exception("could not load bearer token for %s", upstream)
            record_proxy_request(mcp_label, 503)
            return web.Response(status=503, text="bearer token unavailable")

        forwarded_headers = {
            k: v for k, v in request.headers.items() if k.lower() not in _STRIP_REQUEST_HEADERS
        }
        forwarded_headers["Authorization"] = f"Bearer {token}"

        if extra_header_provider is not None:
            try:
                name, value = await extra_header_provider()
            except Exception:
                # Non-fatal: the upstream will reject any service-
                # principal-gated route with 401 (post-#486 there is no
                # acceptance shape other than the auth.romaine.life
                # service JWT this header carries). Logged at INFO
                # because the exchange failure rate is already tracked
                # via the dedicated counter (auth.romaine.life
                # exchange) and a duplicate WARN here would just spam.
                log.info(
                    "extra-header provider failed for %s; forwarding without it",
                    upstream,
                    exc_info=True,
                )
            else:
                if name and value:
                    forwarded_headers[name] = value

        body = await request.read()
        url = upstream + request.path_qs
        try:
            async with upstream_timer(mcp_label):
                async with http.request(
                    request.method,
                    url,
                    headers=forwarded_headers,
                    data=body,
                    allow_redirects=False,
                ) as upstream_resp:
                    status = upstream_resp.status
                    response = web.StreamResponse(
                        status=status,
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
            record_proxy_request(mcp_label, status)
            return response
        except Exception:
            log.exception("upstream request to %s failed", url)
            record_proxy_request(mcp_label, 502)
            return web.Response(status=502, text="upstream request failed")

    return handler


async def run() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    # Long total timeout â€” MCP tool calls can be minutes. Connect timeout
    # short so a dead upstream Service surfaces fast instead of hanging
    # the user-visible MCP call.
    http = ClientSession(timeout=ClientTimeout(total=600, sock_connect=5))
    runners: list[web.AppRunner] = []
    # Bind metrics on a separate port so the PodMonitor scrape doesn't
    # clash with the localhost-only MCP listeners. 9990 sits just below
    # the LISTENERS port range (9991+) so the entire observability +
    # MCP-proxy port block reads contiguously.
    metrics_port = int(os.environ.get("MCP_AUTH_PROXY_METRICS_PORT", "9990"))
    metrics_runner = await start_metrics_server(metrics_port)
    runners.append(metrics_runner)
    # Shared across mcp-tank-operator's outbound calls so the cached
    # JWT (15-min TTL) is reused across many tool calls in a session.
    auth_romaine_provider = AuthRomaineServiceProvider(http)

    try:
        for port, upstream in LISTENERS:
            app = web.Application()
            for discovery_path in _OAUTH_DISCOVERY_PATHS:
                app.router.add_route("GET", discovery_path, _oauth_discovery_not_configured)
            # RFC 7591 Dynamic Client Registration â€” also intercepted so the
            # SDK gets a JSON 404 rather than an upstream plain-text one.
            app.router.add_route("POST", "/register", _oauth_discovery_not_configured)
            if port == GITHUB_MCP_PORT:
                token_provider = TankGitHubAttestationProvider(http)
            else:
                token_provider = ServiceAccountTokenProvider()

            # mcp-tank-operator gets the auth.romaine.life service JWT
            # forwarded so its session-management tools can authenticate
            # to /api/internal/sessions/* . Other MCPs are unaffected.
            extra_header_provider = None
            if port == TANK_OPERATOR_MCP_PORT:
                async def _provide_auth_romaine_header(
                    provider=auth_romaine_provider,
                ) -> tuple[str, str]:
                    return AUTH_ROMAINE_FORWARD_HEADER, await provider.token()
                extra_header_provider = _provide_auth_romaine_header

            app.router.add_route(
                "*",
                "/{tail:.*}",
                _make_handler(
                    upstream,
                    http,
                    token_provider,
                    extra_header_provider=extra_header_provider,
                ),
            )
            runner = web.AppRunner(app)
            await runner.setup()
            site = web.TCPSite(runner, "127.0.0.1", port)
            await site.start()
            log.info("listening on 127.0.0.1:%d â†’ %s", port, upstream)
            runners.append(runner)
        # Park forever; container lifecycle owns us.
        await asyncio.Event().wait()
    finally:
        for runner in runners:
            await runner.cleanup()
        await http.close()
