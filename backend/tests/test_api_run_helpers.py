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

import os
import re

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
    _skill_trigger,
    _validate_skill_name,
    _validate_run_id,
)


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
