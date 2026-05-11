from __future__ import annotations

import asyncio
import json

import pytest

from tank_operator import api
from tank_operator.auth import User
from tank_operator.profiles import ActiveRunRecord, RunEventRecord
from tank_operator.sessions import CODEX_HEADLESS_MODE, SessionInfo


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


class _FakeActiveRuns:
    def __init__(self, record: ActiveRunRecord | None = None) -> None:
        self.record = record
        self.started_kwargs: dict[str, object] | None = None

    async def get_active(self, session_id: str) -> ActiveRunRecord | None:
        return self.record if self.record and self.record.session_id == session_id else None

    async def get_latest(self, session_id: str) -> ActiveRunRecord | None:
        return self.record if self.record and self.record.session_id == session_id else None

    async def start(self, **kwargs: object) -> ActiveRunRecord:
        self.started_kwargs = kwargs
        self.record = ActiveRunRecord(
            session_id=str(kwargs["session_id"]),
            email=str(kwargs["email"]),
            run_id=str(kwargs["run_id"]),
            pod_name=str(kwargs["pod_name"]),
            provider=str(kwargs["provider"]),
            stream_path=str(kwargs["stream_path"]),
            pid_path=str(kwargs["pid_path"]),
            started_at="2026-05-11T02:22:58.927036+00:00",
            updated_at="2026-05-11T02:22:58.927036+00:00",
        )
        return self.record


class _ActiveRunFakeSessions:
    async def get_pod_name(
        self, owner: str, session_id: str, timeout: float = 90.0
    ) -> str:
        return f"session-{session_id}"

    async def get_session(self, owner: str, session_id: str) -> SessionInfo:
        return SessionInfo(
            id=session_id,
            pod_name=f"session-{session_id}",
            owner=owner,
            status="Active",
            mode=CODEX_HEADLESS_MODE,
        )


class _FakeRunEvents:
    def __init__(self, events: list[RunEventRecord]) -> None:
        self.events = events
        self.list_after_kwargs: dict[str, object] | None = None

    async def list_after(self, **kwargs: object) -> list[RunEventRecord]:
        self.list_after_kwargs = kwargs
        limit = int(kwargs.get("limit", 100))
        return self.events[:limit]


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


def test_parse_last_event_id_ignores_invalid_values() -> None:
    assert api._parse_last_event_id(None) == 0
    assert api._parse_last_event_id("123") == 123
    assert api._parse_last_event_id("-1") == 0
    assert api._parse_last_event_id("not-an-int") == 0


def test_format_run_sse_event_shapes_eventstream_frame() -> None:
    event = RunEventRecord(
        run_id="run-1",
        session_id="abc123",
        email="operator@example.test",
        event_id=42,
        type="run.started",
        payload={"provider": "codex"},
        created_at="2026-05-11T02:22:58.927036+00:00",
    )

    frame = api._format_run_sse_event(event)

    assert frame.startswith("id: 42\nevent: run.started\n")
    assert '"run_id":"run-1"' in frame
    assert '"session_id":"abc123"' in frame
    assert '"provider":"codex"' in frame
    assert frame.endswith("\n\n")


def test_latest_run_events_uses_retained_run_pointer(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    record = ActiveRunRecord(
        session_id="abc123",
        email="operator@example.test",
        run_id="run-1",
        pod_name="session-abc123",
        provider="claude",
        status="completed",
        stream_path="/tmp/tank-run-run-1.stream",
        pid_path="/tmp/tank-run-run-1.pid",
        started_at="2026-05-11T02:22:58.927036+00:00",
        updated_at="2026-05-11T02:25:58.927036+00:00",
        completed_at="2026-05-11T02:25:58.927036+00:00",
    )
    monkeypatch.setattr(api, "sessions", _ActiveRunFakeSessions())
    monkeypatch.setattr(api, "active_runs", _FakeActiveRuns(record))
    captured: dict[str, object] = {}

    def fake_run_event_sse_stream(**kwargs: object) -> object:
        captured.update(kwargs)

        async def gen() -> object:
            if False:
                yield ""

        return gen()

    monkeypatch.setattr(api, "_run_event_sse_stream", fake_run_event_sse_stream)

    response = asyncio.run(
        api.stream_latest_run_events(
            session_id="abc123",
            last_event_id="42",
            user=User(
                sub="operator@example.test",
                email="operator@example.test",
                name="Operator",
            ),
        )
    )

    assert response.media_type == "text/event-stream"
    assert captured == {
        "session_id": "abc123",
        "run_id": "run-1",
        "after_event_id": 42,
    }


def test_latest_run_events_json_returns_bounded_event_replay(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    record = ActiveRunRecord(
        session_id="abc123",
        email="operator@example.test",
        run_id="run-1",
        pod_name="session-abc123",
        provider="claude",
        status="completed",
        stream_path="/tmp/tank-run-run-1.stream",
        pid_path="/tmp/tank-run-run-1.pid",
        started_at="2026-05-11T02:22:58.927036+00:00",
        updated_at="2026-05-11T02:25:58.927036+00:00",
        completed_at="2026-05-11T02:25:58.927036+00:00",
    )
    event = RunEventRecord(
        run_id="run-1",
        session_id="abc123",
        email="operator@example.test",
        event_id=42,
        type="run.completed",
        payload={"status": "ok"},
        created_at="2026-05-11T02:25:58.927036+00:00",
    )
    fake_run_events = _FakeRunEvents([event])
    monkeypatch.setattr(api, "sessions", _ActiveRunFakeSessions())
    monkeypatch.setattr(api, "active_runs", _FakeActiveRuns(record))
    monkeypatch.setattr(api, "run_events", fake_run_events)

    response = asyncio.run(
        api.get_latest_run_events_json(
            session_id="abc123",
            limit=5000,
            user=User(
                sub="operator@example.test",
                email="operator@example.test",
                name="Operator",
            ),
        )
    )

    assert fake_run_events.list_after_kwargs == {
        "run_id": "run-1",
        "session_id": "abc123",
        "after_event_id": 0,
        "limit": 1000,
    }
    assert response["run_id"] == "run-1"
    assert response["limit"] == 1000
    assert response["terminal_events_replayed"] == 1
    assert response["events"] == [
        {
            "run_id": "run-1",
            "session_id": "abc123",
            "event_id": 42,
            "type": "run.completed",
            "payload": {"status": "ok"},
            "created_at": "2026-05-11T02:25:58.927036+00:00",
        }
    ]


def test_run_stdout_event_observer_emits_output_and_tool_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    appended: list[dict[str, object]] = []

    async def fake_append_run_event(**kwargs: object) -> None:
        appended.append(kwargs)

    monkeypatch.setattr(api, "_append_run_event", fake_append_run_event)

    observer = api._RunStdoutEventObserver(
        email="operator@example.test",
        session_id="abc123",
        run_id="run-1",
        provider="claude",
    )
    assistant = {
        "type": "assistant",
        "message": {
            "content": [
                {"type": "text", "text": "checking"},
                {"type": "tool_use", "id": "toolu_1", "name": "Read"},
            ]
        },
    }
    user = {
        "type": "user",
        "message": {
            "content": [
                {"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}
            ]
        },
    }

    async def run() -> None:
        await observer.observe_stdout(json.dumps(assistant) + "\n")
        await observer.observe_stdout(json.dumps(user) + "\n")

    asyncio.run(run())

    assert [event["event_type"] for event in appended] == [
        "run.output.started",
        "run.tool.started",
        "run.message.created",
        "run.tool.completed",
    ]
    assert appended[1]["payload"] == {"tool_use_id": "toolu_1", "name": "Read"}
    assert appended[2]["payload"] == {
        "message_id": "assistant-1",
        "role": "assistant",
        "text": "checking",
        "source": "claude",
    }
    assert appended[3]["payload"] == {"tool_use_id": "toolu_1", "output": "ok"}


def test_run_stdout_event_observer_buffers_partial_json_lines(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    appended: list[dict[str, object]] = []

    async def fake_append_run_event(**kwargs: object) -> None:
        appended.append(kwargs)

    monkeypatch.setattr(api, "_append_run_event", fake_append_run_event)

    observer = api._RunStdoutEventObserver(
        email="operator@example.test",
        session_id="abc123",
        run_id="run-1",
        provider="claude",
    )
    event = {
        "type": "assistant",
        "message": {
            "content": [{"type": "tool_use", "id": "toolu_2", "name": "Bash"}]
        },
    }
    payload = json.dumps(event) + "\n"

    async def run() -> None:
        await observer.observe_stdout(payload[:20])
        await observer.observe_stdout(payload[20:])

    asyncio.run(run())

    assert [event["event_type"] for event in appended] == [
        "run.output.started",
        "run.tool.started",
    ]
    assert appended[1]["payload"] == {"tool_use_id": "toolu_2", "name": "Bash"}


def test_run_stdout_event_observer_skips_tool_parse_for_codex(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    appended: list[dict[str, object]] = []

    async def fake_append_run_event(**kwargs: object) -> None:
        appended.append(kwargs)

    monkeypatch.setattr(api, "_append_run_event", fake_append_run_event)

    observer = api._RunStdoutEventObserver(
        email="operator@example.test",
        session_id="abc123",
        run_id="run-1",
        provider="codex",
    )

    asyncio.run(
        observer.observe_stdout(
            json.dumps(
                {
                    "type": "assistant",
                    "message": {
                        "content": [
                            {"type": "tool_use", "id": "toolu_1", "name": "Read"}
                        ]
                    },
                }
            )
            + "\n"
        )
    )

    assert [event["event_type"] for event in appended] == ["run.output.started"]


def test_run_stdout_event_observer_emits_user_and_result_messages(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    appended: list[dict[str, object]] = []

    async def fake_append_run_event(**kwargs: object) -> None:
        appended.append(kwargs)

    monkeypatch.setattr(api, "_append_run_event", fake_append_run_event)

    observer = api._RunStdoutEventObserver(
        email="operator@example.test",
        session_id="abc123",
        run_id="run-1",
        provider="claude",
    )
    user = {
        "type": "user",
        "uuid": "user-msg-1",
        "timestamp": "2026-05-11T04:05:00Z",
        "message": {"content": "hello"},
    }
    result = {
        "type": "result",
        "uuid": "result-msg-1",
        "result": "done",
    }
    duplicate_result = {
        "type": "result",
        "uuid": "result-msg-2",
        "result": "done",
    }

    async def run() -> None:
        await observer.observe_stdout(json.dumps(user) + "\n")
        await observer.observe_stdout(json.dumps(result) + "\n")
        await observer.observe_stdout(json.dumps(duplicate_result) + "\n")

    asyncio.run(run())

    assert [event["event_type"] for event in appended] == [
        "run.output.started",
        "run.message.created",
        "run.message.created",
    ]
    assert appended[1]["payload"] == {
        "message_id": "user-msg-1",
        "role": "user",
        "text": "hello",
        "source": "claude",
        "time": "2026-05-11T04:05:00Z",
    }
    assert appended[2]["payload"] == {
        "message_id": "result-msg-1",
        "role": "assistant",
        "text": "done",
        "source": "claude",
    }


def test_check_active_run_on_pod_uses_specific_registry_run(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    captured: dict[str, object] = {}

    async def fake_exec_capture(
        namespace: str, pod_name: str, command: list[str]
    ) -> bytes:
        captured["namespace"] = namespace
        captured["pod_name"] = pod_name
        captured["command"] = command
        return b"run_abc 42\n"

    monkeypatch.setattr(api, "exec_capture", fake_exec_capture)

    result = asyncio.run(api._check_active_run_on_pod("session-abc", "run_abc"))

    assert result == ("run_abc", 42)
    command = captured["command"]
    assert isinstance(command, list)
    assert "/tmp/tank-run-run_abc.pid" in command[-1]
    assert "ls -t /tmp/tank-run-*.pid" not in command[-1]


def test_get_active_run_returns_registry_started_at(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    started_at = "2026-05-11T02:21:53.142530+00:00"
    record = ActiveRunRecord(
        session_id="abc123",
        email="operator@example.test",
        run_id="run_abc",
        pod_name="session-abc123",
        provider="codex",
        stream_path="/tmp/tank-run-run_abc.stream",
        pid_path="/tmp/tank-run-run_abc.pid",
        started_at=started_at,
        updated_at=started_at,
    )

    async def fake_check_active_run_on_pod(
        pod_name: str, run_id: str | None = None
    ) -> tuple[str, int] | None:
        assert pod_name == "session-abc123"
        assert run_id == "run_abc"
        return ("run_abc", 42)

    monkeypatch.setattr(api, "sessions", _ActiveRunFakeSessions())
    monkeypatch.setattr(api, "active_runs", _FakeActiveRuns(record))
    monkeypatch.setattr(api, "_check_active_run_on_pod", fake_check_active_run_on_pod)

    result = asyncio.run(
        api.get_active_run(
            "abc123",
            user=User(
                sub="user",
                email="operator@example.test",
                name="Operator",
            ),
        )
    )

    assert result is not None
    assert result.run_id == "run_abc"
    assert result.stream_offset == 42
    assert result.started_at == started_at


def test_get_active_run_returns_backfilled_started_at(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    async def fake_check_active_run_on_pod(
        pod_name: str, run_id: str | None = None
    ) -> tuple[str, int] | None:
        assert pod_name == "session-abc123"
        assert run_id is None
        return ("run_backfill", 99)

    active_runs = _FakeActiveRuns()
    monkeypatch.setattr(api, "sessions", _ActiveRunFakeSessions())
    monkeypatch.setattr(api, "active_runs", active_runs)
    monkeypatch.setattr(api, "_check_active_run_on_pod", fake_check_active_run_on_pod)

    result = asyncio.run(
        api.get_active_run(
            "abc123",
            user=User(
                sub="user",
                email="operator@example.test",
                name="Operator",
            ),
        )
    )

    assert result is not None
    assert result.run_id == "run_backfill"
    assert result.stream_offset == 99
    assert result.started_at == "2026-05-11T02:22:58.927036+00:00"


def test_check_active_run_on_pod_rejects_malicious_registry_run() -> None:
    result = asyncio.run(api._check_active_run_on_pod("session-abc", "../../bad"))

    assert result is None
