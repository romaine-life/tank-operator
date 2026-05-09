from __future__ import annotations

import asyncio

import pytest

from tank_operator import api


class _FakeSessions:
    async def get_pod_name(
        self, owner: str, session_id: str, timeout: float = 90.0
    ) -> str:
        await asyncio.sleep(0.03)
        return f"session-{session_id}"


class _FakeWebSocket:
    def __init__(self) -> None:
        self.sent: list[dict[str, object]] = []

    async def send_json(self, payload: dict[str, object]) -> None:
        self.sent.append(payload)


def test_wait_for_run_pod_name_sends_keepalive_while_pending(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    ws = _FakeWebSocket()
    monkeypatch.setattr(api, "sessions", _FakeSessions())
    monkeypatch.setattr(api, "_RUN_PREFLIGHT_KEEPALIVE_SECONDS", 0.01)

    pod_name = asyncio.run(
        api._wait_for_run_pod_name(
            owner="operator@example.test",
            session_id="abc123",
            ws=ws,  # type: ignore[arg-type]
        )
    )

    assert pod_name == "session-abc123"
    assert ws.sent
    assert all(
        payload == {"keepalive": True, "phase": "waiting_for_pod"}
        for payload in ws.sent
    )


def test_run_id_validation_rejects_shell_metacharacters() -> None:
    assert api._validate_run_id("run_abc-123.ok") == "run_abc-123.ok"
    generated = api._validate_run_id("bad; rm -rf /")
    assert generated != "bad; rm -rf /"
    assert api._run_stream_path(generated).startswith("/tmp/tank-run-")


def test_tail_script_resumes_from_offset_and_waits_for_marker() -> None:
    script = api._build_tail_run_script("/tmp/tank-run-abc.stream", offset=42)
    assert "tail -c +43 -F /tmp/tank-run-abc.stream" in script
    assert api._HEADLESS_RUN_EXIT_MARKER in script


def test_headless_script_preserves_exit_status_after_prompt_cleanup() -> None:
    script = api._build_headless_script(
        provider="codex",
        prompt_path="/tmp/prompt one",
        follow_up=True,
        model="gpt-5.4",
        permission_mode="acceptEdits",
    )
    assert "rm -f '/tmp/prompt one'; (exit $rc)" in script
