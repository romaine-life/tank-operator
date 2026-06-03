"""Localhost reverse proxy that injects fresh bearer auth.

Sidecar to the claude-container in each session pod. Listens on per-MCP
localhost ports; .mcp.json points claude at these instead of at the
in-cluster MCP Services directly. Most MCPs receive the projected
ServiceAccount token; mcp-github and mcp-tank-operator receive an
auth.romaine.life-issued role=service JWT via the AuthRomaineService
exchange.

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

from aiohttp import ClientError, ClientSession, ClientTimeout, web

from .metrics import (
    record_auth_romaine_exchange,
    record_proxy_request,
    record_proxy_retry,
    record_sa_token_read,
    start_metrics_server,
    upstream_timer,
)

# Bounded retry budget for transient upstream failures (transport
# errors, 502/503/504). The unrecoverable failure mode this exists to
# prevent: aiohttp.ClientError or a 502 returned to the Claude agent
# SDK lands a plain-text body that crashes the SDK's JSON parser, the
# server gets marked "not connected", and no further calls are
# attempted until the session restarts. Mirrors the OAuth-discovery
# JSON-404 fix that already lives at the top of this file — same
# class of unrecoverable SDK state, different trigger. See
# romaine-life/tank-operator#... (this PR).
#
# Cap kept tight (3 attempts over ~1.3s total) so a genuinely dead
# upstream surfaces fast, while a normal pod-rotation window (typically
# 100-800ms) is invisible to the SDK.
_MAX_UPSTREAM_ATTEMPTS = 3
_RETRY_BACKOFF_SECONDS = (0.1, 0.3)
_TRANSIENT_UPSTREAM_STATUSES = (502, 503, 504)


def _retry_delay(attempt_index: int) -> float:
    """Return the sleep duration before retry `attempt_index + 1`.
    attempt_index is 0-indexed; the first retry sleeps
    _RETRY_BACKOFF_SECONDS[0], the second sleeps [1], etc. Anything
    beyond the table length re-uses the last value."""
    if attempt_index < 0:
        return 0.0
    if attempt_index >= len(_RETRY_BACKOFF_SECONDS):
        return _RETRY_BACKOFF_SECONDS[-1]
    return _RETRY_BACKOFF_SECONDS[attempt_index]


def _json_upstream_error(status: int, reason: str, *, mcp_label: str, attempts: int) -> web.Response:
    """Terminal upstream failure response. JSON-shaped so the Claude
    agent SDK's MCP transport parses it cleanly instead of landing on
    a plain-text body that leaves the connection unrecoverable across
    the session lifetime."""
    return web.json_response(
        {
            "error": "upstream_unavailable",
            "error_description": reason,
            "mcp_server": mcp_label,
            "attempts": attempts,
        },
        status=status,
    )

log = logging.getLogger(__name__)

SA_TOKEN_PATH = Path("/var/run/secrets/kubernetes.io/serviceaccount/token")
GITHUB_MCP_PORT = 9992
GLIMMUNG_MCP_PORT = 9995
TANK_OPERATOR_MCP_PORT = 9996
SPIRELENS_MCP_PORT = 9997

# Optional tailnet upstream: the SpireLens game-host MCP (spire-lens-mcp's
# server.py --transport http). Unlike the in-cluster .svc upstreams below it
# lives on the Tailscale tailnet (tag:spirelens-host), so its requests are
# routed through tailscaled's userspace outbound HTTP proxy (TAILNET_HTTP_PROXY)
# and authenticated with the pod's auth.romaine.life service JWT (the server
# validates it with --auth-mode jwt). The listener is added only when
# SPIRELENS_MCP_UPSTREAM is set (e.g. http://nelsonlaptop:15527), so a pod that
# never joins the tailnet does not expose a dead port. See
# docs/tailnet-host-access.md.
SPIRELENS_MCP_UPSTREAM = (os.environ.get("SPIRELENS_MCP_UPSTREAM") or "").strip()
# CONNECT proxy into the tailnet (tailscaled --outbound-http-proxy-listen).
# Applied ONLY to the SpireLens upstream, passed per-request rather than via the
# HTTP_PROXY env so the in-cluster .svc upstreams keep reaching cluster IPs
# directly (aiohttp would otherwise proxy every request).
TAILNET_HTTP_PROXY = (os.environ.get("TAILNET_HTTP_PROXY") or "").strip() or None

# auth.romaine.life service-principal exchange (see
# romaine-life/tank-operator#486). The session pod mounts a projected SA
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

# Originating tank-operator session id forwarded on outbound calls to
# mcp-tank-operator. Set from this pod's SESSION_ID env var (sourced
# from the `tank-operator/session-id` Pod label). mcp-tank-operator
# threads it into ORIGIN_SESSION_ID and forwards it on to the
# orchestrator, which stamps it onto the persisted user_message.created
# event so the frontend renders the parent session's avatar on the
# user bubble in the target session. Empty env (e.g. local dev without
# the downward-API mount) is fine — the header is omitted and the
# orchestrator falls back to the human-Gravatar rendering. Header name
# shared with mcp-tank-operator/src/mcp_tank_operator/caller.py and
# tank-operator/backend-go/cmd/tank-operator/handlers_internal.go;
# changing it requires a coordinated cross-repo deploy.
ORIGIN_SESSION_FORWARD_HEADER = "X-Tank-Origin-Session-Id"
ORIGIN_SESSION_ID = (os.environ.get("SESSION_ID") or "").strip()

# (port, upstream URL). Mirrors k8s/session-config/mcp.json. Adding an
# MCP means: append here, append a port mapping in mcp.json, ship.
#
# Port allocation (next free: 9998):
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


def _effective_listeners(spirelens_upstream: str = SPIRELENS_MCP_UPSTREAM) -> list[tuple[int, str]]:
    listeners = list(LISTENERS)
    upstream = (spirelens_upstream or "").strip()
    if upstream:
        listeners.append((SPIRELENS_MCP_PORT, upstream))
    return listeners

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


class AuthRomaineServiceProvider:
    """Exchanges the pod's auth.romaine.life-audience projected SA token
    for a `role=service` JWT via auth.romaine.life's
    /api/auth/exchange/k8s. Caches the JWT until ~30s before expiry.

    Used to inject the X-Auth-Romaine-Token header on outbound calls to
    mcp-tank-operator (port 9996), enabling its spawn_service_session
    tool. See romaine-life/tank-operator#486.
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
    static_headers=None,
    proxy: str | None = None,
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
    romaine-life/tank-operator#486.

    `static_headers`, when supplied, is a mapping of header-name → value
    injected verbatim on every outbound request to this upstream.
    Synchronous and per-process-constant — used for identity inputs
    sourced from the pod environment that don't change at runtime
    (today: X-Tank-Origin-Session-Id from SESSION_ID). Empty or None
    values are omitted so the upstream sees the request without the
    header, matching the orchestrator's "fall back to human Gravatar"
    behavior when the field is absent.
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

        if static_headers:
            for name, value in static_headers.items():
                if name and value:
                    forwarded_headers[name] = value

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

        # Bounded-retry loop. Two failure modes are retried because the
        # SDK has no recovery for them within a session:
        #   - aiohttp.ClientError before we start streaming (upstream
        #     pod rotation: connection refused / reset / DNS flap).
        #   - HTTP 502/503/504 from the upstream (kube-rbac-proxy
        #     sidecar in front of the MCP returns 502 briefly while
        #     the MCP container restarts).
        # Anything past response.prepare() is mid-stream; we surface
        # the broken stream rather than mask a real regression.
        last_failure_reason = "all retry attempts failed"

        for attempt in range(_MAX_UPSTREAM_ATTEMPTS):
            started_streaming = False
            try:
                async with upstream_timer(mcp_label):
                    async with http.request(
                        request.method,
                        url,
                        headers=forwarded_headers,
                        data=body,
                        allow_redirects=False,
                        proxy=proxy,
                    ) as upstream_resp:
                        status = upstream_resp.status

                        # Transient upstream statuses get handled
                        # entirely inside this branch BEFORE we call
                        # response.prepare() — once streaming starts
                        # we can't fail back into the loop without
                        # leaving the SDK to parse a truncated body,
                        # which is the exact unrecoverable state this
                        # whole change exists to prevent.
                        if status in _TRANSIENT_UPSTREAM_STATUSES:
                            # Drain so the connection returns to the
                            # pool cleanly rather than getting closed.
                            await upstream_resp.read()
                            record_proxy_retry(mcp_label, "transient_status")
                            last_failure_reason = (
                                f"upstream returned transient status {status}"
                            )
                            if attempt < _MAX_UPSTREAM_ATTEMPTS - 1:
                                log.info(
                                    "upstream %s returned %d on attempt %d/%d; retrying",
                                    url,
                                    status,
                                    attempt + 1,
                                    _MAX_UPSTREAM_ATTEMPTS,
                                )
                                await asyncio.sleep(_retry_delay(attempt))
                                continue
                            # Final attempt was still transient: do
                            # NOT pass the upstream's body through —
                            # an upstream "Bad Gateway" plain-text
                            # body would crash the SDK's JSON parser
                            # just as badly as a transport error.
                            # Fall through to the exhaustion path
                            # below.
                            log.warning(
                                "upstream %s returned %d on final attempt %d/%d",
                                url,
                                status,
                                attempt + 1,
                                _MAX_UPSTREAM_ATTEMPTS,
                            )
                            break

                        response = web.StreamResponse(
                            status=status,
                            headers={
                                k: v
                                for k, v in upstream_resp.headers.items()
                                if k.lower() not in _STRIP_RESPONSE_HEADERS
                            },
                        )
                        await response.prepare(request)
                        started_streaming = True
                        async for chunk in upstream_resp.content.iter_any():
                            await response.write(chunk)
                        await response.write_eof()
                record_proxy_request(mcp_label, status)
                return response
            except ClientError as exc:
                if started_streaming:
                    # Mid-stream drop. The wire is already committed
                    # to a partial response; let it surface as the
                    # broken stream it is rather than mask a real
                    # upstream regression with a confusing retry that
                    # writes JSON on top of partial bytes.
                    log.warning(
                        "upstream %s dropped mid-stream on attempt %d: %r",
                        url,
                        attempt + 1,
                        exc,
                    )
                    record_proxy_request(mcp_label, 502)
                    raise
                record_proxy_retry(mcp_label, "transport_error")
                last_failure_reason = f"transport error: {exc!r}"
                if attempt >= _MAX_UPSTREAM_ATTEMPTS - 1:
                    log.warning(
                        "upstream request to %s failed after %d attempts: %r",
                        url,
                        attempt + 1,
                        exc,
                    )
                    break
                log.info(
                    "upstream request to %s failed on attempt %d/%d (%r); retrying",
                    url,
                    attempt + 1,
                    _MAX_UPSTREAM_ATTEMPTS,
                    exc,
                )
                await asyncio.sleep(_retry_delay(attempt))

        # Retry budget exhausted. Return a JSON-shaped 502 so the SDK's
        # MCP transport parser doesn't crash on a plain-text body and
        # leave the connection unrecoverable for the rest of the
        # session — same shape as the OAuth discovery 404 short-circuit
        # at the top of this file.
        record_proxy_retry(mcp_label, "exhausted")
        record_proxy_request(mcp_label, 502)
        return _json_upstream_error(
            502,
            last_failure_reason,
            mcp_label=mcp_label,
            attempts=_MAX_UPSTREAM_ATTEMPTS,
        )

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
    # Shared across mcp-tank-operator and mcp-github outbound calls so
    # the cached JWT (15-min TTL) is reused across many tool calls in a
    # session.
    auth_romaine_provider = AuthRomaineServiceProvider(http)

    # The SpireLens game-host MCP is a tailnet upstream, added only when
    # configured (SPIRELENS_MCP_UPSTREAM) and reached through the tailscaled
    # outbound HTTP proxy. See docs/tailnet-host-access.md.
    effective_listeners = _effective_listeners()
    if SPIRELENS_MCP_UPSTREAM:
        if not TAILNET_HTTP_PROXY:
            log.warning(
                "SPIRELENS_MCP_UPSTREAM set but TAILNET_HTTP_PROXY is empty; the "
                "tailnet upstream on :%d will be unreachable until the pod joins "
                "the tailnet and exposes its outbound HTTP proxy",
                SPIRELENS_MCP_PORT,
            )

    try:
        for port, upstream in effective_listeners:
            app = web.Application()
            for discovery_path in _OAUTH_DISCOVERY_PATHS:
                app.router.add_route("GET", discovery_path, _oauth_discovery_not_configured)
            # RFC 7591 Dynamic Client Registration â€” also intercepted so the
            # SDK gets a JSON 404 rather than an upstream plain-text one.
            app.router.add_route("POST", "/register", _oauth_discovery_not_configured)
            if port in (GITHUB_MCP_PORT, SPIRELENS_MCP_PORT):
                # Both authenticate with the auth.romaine.life service JWT as
                # the bearer. mcp-github verifies it against the IdP's JWKS and
                # resolves the caller's GitHub App installation by calling
                # tank-operator's /api/internal/github/installation with the
                # same bearer forwarded; the SpireLens game-host MCP validates
                # it directly with --auth-mode jwt.
                token_provider = auth_romaine_provider
            else:
                token_provider = ServiceAccountTokenProvider()

            # mcp-tank-operator and mcp-glimmung both gate their tool
            # surface on the caller's auth.romaine.life service JWT (read
            # from X-Auth-Romaine-Token because Authorization is consumed
            # by kube-rbac-proxy in front of each, which strips it before
            # forwarding upstream). Inject the header so the upstreams
            # can attribute every call to the originating user.
            extra_header_provider = None
            if port in (TANK_OPERATOR_MCP_PORT, GLIMMUNG_MCP_PORT):
                async def _provide_auth_romaine_header(
                    provider=auth_romaine_provider,
                ) -> tuple[str, str]:
                    return AUTH_ROMAINE_FORWARD_HEADER, await provider.token()
                extra_header_provider = _provide_auth_romaine_header

            # Tell mcp-tank-operator which session pod is calling so it
            # can forward the originating session id to the orchestrator,
            # which stamps it onto user_message.created events and lets
            # the frontend render the parent session's avatar on the
            # user bubble in the target session. Only meaningful for the
            # tank-operator handoff path (send_prompt / spawn_run_session)
            # — other upstreams ignore the header. SESSION_ID is sourced
            # from the pod's downward-API env var; when unset we omit
            # the header and the orchestrator falls back to the
            # human-Gravatar rendering.
            static_headers = None
            if port == TANK_OPERATOR_MCP_PORT and ORIGIN_SESSION_ID:
                static_headers = {ORIGIN_SESSION_FORWARD_HEADER: ORIGIN_SESSION_ID}

            # The SpireLens upstream is on the tailnet; route it through the
            # tailscaled outbound HTTP proxy. Every other upstream is an
            # in-cluster .svc and must connect directly (proxy=None).
            request_proxy = TAILNET_HTTP_PROXY if port == SPIRELENS_MCP_PORT else None

            app.router.add_route(
                "*",
                "/{tail:.*}",
                _make_handler(
                    upstream,
                    http,
                    token_provider,
                    extra_header_provider=extra_header_provider,
                    static_headers=static_headers,
                    proxy=request_proxy,
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
