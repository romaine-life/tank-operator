"""Tests for the /api/internal/* surface (#57 stage 3).

These cover the orchestrator side of the mcp-github per-caller routing
plumbing: pod-by-IP lookup, profile join, host-flag, and SA-token
authorization. The TokenReview path itself is exercised via dependency
override because hitting a real apiserver in unit tests is overkill —
``authorized_caller`` is the only contract the production endpoint
relies on, so substituting it gives us deterministic auth states.
"""

from __future__ import annotations

import asyncio
import importlib
import os
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest
from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

# HOST_EMAIL is read at import time inside internal_api; set it before the
# import so the resolve-caller response correctly tags the host.
os.environ["HOST_EMAIL"] = "host@example.test"
os.environ["INTERNAL_API_ALLOWED_SUBJECTS"] = "mcp-github/mcp-github"

from tank_operator import internal_api  # noqa: E402  (env must be set first)
from tank_operator.profiles import Profile  # noqa: E402
from tank_operator.sessions import SESSIONS_NAMESPACE, SessionManager  # noqa: E402


def _pod_with_ip(
    name: str,
    pod_ip: str,
    owner_email: str | None,
) -> SimpleNamespace:
    annotations: dict[str, str] = {}
    if owner_email is not None:
        annotations["tank-operator/owner-email"] = owner_email
    return SimpleNamespace(
        metadata=SimpleNamespace(
            name=name,
            namespace=SESSIONS_NAMESPACE,
            annotations=annotations,
        ),
        status=SimpleNamespace(pod_ip=pod_ip),
    )


class _FakeCore:
    def __init__(self, pods: list[SimpleNamespace]) -> None:
        self._pods = pods

    async def list_namespaced_pod(self, **_kwargs: object) -> SimpleNamespace:
        return SimpleNamespace(items=self._pods)


class _FakeProfiles:
    def __init__(self, by_email: dict[str, Profile]) -> None:
        self._by_email = by_email

    async def get(self, email: str) -> Profile:
        # Mirror ProfileStore.get's get-or-create-with-default semantics.
        return self._by_email.get(email.lower(), Profile(email=email.lower()))


def _build_app(
    pods: list[SimpleNamespace],
    profiles: dict[str, Profile],
    *,
    caller_subject: internal_api.CallerSubject | None = internal_api.CallerSubject(
        namespace="mcp-github", name="mcp-github"
    ),
) -> FastAPI:
    sessions = SessionManager()
    sessions._core = _FakeCore(pods)  # noqa: SLF001 — test seam
    app = FastAPI()
    app.include_router(internal_api.build_router(sessions, _FakeProfiles(profiles)))

    if caller_subject is None:

        async def _reject():
            raise HTTPException(status_code=403, detail="test-blocked")

        app.dependency_overrides[internal_api.authorized_caller] = _reject
    else:

        async def _allow() -> internal_api.CallerSubject:
            return caller_subject

        app.dependency_overrides[internal_api.authorized_caller] = _allow

    return app


def test_find_pod_by_ip_returns_match() -> None:
    sessions = SessionManager()
    sessions._core = _FakeCore(  # noqa: SLF001
        [
            _pod_with_ip("session-a", "10.0.0.1", "alice@example.test"),
            _pod_with_ip("session-b", "10.0.0.2", "bob@example.test"),
        ]
    )

    pod = asyncio.run(sessions.find_pod_by_ip("10.0.0.2"))

    assert pod is not None
    assert pod.metadata.name == "session-b"


def test_find_pod_by_ip_returns_none_for_unknown_ip() -> None:
    sessions = SessionManager()
    sessions._core = _FakeCore(  # noqa: SLF001
        [_pod_with_ip("session-a", "10.0.0.1", "alice@example.test")]
    )

    pod = asyncio.run(sessions.find_pod_by_ip("10.0.0.99"))

    assert pod is None


def test_find_pod_by_ip_returns_none_when_core_unset() -> None:
    sessions = SessionManager()
    pod = asyncio.run(sessions.find_pod_by_ip("10.0.0.1"))
    assert pod is None


def test_resolve_caller_returns_email_and_installation_for_known_pod() -> None:
    pods = [_pod_with_ip("session-a", "10.0.0.1", "alice@example.test")]
    profiles = {
        "alice@example.test": Profile(
            email="alice@example.test", installation_id=12345
        )
    }
    app = _build_app(pods, profiles)

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.1"}
    )

    assert response.status_code == 200
    assert response.json() == {
        "email": "alice@example.test",
        "installation_id": 12345,
        "is_host": False,
        "host_email": "host@example.test",
        "pod_name": "session-a",
    }


def test_resolve_caller_flags_host_email() -> None:
    pods = [_pod_with_ip("session-host", "10.0.0.5", "Host@Example.Test")]
    profiles = {
        "host@example.test": Profile(
            email="host@example.test", installation_id=1
        )
    }
    app = _build_app(pods, profiles)

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.5"}
    )

    assert response.status_code == 200
    body = response.json()
    assert body["email"] == "host@example.test"
    assert body["is_host"] is True


def test_resolve_caller_returns_null_installation_when_profile_missing_install() -> None:
    pods = [_pod_with_ip("session-c", "10.0.0.3", "carol@example.test")]
    # No profile row → ProfileStore.get returns an empty stub. Mirror that.
    app = _build_app(pods, profiles={})

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.3"}
    )

    assert response.status_code == 200
    body = response.json()
    assert body["email"] == "carol@example.test"
    assert body["installation_id"] is None
    assert body["is_host"] is False


def test_resolve_caller_404_when_no_pod_has_ip() -> None:
    app = _build_app(pods=[], profiles={})

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.99"}
    )

    assert response.status_code == 404
    assert "no session pod" in response.json()["detail"].lower()


def test_resolve_caller_404_when_pod_missing_owner_annotation() -> None:
    pods = [_pod_with_ip("session-orphan", "10.0.0.7", owner_email=None)]
    app = _build_app(pods, profiles={})

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.7"}
    )

    assert response.status_code == 404
    assert "owner-email" in response.json()["detail"]


def test_resolve_caller_rejects_unauthorized_caller() -> None:
    pods = [_pod_with_ip("session-a", "10.0.0.1", "alice@example.test")]
    app = _build_app(pods, profiles={}, caller_subject=None)

    response = TestClient(app).get(
        "/api/internal/resolve-caller", params={"pod_ip": "10.0.0.1"}
    )

    assert response.status_code == 403


def test_authorized_caller_rejects_disallowed_sa(monkeypatch: pytest.MonkeyPatch) -> None:
    """``authorized_caller`` itself: a TokenReview-validated caller from a
    namespace not in INTERNAL_API_ALLOWED_SUBJECTS gets 403."""

    async def _stub_validator(token: str) -> internal_api.CallerSubject:
        return internal_api.CallerSubject(namespace="someone-else", name="foo")

    monkeypatch.setattr(internal_api, "_validate_sa_token", _stub_validator)

    with pytest.raises(HTTPException) as exc_info:
        asyncio.run(internal_api.authorized_caller(authorization="Bearer abc"))
    assert exc_info.value.status_code == 403
    assert "not allowed" in exc_info.value.detail.lower()


def test_authorized_caller_rejects_missing_token() -> None:
    with pytest.raises(HTTPException) as exc_info:
        asyncio.run(internal_api.authorized_caller(authorization=None))
    assert exc_info.value.status_code == 401


def test_authorized_caller_accepts_allowed_sa(monkeypatch: pytest.MonkeyPatch) -> None:
    async def _stub_validator(token: str) -> internal_api.CallerSubject:
        return internal_api.CallerSubject(namespace="mcp-github", name="mcp-github")

    monkeypatch.setattr(internal_api, "_validate_sa_token", _stub_validator)

    subject = asyncio.run(internal_api.authorized_caller(authorization="Bearer good"))
    assert subject.qualified == "mcp-github/mcp-github"


def test_authorized_caller_rejects_non_serviceaccount(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A real-user TokenReview reply (not ``system:serviceaccount:...``) is rejected."""
    # Simulate the validator raising the same HTTPException production would.
    async def _real_user(token: str) -> internal_api.CallerSubject:
        raise HTTPException(
            status_code=403, detail="caller is not a service account: nelson@example.test"
        )

    monkeypatch.setattr(internal_api, "_validate_sa_token", _real_user)

    with pytest.raises(HTTPException) as exc_info:
        asyncio.run(internal_api.authorized_caller(authorization="Bearer abc"))
    assert exc_info.value.status_code == 403


def test_validate_sa_token_passes_tank_operator_audience(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The TokenReview submitted to the apiserver must scope ``audiences`` to
    ``["tank-operator"]`` so a projected SA token minted for any other
    audience (e.g. the default cluster audience used for kubelet auth) is
    rejected. Without this, a SA token issued for one consumer could be
    replayed against this internal API.
    """
    captured: dict[str, object] = {}

    class _FakeReview:
        def __init__(self) -> None:
            self.status = type(
                "S", (), {"authenticated": True, "user": type("U", (), {"username": "system:serviceaccount:mcp-github:mcp-github"})()},
            )()

    class _FakeAuthnApi:
        def __init__(self, _client: object) -> None:
            pass

        async def create_token_review(self, *, body: object) -> _FakeReview:
            captured["body"] = body
            return _FakeReview()

    class _FakeApiClient:
        async def close(self) -> None:
            return None

    monkeypatch.setattr(internal_api.client, "ApiClient", _FakeApiClient)
    monkeypatch.setattr(internal_api.client, "AuthenticationV1Api", _FakeAuthnApi)

    subject = asyncio.run(internal_api._validate_sa_token("any-token"))

    assert subject.qualified == "mcp-github/mcp-github"
    body = captured["body"]
    assert body.spec.audiences == ["tank-operator"], (  # type: ignore[union-attr]
        f"TokenReview must request the tank-operator audience, got {body.spec.audiences!r}"  # type: ignore[union-attr]
    )


def test_authorized_caller_reloads_with_widened_allowlist(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Widening INTERNAL_API_ALLOWED_SUBJECTS via env + module reload picks up
    new callers — the env var contract documented in internal_api.py."""
    monkeypatch.setenv(
        "INTERNAL_API_ALLOWED_SUBJECTS", "mcp-github/mcp-github,mcp-azure/mcp-azure"
    )
    reloaded = importlib.reload(internal_api)

    async def _stub_validator(token: str) -> reloaded.CallerSubject:  # type: ignore[name-defined]
        return reloaded.CallerSubject(namespace="mcp-azure", name="mcp-azure")

    monkeypatch.setattr(reloaded, "_validate_sa_token", _stub_validator)
    subject = asyncio.run(reloaded.authorized_caller(authorization="Bearer good"))
    assert subject.qualified == "mcp-azure/mcp-azure"

    # Restore default so subsequent tests in the session see the original allowlist.
    monkeypatch.setenv("INTERNAL_API_ALLOWED_SUBJECTS", "mcp-github/mcp-github")
    importlib.reload(internal_api)
