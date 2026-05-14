from __future__ import annotations

import asyncio
from datetime import datetime, timedelta, timezone

import pytest

from mcp_auth_proxy.server import TankGitHubAttestationProvider


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


def test_tank_attestation_provider_exchanges_pod_token(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-token\n", encoding="utf-8")
    expires_at = datetime.now(timezone.utc) + timedelta(minutes=5)
    http = _FakeHTTP(
        _FakeResponse(
            200,
            {
                "token": "tank-attestation",
                "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
            },
        )
    )
    provider = TankGitHubAttestationProvider(
        http,
        operator_url="http://tank-operator",
        token_path=token_path,
    )

    token = asyncio.run(provider.token())
    token_again = asyncio.run(provider.token())

    assert token == "tank-attestation"
    assert token_again == "tank-attestation"
    assert len(http.calls) == 1
    assert http.calls[0] == {
        "url": "http://tank-operator/api/internal/github/attestation",
        "headers": {"Authorization": "Bearer pod-token"},
        "json": {},
    }


def test_tank_attestation_provider_requires_operator_url(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-token\n", encoding="utf-8")
    provider = TankGitHubAttestationProvider(
        _FakeHTTP(_FakeResponse(200)),
        operator_url="",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="TANK_OPERATOR_INTERNAL_URL"):
        asyncio.run(provider.token())
