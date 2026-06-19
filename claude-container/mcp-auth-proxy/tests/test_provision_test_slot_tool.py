from __future__ import annotations

import asyncio
import json

import pytest

from mcp_auth_proxy.server import (
    CALLER_KIND_FORWARD_HEADER,
    CALLER_SESSION_ID_FORWARD_HEADER,
    CALLER_SYSTEM_FORWARD_HEADER,
    _handle_tank_provision_test_slot,
)


def _dumps(value) -> bytes:
    return json.dumps(value).encode("utf-8")


class _Resp:
    """Minimal async-context-manager response matching the shape the tool reads
    from the backend POST (status + text)."""

    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_args):
        return False

    async def text(self) -> str:
        return self._body.decode("utf-8")


class _ProvisionHTTP:
    """Captures the single backend POST the provision tool makes and returns the
    configured status/body for the internal test-workflow/start endpoint."""

    def __init__(self, *, status: int = 202, body: dict | bytes | None = None) -> None:
        self._status = status
        if body is None:
            body = {"status": "started", "repo": "romaine-life/tank-operator", "branch": "tank/session/95/tank-operator"}
        self._body = body if isinstance(body, (bytes, bytearray)) else _dumps(body)
        self.posts: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.posts.append({"url": url, "headers": headers, "json": json})
        if "test-workflow/start" in url:
            return _Resp(self._status, bytes(self._body))
        return _Resp(200, b"{}")


class _Tok:
    async def token(self) -> str:
        return "service-token"


@pytest.fixture(autouse=True)
def _session_env(monkeypatch):
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    monkeypatch.setattr("mcp_auth_proxy.server.SESSION_SCOPE", "default")
    monkeypatch.setattr(
        "mcp_auth_proxy.server.TANK_OPERATOR_INTERNAL_URL",
        "http://tank-operator.tank-operator.svc.cluster.local",
    )


def _run(http, **arguments):
    response = asyncio.run(_handle_tank_provision_test_slot(http, _Tok(), 7, arguments))
    return json.loads(response.text)


def _start_post(http) -> dict:
    return next(p for p in http.posts if "test-workflow/start" in p["url"])


def test_provision_posts_to_internal_start_with_caller_headers() -> None:
    http = _ProvisionHTTP(status=202)
    payload = _run(http)
    # Success on 202: an MCP result (text + structuredContent), no error.
    assert "result" in payload and "error" not in payload
    result = payload["result"]
    assert result["structuredContent"]["status"] == "started"
    assert result["structuredContent"]["repo"] == "romaine-life/tank-operator"
    assert result["structuredContent"]["branch"] == "tank/session/95/tank-operator"
    assert any("provision started" in block.get("text", "") for block in result["content"])

    # POSTed to THIS session's internal test-workflow/start endpoint.
    post = _start_post(http)
    assert post["url"].endswith("/api/internal/sessions/95/test-workflow/start")
    # Service-principal bearer + the caller-session forwarding headers
    # (_tank_caller_session_headers) ride the request.
    assert post["headers"]["Authorization"] == "Bearer service-token"
    assert post["headers"][CALLER_SYSTEM_FORWARD_HEADER] == "tank-operator"
    assert post["headers"][CALLER_KIND_FORWARD_HEADER] == "session"
    assert post["headers"][CALLER_SESSION_ID_FORWARD_HEADER] == "95"


def test_provision_includes_only_set_arguments() -> None:
    http = _ProvisionHTTP(status=202)
    _run(http, repo="romaine-life/glimmung", pr=42, drive=True)
    body = _start_post(http)["json"]
    assert body == {"repo": "romaine-life/glimmung", "pr": 42, "drive": True}


def test_provision_omits_unset_arguments() -> None:
    http = _ProvisionHTTP(status=202)
    _run(http)
    # No repo/pr/drive supplied -> empty body so the backend applies its defaults.
    assert _start_post(http)["json"] == {}


def test_provision_returns_error_on_4xx() -> None:
    http = _ProvisionHTTP(
        status=409,
        body=b"a test environment is already active for this session",
    )
    payload = _run(http)
    assert "error" in payload and "result" not in payload
    assert "409" in payload["error"]["message"]
    assert "already active" in payload["error"]["message"]
    assert payload["error"]["data"]["tool"] == "provision_test_slot"


def test_provision_requires_session_id(monkeypatch) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "")
    http = _ProvisionHTTP(status=202)
    payload = _run(http)
    assert "error" in payload
    assert "SESSION_ID is required" in payload["error"]["message"]
    # Nothing POSTed without an origin session.
    assert http.posts == []
