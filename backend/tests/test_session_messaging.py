"""Tests for the session-to-session messaging surface.

Covers:
- Pod manifest does not include the removed legacy mcp-tank callback env.
- SessionManager.dispatch_headless rejects non-headless modes and shapes the
  launcher command correctly.
"""
from __future__ import annotations

import asyncio
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from tank_operator import sessions as sessions_module
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


def test_pod_manifest_omits_legacy_mcp_tank_callback_env() -> None:
    manifest = SessionManager()._pod_manifest(
        "abc123",
        owner="operator@example.test",
        mode="subscription_headless",
    )

    env = _claude_env(manifest)
    assert "TANK_OPERATOR_URL" not in env
    assert "TANK_API_TOKEN" not in env
    assert "TANK_SESSION_ID" not in env


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
