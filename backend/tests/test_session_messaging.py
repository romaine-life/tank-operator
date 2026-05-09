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
from tank_operator.api import (
    _build_tail_run_script,
    _validate_run_id,
)
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
        mode="claude_gui",
    )

    env = _claude_env(manifest)
    assert "TANK_OPERATOR_URL" not in env
    assert "TANK_API_TOKEN" not in env
    assert "TANK_SESSION_ID" not in env


class _DispatchFakeManager(SessionManager):
    """SessionManager stub that captures dispatch_headless's pod-side calls."""

    def __init__(self, *, mode: str, active_runs: Any | None = None) -> None:
        super().__init__(active_runs=active_runs)
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


class _FakeActiveRuns:
    def __init__(self) -> None:
        self.started: list[dict[str, Any]] = []

    async def start(self, **kwargs: Any) -> None:
        self.started.append(kwargs)


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
    active_runs = _FakeActiveRuns()
    manager = _DispatchFakeManager(
        mode=SUBSCRIPTION_HEADLESS_MODE, active_runs=active_runs
    )

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
    assert log_path.startswith("/tmp/tank-run-")
    assert log_path.endswith(".stream")
    assert path in script
    assert "/tmp/tank-run-" in script
    assert ".pid" in script
    assert " true " in script  # follow_up flag splice
    assert "/opt/tank/headless-run.sh" in script
    assert "claude" in script  # claude_gui → claude provider
    assert active_runs.started
    assert active_runs.started[0]["email"] == "operator@example.test"
    assert active_runs.started[0]["session_id"] == "abc123"
    assert active_runs.started[0]["pod_name"] == "session-abc123"
    assert active_runs.started[0]["provider"] == "claude"
    assert active_runs.started[0]["stream_path"] == log_path
    assert active_runs.started[0]["pid_path"].startswith("/tmp/tank-run-")


def test_dispatch_headless_codex_provider(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    manager = _DispatchFakeManager(mode="codex_gui")
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


def test_codex_gui_runner_resumes_on_follow_up() -> None:
    runner = Path(__file__).resolve().parents[2] / "k8s/session-config/headless-run.sh"
    script = runner.read_text()

    assert 'follow_up = sys.argv[2] == "true"' in script
    assert 'args.extend(["resume", "--last"])' in script
    assert 'args.extend(["--model", model])' in script


def test_codex_gui_runner_mirrors_json_stream_to_history() -> None:
    runner = Path(__file__).resolve().parents[2] / "k8s/session-config/headless-run.sh"
    script = runner.read_text()

    assert 'history_path = "/tmp/tank-run-history.ndjson"' in script
    assert '"type": "tank.user_message"' in script
    assert "history.write(stamped_line(line) + \"\\n\")" in script
    assert "history_line_buffer" in script
    assert "pty.spawn(args, master_read=master_read)" in script


# ---------------------------------------------------------------------------
# session_run resume path — helper-level coverage
#
# The WebSocket endpoint itself requires a live k8s cluster to integration-
# test, so we verify the two helpers that encode the resume logic:
#   - _validate_run_id: determines whether the client's run_id is used or a
#     fresh one is minted (reconnects would break if ids diverge silently).
#   - _build_tail_run_script with offset > 0: the tail start byte must be
#     offset+1 so a reconnect replays exactly the bytes the browser missed.
# ---------------------------------------------------------------------------


def test_resume_run_id_passes_through_when_valid() -> None:
    client_id = "550e8400-e29b-41d4-a716-446655440000"
    assert _validate_run_id(client_id) == client_id


def test_resume_run_id_sanitised_when_malicious() -> None:
    bad = "../../etc/passwd"
    result = _validate_run_id(bad)
    assert result != bad
    assert "/" not in result


def test_resume_tail_offset_zero_reads_whole_file() -> None:
    script = _build_tail_run_script("/tmp/run.stream", offset=0)
    assert "tail -c +1 " in script


def test_resume_tail_offset_nonzero_skips_already_seen_bytes() -> None:
    script = _build_tail_run_script("/tmp/run.stream", offset=4096)
    assert "tail -c +4097 " in script


def test_resume_tail_offset_is_clamped_to_one_when_negative() -> None:
    script = _build_tail_run_script("/tmp/run.stream", offset=-999)
    assert "tail -c +1 " in script


def test_resume_prompt_is_not_required_for_tail_script() -> None:
    # The tail script takes only stream_path and offset — no prompt — so
    # a reconnect never re-stages or re-runs the underlying command.
    import inspect
    sig = inspect.signature(_build_tail_run_script)
    assert "prompt" not in sig.parameters


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
