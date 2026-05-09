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
from tank_operator.sessions import (  # noqa: E402
    SESSIONS_NAMESPACE,
    SUBSCRIPTION_HEADLESS_MODE,
    PodNotReady,
    SessionInfo,
    SessionManager,
    SessionNotFound,
    SessionNotOwned,
)


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


# ---------------------------------------------------------------------------
# /api/internal/sessions/* — used by mcp-tank-operator
# ---------------------------------------------------------------------------


class _RecordingSessions:
    """SessionManager stand-in that records calls and serves canned responses.

    Internal session endpoints only depend on a few SessionManager methods
    (find_pod_by_ip, create, list, delete, set_name, set_test_state,
    dispatch_headless), so
    a focused stub gives deterministic tests without spinning up a fake
    apiserver. Each method records its kwargs into ``calls`` for assertion.
    """

    def __init__(self, pods: list[SimpleNamespace]) -> None:
        self._pods = pods
        self.calls: list[tuple[str, dict[str, object]]] = []
        self.list_response: list[SessionInfo] = []
        self.create_response: SessionInfo | None = None
        self.set_name_response: SessionInfo | None = None
        self.set_test_state_response: SessionInfo | None = None
        self.dispatch_raises: BaseException | None = None
        self.delete_raises: BaseException | None = None
        self.set_name_raises: BaseException | None = None
        self.set_test_state_raises: BaseException | None = None

    async def find_pod_by_ip(self, pod_ip: str) -> SimpleNamespace | None:
        self.calls.append(("find_pod_by_ip", {"pod_ip": pod_ip}))
        for pod in self._pods:
            if pod.status.pod_ip == pod_ip:
                return pod
        return None

    async def list(self, owner: str) -> list[SessionInfo]:
        self.calls.append(("list", {"owner": owner}))
        return self.list_response

    async def create(self, owner: str, mode: str, **kwargs: object) -> SessionInfo:
        self.calls.append(("create", {"owner": owner, "mode": mode, **kwargs}))
        return self.create_response or SessionInfo(
            id="new123",
            pod_name="session-new123",
            owner=owner,
            status="Pending",
            mode=mode,
        )

    async def delete(self, owner: str, session_id: str) -> None:
        self.calls.append(("delete", {"owner": owner, "session_id": session_id}))
        if self.delete_raises is not None:
            raise self.delete_raises

    async def set_name(
        self, owner: str, session_id: str, name: str | None
    ) -> SessionInfo:
        self.calls.append(
            ("set_name", {"owner": owner, "session_id": session_id, "name": name})
        )
        if self.set_name_raises is not None:
            raise self.set_name_raises
        return self.set_name_response or SessionInfo(
            id=session_id,
            pod_name=f"session-{session_id}",
            owner=owner,
            status="Running",
            mode=SUBSCRIPTION_HEADLESS_MODE,
            name=name,
        )

    async def set_test_state(
        self, owner: str, session_id: str, **kwargs: object
    ) -> SessionInfo:
        self.calls.append(
            ("set_test_state", {"owner": owner, "session_id": session_id, **kwargs})
        )
        if self.set_test_state_raises is not None:
            raise self.set_test_state_raises
        return self.set_test_state_response or SessionInfo(
            id=session_id,
            pod_name=f"session-{session_id}",
            owner=owner,
            status="Running",
            mode=SUBSCRIPTION_HEADLESS_MODE,
            test_state={"active": kwargs.get("active", True)},
        )

    async def dispatch_headless(self, owner: str, session_id: str, prompt: str, **kwargs) -> None:
        self.calls.append(
            (
                "dispatch_headless",
                {"owner": owner, "session_id": session_id, "prompt": prompt, **kwargs},
            )
        )
        if self.dispatch_raises is not None:
            raise self.dispatch_raises


def _build_app_with_sessions(
    pods: list[SimpleNamespace],
    sessions: _RecordingSessions | None = None,
    *,
    caller_subject: internal_api.CallerSubject | None = internal_api.CallerSubject(
        namespace="mcp-tank-operator", name="mcp-tank-operator"
    ),
) -> tuple[FastAPI, _RecordingSessions]:
    sessions = sessions or _RecordingSessions(pods)
    app = FastAPI()
    app.include_router(internal_api.build_router(sessions, _FakeProfiles({})))

    if caller_subject is None:

        async def _reject():
            raise HTTPException(status_code=403, detail="test-blocked")

        app.dependency_overrides[internal_api.authorized_caller] = _reject
    else:

        async def _allow() -> internal_api.CallerSubject:
            return caller_subject

        app.dependency_overrides[internal_api.authorized_caller] = _allow

    return app, sessions


def test_internal_create_session_resolves_caller_to_email() -> None:
    """Caller email is sourced from the pod's owner-email annotation, not
    the request body — that's the central security property keeping a
    non-host session from spawning pods owned by someone else."""
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    app, sessions = _build_app_with_sessions(pods)

    response = TestClient(app).post(
        "/api/internal/sessions",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"mode": "subscription"},
    )

    assert response.status_code == 201
    body = response.json()
    assert body["owner"] == "alice@example.test"
    assert body["mode"] == "subscription"
    assert body["url"].endswith("/?session=new123")
    create_call = next(c for c in sessions.calls if c[0] == "create")
    assert create_call[1] == {"owner": "alice@example.test", "mode": "subscription"}


def test_internal_create_session_422_when_caller_pod_unknown() -> None:
    """Brief: 'fail-open posture — if /api/internal/resolve-caller returns 404,
    don't 500.' Same shape here: a clean 422 with a user-visible message
    that the MCP tool can surface verbatim."""
    app, _ = _build_app_with_sessions(pods=[])

    response = TestClient(app).post(
        "/api/internal/sessions",
        params={"caller_pod_ip": "10.0.0.99"},
        json={"mode": "subscription"},
    )

    assert response.status_code == 422
    assert "tank-operator session pod" in response.json()["detail"]


def test_internal_create_session_rejects_unknown_mode() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    app, _ = _build_app_with_sessions(pods)

    response = TestClient(app).post(
        "/api/internal/sessions",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"mode": "definitely-not-a-mode"},
    )

    assert response.status_code == 400
    assert "unknown mode" in response.json()["detail"]


def test_internal_list_sessions_returns_owner_rows_with_urls() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.list_response = [
        SessionInfo(
            id="abc",
            pod_name="session-abc",
            owner="alice@example.test",
            status="Running",
            mode="subscription",
        )
    ]
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).get(
        "/api/internal/sessions", params={"caller_pod_ip": "10.0.0.10"}
    )

    assert response.status_code == 200
    rows = response.json()
    assert len(rows) == 1
    assert rows[0]["id"] == "abc"
    assert rows[0]["owner"] == "alice@example.test"
    assert rows[0]["url"].endswith("/?session=abc")


def test_internal_delete_session_404_when_unknown() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.delete_raises = SessionNotFound("xyz")
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).delete(
        "/api/internal/sessions/xyz", params={"caller_pod_ip": "10.0.0.10"}
    )

    assert response.status_code == 404


def test_internal_delete_session_403_when_not_owned() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.delete_raises = SessionNotOwned("xyz")
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).delete(
        "/api/internal/sessions/xyz", params={"caller_pod_ip": "10.0.0.10"}
    )

    assert response.status_code == 403


def test_internal_patch_session_sets_name() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).patch(
        "/api/internal/sessions/abc",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"name": "rollout watcher"},
    )

    assert response.status_code == 200
    set_name_call = next(c for c in sessions.calls if c[0] == "set_name")
    assert set_name_call[1] == {
        "owner": "alice@example.test",
        "session_id": "abc",
        "name": "rollout watcher",
    }


def test_internal_set_test_state_records_slot_and_url() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).post(
        "/api/internal/sessions/abc/test-state",
        params={"caller_pod_ip": "10.0.0.10"},
        json={
            "active": True,
            "slot_index": 2,
            "url": "https://tank-slot-2.tank.dev.romaine.life",
            "lease_id": "lease-123",
        },
    )

    assert response.status_code == 200
    call = next(c for c in sessions.calls if c[0] == "set_test_state")
    assert call[1] == {
        "owner": "alice@example.test",
        "session_id": "abc",
        "active": True,
        "slot_index": 2,
        "url": "https://tank-slot-2.tank.dev.romaine.life",
        "lease_id": "lease-123",
    }


def test_internal_send_message_dispatches_with_follow_up() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).post(
        "/api/internal/sessions/abc/messages",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "keep going"},
    )

    assert response.status_code == 202
    dispatch = next(c for c in sessions.calls if c[0] == "dispatch_headless")
    assert dispatch[1]["follow_up"] is True
    assert dispatch[1]["prompt"] == "keep going"


def test_internal_send_message_400_on_non_headless_mode() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.dispatch_raises = ValueError(
        "session mode 'subscription' does not support headless dispatch"
    )
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).post(
        "/api/internal/sessions/abc/messages",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "hi"},
    )

    assert response.status_code == 400


def test_internal_send_message_400_on_blank_prompt() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    app, _ = _build_app_with_sessions(pods)

    response = TestClient(app).post(
        "/api/internal/sessions/abc/messages",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "   "},
    )

    assert response.status_code == 400


def test_internal_spawn_run_creates_session_and_dispatches() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.create_response = SessionInfo(
        id="newrun",
        pod_name="session-newrun",
        owner="alice@example.test",
        status="Pending",
        mode=SUBSCRIPTION_HEADLESS_MODE,
    )
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).post(
        "/api/internal/sessions/run",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "investigate failing test"},
    )

    assert response.status_code == 202
    body = response.json()
    assert body["session"]["id"] == "newrun"
    assert body["status"] == "dispatched"
    op_names = [c[0] for c in sessions.calls]
    # Order matters: create first, then dispatch onto the new session.
    assert op_names.index("create") < op_names.index("dispatch_headless")


def test_internal_spawn_run_400_on_non_headless_mode() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    app, _ = _build_app_with_sessions(pods)

    response = TestClient(app).post(
        "/api/internal/sessions/run",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "hi", "mode": "subscription"},
    )

    assert response.status_code == 400
    assert "headless" in response.json()["detail"]


def test_internal_send_message_503_when_pod_not_ready() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    sessions = _RecordingSessions(pods)
    sessions.dispatch_raises = PodNotReady("abc")
    app, _ = _build_app_with_sessions(pods, sessions=sessions)

    response = TestClient(app).post(
        "/api/internal/sessions/abc/messages",
        params={"caller_pod_ip": "10.0.0.10"},
        json={"prompt": "hi"},
    )

    assert response.status_code == 503


def test_internal_session_endpoints_reject_unauthorized_caller() -> None:
    pods = [_pod_with_ip("session-x", "10.0.0.10", "alice@example.test")]
    app, _ = _build_app_with_sessions(pods, caller_subject=None)

    response = TestClient(app).get(
        "/api/internal/sessions", params={"caller_pod_ip": "10.0.0.10"}
    )

    assert response.status_code == 403
