"""Unit tests for the headless-run script builder helpers in api.py.

Covers:
- _validate_run_id: valid IDs pass through; invalid/None get a hex fallback.
- _build_live_run_script: normal exit path includes PID cleanup + exit marker;
  SIGTERM trap includes the same writes so cancel doesn't leave the tail
  script hanging.
- _build_tail_run_script: offset maps to the correct tail -c start byte; the
  30-second deadline command is present; exit code extraction is present.
- _build_cancel_run_command: pkill/kill structure targets the right pid file.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
from pathlib import Path

import pytest

# internal_api.py caches HOST_EMAIL and ALLOWED_CALLER_SUBJECTS at module
# import time. This file is collected alphabetically before test_internal_api
# and triggers the first import of tank_operator.internal_api (via
# tank_operator.api). Prime the env vars to the same values test_internal_api
# expects so the cached module state is compatible with both test files.
os.environ.setdefault("HOST_EMAIL", "host@example.test")
os.environ.setdefault("INTERNAL_API_ALLOWED_SUBJECTS", "mcp-github/mcp-github")

from tank_operator.api import (
    _HEADLESS_RUN_EXIT_MARKER,
    _build_cancel_run_command,
    _build_headless_script,
    _build_live_run_script,
    _build_tail_run_script,
    _skill_prompt,
    _skill_trigger,
    _validate_skill_name,
    _validate_run_id,
)
from tank_operator import api as api_module


# ---------------------------------------------------------------------------
# _validate_run_id
# ---------------------------------------------------------------------------


def test_validate_run_id_passes_through_valid_uuid() -> None:
    run_id = "550e8400-e29b-41d4-a716-446655440000"
    assert _validate_run_id(run_id) == run_id


def test_validate_run_id_passes_through_alphanumeric() -> None:
    run_id = "abc123XYZ"
    assert _validate_run_id(run_id) == run_id


def test_validate_run_id_passes_through_dots_and_dashes() -> None:
    run_id = "run.v1-abc_def"
    assert _validate_run_id(run_id) == run_id


def test_validate_run_id_rejects_none() -> None:
    result = _validate_run_id(None)
    assert re.match(r"^[0-9a-f]{24}$", result), f"expected 24-char hex token, got {result!r}"


def test_validate_run_id_rejects_empty_string() -> None:
    result = _validate_run_id("")
    assert re.match(r"^[0-9a-f]{24}$", result)


def test_validate_run_id_rejects_shell_metacharacters() -> None:
    result = _validate_run_id("abc; rm -rf /")
    assert result != "abc; rm -rf /"
    assert re.match(r"^[0-9a-f]{24}$", result)


def test_validate_run_id_rejects_too_long() -> None:
    long_id = "a" * 81
    result = _validate_run_id(long_id)
    assert result != long_id
    assert re.match(r"^[0-9a-f]{24}$", result)


# ---------------------------------------------------------------------------
# skill invocation helpers
# ---------------------------------------------------------------------------


def test_validate_skill_name_accepts_simple_names() -> None:
    assert _validate_skill_name("test") == "test"
    assert _validate_skill_name("rollout_v2") == "rollout_v2"


def test_validate_skill_name_rejects_paths_and_shell() -> None:
    assert _validate_skill_name("../test") == ""
    assert _validate_skill_name("test;echo nope") == ""


def test_skill_trigger_is_provider_specific() -> None:
    assert _skill_trigger("codex", "test") == "$test"
    assert _skill_trigger("claude", "test") == "/test"


def test_skill_prompt_includes_supplemental_text() -> None:
    assert _skill_prompt("codex", "test", "Set up env for issue 42") == (
        "$test\n\nSet up env for issue 42"
    )


def test_skill_prompt_without_supplemental_text_is_trigger_only() -> None:
    assert _skill_prompt("claude", "test", "  ") == "/test"


def test_headless_script_passes_skill_name() -> None:
    script = _build_headless_script(
        provider="codex",
        prompt_path="/tmp/prompt",
        follow_up=False,
        model="",
        permission_mode="",
        skill_name="test",
    )
    assert "/opt/tank/headless-run.sh codex /tmp/prompt false '' '' test" in script


# ---------------------------------------------------------------------------
# _build_live_run_script
# ---------------------------------------------------------------------------


def test_live_run_script_records_pid() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "echo $$ >" in script


def test_live_run_script_normal_path_removes_pid_file() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "rm -f" in script


def test_live_run_script_normal_path_writes_exit_marker() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert _HEADLESS_RUN_EXIT_MARKER in script


def test_live_run_script_records_nonzero_exit_in_history() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "tank.run_started" in script
    assert "tank.run_failed" in script
    assert "Agent process exited with status %s" in script
    assert "diagnostics_path" in script
    assert "operator_pod" in script
    assert "operator_started_at" in script
    assert "_tank_record_failure \"$rc\"" in script
    assert "/tmp/tank-run-history.ndjson" in script


def test_live_run_script_writes_failure_diagnostics_bundle() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "/workspace/.tank-diagnostics" in script
    assert "latest_core" in script
    assert "core_files" in script
    assert "operator_pod" in script
    assert "operator_started_at" in script
    assert "node --version" in script
    assert "codex --version" in script
    assert "recent_history" in script


def test_live_run_script_names_common_transport_signals() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "1) signal=SIGHUP" in script
    assert "13) signal=SIGPIPE" in script
    assert "15) signal=SIGTERM" in script


def test_live_run_script_records_failure_before_exit_marker() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    record_pos = script.index("_tank_record_failure \"$rc\"")
    marker_pos = script.rindex(_HEADLESS_RUN_EXIT_MARKER)
    assert record_pos < marker_pos


def test_live_run_script_decodes_pty_negative_signal_exits(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    history_path = tmp_path / "history.ndjson"
    diagnostics_dir = tmp_path / "diagnostics"
    pid_path = tmp_path / "run.pid"

    monkeypatch.setattr(api_module, "_HEADLESS_RUN_HISTORY_PATH", str(history_path))
    monkeypatch.setattr(
        api_module, "_HEADLESS_RUN_DIAGNOSTICS_DIR", str(diagnostics_dir)
    )

    script = api_module._build_live_run_script(
        "python3 -c 'import sys; sys.exit(-11)'", str(pid_path)
    )
    result = subprocess.run(
        ["bash", "-lc", script],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )

    assert result.returncode == 245
    events = [json.loads(line) for line in history_path.read_text().splitlines()]
    assert events[0]["type"] == "tank.run_started"
    assert events[0]["pid_path"] == str(pid_path)
    failed = events[1]
    assert failed["type"] == "tank.run_failed"
    assert failed["exit_code"] == 245
    assert failed["signal"] == "SIGSEGV"
    assert "operator_pod" in failed
    assert "operator_started_at" in failed
    diagnostics_path = Path(failed["diagnostics_path"])
    assert diagnostics_path.parent == diagnostics_dir
    diagnostics = diagnostics_path.read_text()
    assert "signal=SIGSEGV" in diagnostics
    assert "operator_pod=" in diagnostics
    assert "operator_started_at=" in diagnostics


def test_live_run_script_sigterm_trap_writes_exit_marker() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    # The trap must reference the marker variable so cancellation writes it.
    assert "_tank_marker" in script
    # The marker variable must be set before the trap is registered.
    trap_pos = script.index("trap ")
    marker_var_pos = script.index("_tank_marker=")
    assert marker_var_pos < trap_pos, "marker variable must be set before the trap"


def test_live_run_script_sigterm_trap_removes_pid_file() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "_tank_pid" in script
    assert "rm -f" in script


def test_live_run_script_sigterm_trap_exits() -> None:
    script = _build_live_run_script("echo hi", "/tmp/test.pid")
    assert "exit $rc" in script


def test_live_run_script_embeds_inner_script() -> None:
    inner = "bash /opt/tank/headless-run.sh claude /tmp/prompt false '' ''"
    script = _build_live_run_script(inner, "/tmp/run.pid")
    assert inner in script


# ---------------------------------------------------------------------------
# _build_tail_run_script
# ---------------------------------------------------------------------------


def test_tail_run_script_offset_zero_starts_at_byte_one() -> None:
    script = _build_tail_run_script("/tmp/run.stream", offset=0)
    assert "tail -c +1 " in script


def test_tail_run_script_offset_n_starts_at_byte_n_plus_one() -> None:
    script = _build_tail_run_script("/tmp/run.stream", offset=100)
    assert "tail -c +101 " in script


def test_tail_run_script_offset_negative_treated_as_zero() -> None:
    # max(1, offset+1) with offset=-5 → max(1, -4) = 1
    script = _build_tail_run_script("/tmp/run.stream", offset=-5)
    assert "tail -c +1 " in script


def test_tail_run_script_has_file_wait_loop() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    assert "while [ ! -f" in script


def test_tail_run_script_has_30s_deadline() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    assert "deadline=$((SECONDS+30))" in script
    assert "SECONDS -lt $deadline" in script


def test_tail_run_script_waits_for_exit_marker() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    assert _HEADLESS_RUN_EXIT_MARKER in script


def test_tail_run_script_extracts_exit_code() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    assert "sed -n" in script
    assert "exit" in script


def test_tail_run_script_kills_tail_background_process() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    assert "kill" in script
    assert "tail_pid" in script


def test_tail_run_script_deletes_stream_file_after_read() -> None:
    script = _build_tail_run_script("/tmp/run.stream")
    # rm -f must appear after sed extracts the exit code so the file is still
    # present when sed reads it, then cleaned up before exit.
    assert "rm -f" in script
    rm_pos = script.rindex("rm -f")
    sed_pos = script.index("sed -n")
    assert sed_pos < rm_pos, "sed must extract exit code before rm -f deletes the file"


# ---------------------------------------------------------------------------
# _build_cancel_run_command
# ---------------------------------------------------------------------------


def test_cancel_run_command_is_bash_list() -> None:
    cmd = _build_cancel_run_command("/tmp/run.pid")
    assert cmd[0] == "bash"
    assert cmd[1] == "-lc"
    assert isinstance(cmd[2], str)


def test_cancel_run_command_reads_pid_file() -> None:
    cmd = _build_cancel_run_command("/tmp/run.pid")
    assert "/tmp/run.pid" in cmd[2]
    assert "cat" in cmd[2]


def test_cancel_run_command_pkills_process_group() -> None:
    cmd = _build_cancel_run_command("/tmp/run.pid")
    assert "pkill" in cmd[2]
    assert "TERM" in cmd[2]


def test_cancel_run_command_kills_wrapper_shell() -> None:
    cmd = _build_cancel_run_command("/tmp/run.pid")
    assert "kill -TERM" in cmd[2]


def test_cancel_run_command_tolerates_missing_pid_file() -> None:
    cmd = _build_cancel_run_command("/tmp/run.pid")
    assert "2>/dev/null" in cmd[2] or "|| true" in cmd[2]
