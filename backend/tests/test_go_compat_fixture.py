from __future__ import annotations

import json
from pathlib import Path

from tank_operator.api import _run_pid_path, _run_stream_path, _validate_run_id
from tank_operator.profiles import (
    ActiveRunRecord,
    SessionRecord,
    _active_run_doc,
    _session_counter_doc_id,
    _session_doc,
    _session_doc_id,
)
from tank_operator.sessions import SessionManager, _owner_label, normalize_session_mode


FIXTURE = (
    Path(__file__).resolve().parents[2]
    / "backend-go/internal/compat/testdata/python_compat.json"
)


def _load_fixture() -> dict:
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def test_go_compat_fixture_matches_python_source_of_truth() -> None:
    fixture = _load_fixture()

    for raw, expected in fixture["mode_aliases"].items():
        assert normalize_session_mode(raw) == expected

    for row in fixture["owner_labels"]:
        assert _owner_label(row["email"]) == row["label"]

    for run_id in fixture["run_ids"]["valid"]:
        assert _validate_run_id(run_id) == run_id
    for run_id in fixture["run_ids"]["invalid"]:
        assert _validate_run_id(run_id) != run_id

    for row in fixture["run_paths"]:
        assert _run_stream_path(row["run_id"]) == row["stream_path"]
        assert _run_pid_path(row["run_id"]) == row["pid_path"]

    for row in fixture["session_doc_ids"]:
        assert _session_doc_id(row["scope"], row["session_id"]) == row["doc_id"]
        assert _session_counter_doc_id(row["scope"]) == row["counter_id"]

    name = fixture["session_doc"]["name"]
    assert _session_doc(
        SessionRecord(
            id="12",
            email="USER@example.COM",
            mode="claude_cli",
            scope="default",
            pod_name="session-12",
            name=name,
            visible=True,
            requested_at="2026-05-11T00:00:00+00:00",
            created_at="2026-05-11T00:00:01+00:00",
            updated_at="2026-05-11T00:00:02+00:00",
        )
    ) == fixture["session_doc"]

    assert _active_run_doc(
        ActiveRunRecord(
            session_id="12",
            email="USER@example.COM",
            run_id="run_12",
            pod_name="session-12",
            provider="codex",
            status="running",
            stream_path=_run_stream_path("run_12"),
            pid_path=_run_pid_path("run_12"),
            started_at="2026-05-11T00:01:00+00:00",
            updated_at="2026-05-11T00:01:01+00:00",
            completed_at=None,
        )
    ) == fixture["active_run_doc"]

    core = fixture["pod_manifest_core"]
    manifest = SessionManager()._pod_manifest(
        core["input"]["session_id"],
        owner=core["input"]["owner"],
        mode=core["input"]["mode"],
    )
    containers = manifest["spec"]["containers"]
    assert {
        "input": core["input"],
        "metadata": manifest["metadata"],
        "service_account": manifest["spec"]["serviceAccountName"],
        "security_context": manifest["spec"]["securityContext"],
        "container_names": [container["name"] for container in containers],
        "container_images": {
            container["name"]: container["image"] for container in containers
        },
        "claude_command": next(
            container for container in containers if container["name"] == "claude"
        )["command"],
        "claude_env": {
            env["name"]: env["value"]
            for env in next(
                container for container in containers if container["name"] == "claude"
            )["env"]
        },
    } == core
