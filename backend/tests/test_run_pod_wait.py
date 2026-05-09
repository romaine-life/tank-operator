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
