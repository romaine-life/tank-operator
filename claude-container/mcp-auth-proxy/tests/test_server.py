from __future__ import annotations

import asyncio
from datetime import datetime, timedelta, timezone

import pytest

from mcp_auth_proxy.server import AuthRomaineServiceProvider


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
