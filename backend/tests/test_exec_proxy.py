from __future__ import annotations

import asyncio
import json
import sys
from pathlib import Path
from types import SimpleNamespace

import aiohttp
import pytest
from fastapi import WebSocketDisconnect

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from tank_operator import exec_proxy  # noqa: E402


class _FakeBrowser:
    def __init__(self, first_message: str | None = None) -> None:
        self.sent: list[dict[str, object]] = []
        self.closed = False
        self._first_message = first_message

    async def receive_text(self) -> str:
        if self._first_message is not None:
            message = self._first_message
            self._first_message = None
            return message
        raise WebSocketDisconnect()

    async def send_json(self, payload: dict[str, object]) -> None:
        self.sent.append(payload)

    async def close(self, code: int = 1000) -> None:
        self.closed = True


class _FakeK8sWs:
    def __init__(self) -> None:
        self.completed = False
        self.sent: list[bytes] = []

    async def __aenter__(self) -> "_FakeK8sWs":
        return self

    async def __aexit__(self, *_args: object) -> None:
        pass

    def __aiter__(self) -> "_FakeK8sWs":
        self._step = 0
        return self

    async def __anext__(self) -> SimpleNamespace:
        self._step += 1
        if self._step == 1:
            # Give the browser pump time to observe the disconnect. The
            # command stream should still continue to completion.
            await asyncio.sleep(0.01)
            return SimpleNamespace(
                type=aiohttp.WSMsgType.BINARY,
                data=bytes([exec_proxy.STDOUT_CHANNEL]) + b"still running\n",
            )
        if self._step == 2:
            self.completed = True
            return SimpleNamespace(
                type=aiohttp.WSMsgType.BINARY,
                data=bytes([exec_proxy.ERROR_CHANNEL])
                + json.dumps({"status": "Success"}).encode(),
            )
        raise StopAsyncIteration

    async def send_bytes(self, data: bytes) -> None:
        self.sent.append(data)


class _FakeWsApiClient:
    async def close(self) -> None:
        pass


def test_exec_stream_continues_after_browser_disconnect(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fake_k8s_ws = _FakeK8sWs()

    class _FakeCoreV1Api:
        def __init__(self, api_client: object) -> None:
            self.api_client = api_client

        async def connect_get_namespaced_pod_exec(self, **_kwargs: object) -> _FakeK8sWs:
            return fake_k8s_ws

    monkeypatch.setattr(exec_proxy, "WsApiClient", _FakeWsApiClient)
    monkeypatch.setattr(
        exec_proxy.client,
        "CoreV1Api",
        _FakeCoreV1Api,
    )

    asyncio.run(
        exec_proxy.exec_stream_to_websocket(
            _FakeBrowser(),
            namespace="tank-operator-sessions",
            pod_name="session-abc",
            command=["bash", "-lc", "echo hi"],
            stdin=b"",
        )
    )

    assert fake_k8s_ws.completed is True


def test_exec_launch_detached_wraps_transport_failures(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    async def _raise_transport_error(
        namespace: str, pod_name: str, command: list[str]
    ) -> bytes:
        raise aiohttp.ClientConnectionError("connect failed")

    monkeypatch.setattr(exec_proxy, "exec_capture", _raise_transport_error)

    with pytest.raises(RuntimeError, match="detached launch failed"):
        asyncio.run(
            exec_proxy.exec_launch_detached(
                namespace="tank-operator-sessions",
                pod_name="session-abc",
                command="echo hi",
                log_path="/tmp/run.stream",
            )
        )


def test_exec_stream_cancel_frame_stops_pod_stream(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    fake_k8s_ws = _FakeK8sWs()
    cancel_calls: list[tuple[str, str, list[str]]] = []

    class _FakeCoreV1Api:
        def __init__(self, api_client: object) -> None:
            self.api_client = api_client

        async def connect_get_namespaced_pod_exec(self, **_kwargs: object) -> _FakeK8sWs:
            return fake_k8s_ws

    monkeypatch.setattr(exec_proxy, "WsApiClient", _FakeWsApiClient)
    monkeypatch.setattr(
        exec_proxy.client,
        "CoreV1Api",
        _FakeCoreV1Api,
    )

    async def fake_exec_capture(
        namespace: str, pod_name: str, command: list[str]
    ) -> bytes:
        cancel_calls.append((namespace, pod_name, command))
        return b""

    monkeypatch.setattr(exec_proxy, "exec_capture", fake_exec_capture)

    asyncio.run(
        exec_proxy.exec_stream_to_websocket(
            _FakeBrowser(json.dumps({"cancel": True})),
            namespace="tank-operator-sessions",
            pod_name="session-abc",
            command=["bash", "-lc", "echo hi"],
            stdin=b"",
            cancel_command=["bash", "-lc", "cancel"],
        )
    )

    assert fake_k8s_ws.completed is False
    assert cancel_calls == [
        ("tank-operator-sessions", "session-abc", ["bash", "-lc", "cancel"])
    ]
