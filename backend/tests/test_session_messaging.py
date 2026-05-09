"""Tests for the session-to-session messaging surface.

Covers:
- Pod manifest gets TANK_OPERATOR_URL / TANK_API_TOKEN / TANK_SESSION_ID env
  when the orchestrator can mint a JWT for the owner.
- mint_session_token_for_email round-trips through _decode_session_token.
- SessionManager.dispatch_headless rejects non-headless modes and shapes the
  launcher command correctly.
"""
from __future__ import annotations

import asyncio
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from tank_operator import auth, sessions as sessions_module
from tank_operator.sessions import (
    HEADLESS_MODES,
    SessionInfo,
    SessionManager,
    SUBSCRIPTION_HEADLESS_MODE,
)


def _claude_env(manifest: dict) -> dict[str, str]:
    claude = next(
        c for c in manifest["spec"]["containers"] if c["name"] == "claude"
    )
    return {e["name"]: e["value"] for e in claude["env"]}


def test_pod_manifest_injects_orchestrator_url_and_session_token() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription_headless",
        api_token="signed-token",
    )

    env = _claude_env(manifest)
    assert env["TANK_API_TOKEN"] == "signed-token"
    assert env["TANK_SESSION_ID"] == "abc123"
    assert env["TANK_OPERATOR_URL"].startswith("http")


def test_pod_manifest_omits_token_when_orchestrator_cannot_mint(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # Pods still boot when the orchestrator's JWT_SECRET isn't configured.
    # The mcp-tank stdio server reads TANK_API_TOKEN at call time and
    # surfaces an actionable error if it's empty.
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription_headless",
        api_token=None,
    )
    assert _claude_env(manifest)["TANK_API_TOKEN"] == ""


def test_mint_session_token_round_trips_to_user(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(auth, "JWT_SECRET", "test-secret")
    monkeypatch.setattr(
        auth, "ALLOWED_EMAILS", frozenset({"operator@example.test"})
    )

    token = auth.mint_session_token_for_email(
        "Operator@Example.Test", sub="pod:session-abc"
    )
    user = auth._decode_session_token(token)
    assert user.email == "operator@example.test"
    assert user.sub == "pod:session-abc"


def test_mint_session_token_rejects_unknown_email(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(auth, "JWT_SECRET", "test-secret")
    monkeypatch.setattr(
        auth, "ALLOWED_EMAILS", frozenset({"operator@example.test"})
    )
    with pytest.raises(auth.HTTPException) as exc:
        auth.mint_session_token_for_email("intruder@example.test")
    assert exc.value.status_code == 403


class _DispatchFakeManager(SessionManager):
    """SessionManager stub that captures dispatch_headless's pod-side calls."""

    def __init__(self, *, mode: str) -> None:
        super().__init__()
        self._mode = mode
        self.captured_command: list[str] | None = None
        self.captured_prompt: bytes | None = None
        self.captured_path: str | None = None

    async def get_session(self, owner: str, session_id: str) -> SessionInfo:
        return SessionInfo(
            id=session_id,
            pod_name=f"session-{session_id}",
            owner=owner,
            status="Active",
            mode=self._mode,
        )

    async def get_pod_name(
        self, owner: str, session_id: str, timeout: float = 90.0
    ) -> str:
        return f"session-{session_id}"


async def _fake_write_file(
    namespace: str, pod_name: str, path: str, data: bytes
) -> None:
    _fake_write_file.captured = (namespace, pod_name, path, data)  # type: ignore[attr-defined]


async def _fake_capture(
    namespace: str, pod_name: str, command: list[str]
) -> bytes:
    _fake_capture.captured = (namespace, pod_name, command)  # type: ignore[attr-defined]
    return b""


async def _fake_launch_detached(
    namespace: str, pod_name: str, command: str, log_path: str
) -> None:
    _fake_launch_detached.captured = (  # type: ignore[attr-defined]
        namespace,
        pod_name,
        command,
        log_path,
    )


def test_dispatch_headless_rejects_non_headless_mode() -> None:
    manager = _DispatchFakeManager(mode="subscription")
    with pytest.raises(ValueError):
        asyncio.run(
            manager.dispatch_headless(
                owner="operator@example.test",
                session_id="abc123",
                prompt="hi",
                follow_up=False,
                model="",
                permission_mode="",
            )
        )


def test_dispatch_headless_writes_prompt_and_backgrounds_command(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    manager = _DispatchFakeManager(mode=SUBSCRIPTION_HEADLESS_MODE)

    # exec_proxy is imported lazily inside dispatch_headless; patch the
    # module attributes so the late import picks up our stubs.
    from tank_operator import exec_proxy

    monkeypatch.setattr(exec_proxy, "exec_write_file", _fake_write_file)
    monkeypatch.setattr(exec_proxy, "exec_launch_detached", _fake_launch_detached)

    asyncio.run(
        manager.dispatch_headless(
            owner="operator@example.test",
            session_id="abc123",
            prompt="ship it",
            follow_up=True,
            model="",
            permission_mode="",
        )
    )

    _, pod_name, path, data = _fake_write_file.captured  # type: ignore[attr-defined]
    assert pod_name == "session-abc123"
    assert path.startswith("/tmp/tank-prompt-")
    assert data == b"ship it"

    _, _, script, log_path = _fake_launch_detached.captured  # type: ignore[attr-defined]
    assert log_path.startswith("/tmp/tank-headless-")
    assert path in script
    assert " true " in script  # follow_up flag splice
    assert "/opt/tank/headless-run.sh" in script
    assert "claude" in script  # subscription_headless → claude provider


def test_dispatch_headless_codex_provider(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    manager = _DispatchFakeManager(mode="codex_headless")
    from tank_operator import exec_proxy

    monkeypatch.setattr(exec_proxy, "exec_write_file", _fake_write_file)
    monkeypatch.setattr(exec_proxy, "exec_launch_detached", _fake_launch_detached)

    asyncio.run(
        manager.dispatch_headless(
            owner="operator@example.test",
            session_id="abc123",
            prompt="codex prompt",
            follow_up=False,
            model="",
            permission_mode="",
        )
    )
    script = _fake_launch_detached.captured[2]  # type: ignore[attr-defined]
    assert " codex " in script
    assert " false " in script


def test_codex_headless_runner_resumes_on_follow_up() -> None:
    runner = Path(__file__).resolve().parents[2] / "k8s/session-config/headless-run.sh"
    script = runner.read_text()

    assert 'follow_up = sys.argv[2] == "true"' in script
    assert 'args.extend(["resume", "--last"])' in script
    assert 'args.extend(["--model", model])' in script


def test_codex_headless_runner_mirrors_json_stream_to_history() -> None:
    runner = Path(__file__).resolve().parents[2] / "k8s/session-config/headless-run.sh"
    script = runner.read_text()

    assert 'history_path = "/tmp/tank-run-history.ndjson"' in script
    assert '"type": "tank.user_message"' in script
    assert "history.buffer.write(data)" in script
    assert "pty.spawn(args, master_read=master_read)" in script


def test_dispatch_headless_validates_model_and_permission_mode(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    manager = _DispatchFakeManager(mode=SUBSCRIPTION_HEADLESS_MODE)
    from tank_operator import exec_proxy

    monkeypatch.setattr(exec_proxy, "exec_write_file", _fake_write_file)
    monkeypatch.setattr(exec_proxy, "exec_launch_detached", _fake_launch_detached)

    with pytest.raises(ValueError):
        asyncio.run(
            manager.dispatch_headless(
                owner="operator@example.test",
                session_id="abc123",
                prompt="x",
                follow_up=False,
                model="haiku; rm -rf /",  # space + ; → fails the regex
                permission_mode="",
            )
        )
