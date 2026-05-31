from __future__ import annotations

import asyncio
import json
from datetime import datetime, timedelta, timezone

import pytest
from aiohttp import ClientSession, ClientTimeout, web
from aiohttp.test_utils import TestClient, TestServer

from mcp_auth_proxy.server import (
    LISTENERS,
    _MAX_UPSTREAM_ATTEMPTS,
    AuthRomaineServiceProvider,
    SPIRELENS_MCP_PORT,
    _effective_listeners,
    _make_handler,
)


class _FakeResponse:
    def __init__(self, status: int, body: dict | None = None, text: str = "") -> None:
        self.status = status
        self._body = body or {}
        self._text = text

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_args):
        return False

    async def json(self) -> dict:
        return self._body

    async def text(self) -> str:
        return self._text


class _FakeHTTP:
    def __init__(self, response: _FakeResponse) -> None:
        self.response = response
        self.calls: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.calls.append({"url": url, "headers": headers, "json": json})
        return self.response


def test_auth_romaine_service_provider_exchanges_sa_token(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    expires_at = datetime.now(timezone.utc) + timedelta(minutes=15)
    http = _FakeHTTP(
        _FakeResponse(
            200,
            {
                "token": "auth-romaine-service-jwt",
                "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
            },
        )
    )
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    token = asyncio.run(provider.token())
    token_again = asyncio.run(provider.token())

    assert token == "auth-romaine-service-jwt"
    assert token_again == "auth-romaine-service-jwt"
    # Second call hits cache; only one outbound exchange.
    assert len(http.calls) == 1
    assert http.calls[0] == {
        "url": "https://auth.romaine.life/api/auth/exchange/k8s",
        "headers": {"Authorization": "Bearer pod-sa-token"},
        "json": {},
    }


def test_auth_romaine_service_provider_requires_exchange_url(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    provider = AuthRomaineServiceProvider(
        _FakeHTTP(_FakeResponse(200)),
        exchange_url="",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="AUTH_ROMAINE_EXCHANGE_URL"):
        asyncio.run(provider.token())


def test_auth_romaine_service_provider_rejects_unauthenticated_response(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    http = _FakeHTTP(_FakeResponse(401, text="upstream rejected"))
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="returned 401"):
        asyncio.run(provider.token())


def test_auth_romaine_service_provider_rejects_expired_response(tmp_path) -> None:
    # exchange responded 200 with a token whose `expires_at` is already
    # in the past — provider must refuse rather than cache+serve.
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    expired = datetime.now(timezone.utc) - timedelta(seconds=5)
    http = _FakeHTTP(
        _FakeResponse(
            200,
            {
                "token": "stale",
                "expires_at": expired.isoformat().replace("+00:00", "Z"),
            },
        )
    )
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="response was invalid"):
        asyncio.run(provider.token())


def test_effective_listeners_omit_spirelens_by_default() -> None:
    listeners = _effective_listeners("")

    assert listeners == LISTENERS
    assert all(port != SPIRELENS_MCP_PORT for port, _upstream in listeners)


def test_effective_listeners_add_spirelens_when_configured() -> None:
    upstream = "http://nelsonlaptop:15527"
    listeners = _effective_listeners(upstream)

    assert listeners[:-1] == LISTENERS
    assert listeners[-1] == (SPIRELENS_MCP_PORT, upstream)


# --- retry-loop tests ----------------------------------------------
#
# The handler exists to prevent an unrecoverable "MCP server not
# connected" state in the Claude agent SDK. The relevant failure
# modes:
#   - upstream transport error during pod rotation (no response yet,
#     safe to retry the whole call)
#   - upstream 502/503/504 returned by the kube-rbac-proxy sidecar
#     while the MCP container restarts (safe to retry; no body has
#     been streamed back to the client yet)
#   - terminal exhaustion (must return JSON-shaped 502 so the SDK's
#     JSON parser doesn't crash on a plain-text body and leave the
#     transport stuck for the rest of the session)


class _StaticTokenProvider:
    def __init__(self, token: str = "test-token") -> None:
        self._token = token

    async def token(self) -> str:
        return self._token


async def _flapping_upstream_app(status_sequence: list[int], success_body: bytes) -> tuple[web.Application, list[int]]:
    """Upstream test app that returns each status in `status_sequence`
    in order, then 200 with `success_body` from then on. Records every
    call's index so tests can assert how many attempts the proxy made."""
    call_index = [0]

    async def handler(request: web.Request) -> web.Response:
        idx = call_index[0]
        call_index[0] += 1
        if idx < len(status_sequence):
            return web.Response(status=status_sequence[idx], text=f"transient {status_sequence[idx]}")
        return web.Response(status=200, body=success_body, content_type="application/json")

    app = web.Application()
    app.router.add_route("*", "/{tail:.*}", handler)
    return app, call_index


async def _proxy_app_for(upstream_url: str, http: ClientSession) -> web.Application:
    handler = _make_handler(upstream_url, http, _StaticTokenProvider())
    app = web.Application()
    app.router.add_route("*", "/{tail:.*}", handler)
    return app


async def _run_proxy_against_upstream(status_sequence: list[int], success_body: bytes = b'{"ok":true}') -> tuple[int, str, int]:
    """Wire upstream + proxy via TestServer, POST one request through,
    return (response_status, response_text, upstream_call_count)."""
    upstream_app, call_index = await _flapping_upstream_app(status_sequence, success_body)
    upstream_server = TestServer(upstream_app)
    await upstream_server.start_server()
    try:
        http = ClientSession(timeout=ClientTimeout(total=10, sock_connect=2))
        try:
            upstream_url = f"http://{upstream_server.host}:{upstream_server.port}"
            proxy_app = await _proxy_app_for(upstream_url, http)
            proxy_server = TestServer(proxy_app)
            client = TestClient(proxy_server)
            await client.start_server()
            try:
                resp = await client.post("/mcp/some/path", data=b'{"jsonrpc":"2.0"}')
                text = await resp.text()
                return resp.status, text, call_index[0]
            finally:
                await client.close()
        finally:
            await http.close()
    finally:
        await upstream_server.close()


def test_handler_retries_transient_5xx_then_succeeds() -> None:
    # First call: 503 (kube-rbac-proxy returning transient while MCP
    # pod restarts). Second call: 200. Proxy must hide the 503 from
    # the client.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([503], success_body=b'{"jsonrpc":"2.0","result":"ok"}')
    )
    assert status == 200
    assert body == '{"jsonrpc":"2.0","result":"ok"}'
    assert calls == 2  # one failed, one succeeded


def test_handler_retries_502_then_504_then_succeeds() -> None:
    # Worst case within budget: two transient statuses then success.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([502, 504], success_body=b'{"ok":1}')
    )
    assert status == 200
    assert body == '{"ok":1}'
    assert calls == 3


def test_handler_returns_json_502_after_budget_exhausted() -> None:
    # All three attempts return 502 — must surface as a JSON-shaped
    # 502 so the SDK's MCP transport parses it cleanly instead of
    # leaving the server marked "not connected".
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([502, 502, 502])
    )
    assert status == 502
    assert calls == _MAX_UPSTREAM_ATTEMPTS == 3
    payload = json.loads(body)
    assert payload["error"] == "upstream_unavailable"
    assert payload["attempts"] == _MAX_UPSTREAM_ATTEMPTS
    assert "mcp_server" in payload


def test_handler_passes_non_transient_4xx_through_without_retry() -> None:
    # 401/403/404/etc. are real upstream answers — never retry them.
    # A 401 from the MCP means kube-rbac-proxy rejected our bearer;
    # retrying just spams the IdP.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([401])
    )
    assert status == 401
    assert calls == 1
    assert body == "transient 401"


async def _run_proxy_against_dead_upstream() -> tuple[int, str]:
    """No upstream listening at all → ClientConnectorError on every
    attempt. Must exhaust the retry budget and return JSON 502."""
    http = ClientSession(timeout=ClientTimeout(total=10, sock_connect=1))
    try:
        # Reserve a port by binding then immediately closing — the
        # subsequent connect attempts get ECONNREFUSED.
        import socket
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
        s.close()
        upstream_url = f"http://127.0.0.1:{port}"
        proxy_app = await _proxy_app_for(upstream_url, http)
        proxy_server = TestServer(proxy_app)
        client = TestClient(proxy_server)
        await client.start_server()
        try:
            resp = await client.post("/mcp/some/path", data=b"{}")
            text = await resp.text()
            return resp.status, text
        finally:
            await client.close()
    finally:
        await http.close()


def test_handler_returns_json_502_on_transport_error() -> None:
    status, body = asyncio.run(_run_proxy_against_dead_upstream())
    assert status == 502
    payload = json.loads(body)
    assert payload["error"] == "upstream_unavailable"
    assert payload["attempts"] == _MAX_UPSTREAM_ATTEMPTS
    assert "transport error" in payload["error_description"]
