#!/usr/bin/env python3
"""Persistent Claude agent runner for tank-operator GUI sessions.

Replaces the per-turn `claude -p` dispatch with a single long-lived SDK
session per pod. The orchestrator submits turns by dropping JSON files into
a watched directory; this script picks them up in order, runs each through
the SDK, and writes stream-json events to per-turn stream files so the
existing browser WebSocket tail infrastructure works unchanged.

ScheduleWakeup is handled natively here: when the agent emits a
ScheduleWakeup tool_use we sleep for delaySeconds in-process then queue the
wakeup prompt as the next turn — no orchestrator polling, no k8s exec.

Usage (launched once per session by claude-agent-launch.sh):
    persistent-agent.py <session_id> <turns_dir> <agent_pid_path>

Turn descriptor format (written by orchestrator to <turns_dir>/<run_id>.json):
    {
        "run_id": "<hex>",
        "prompt": "<user message>",
        "stream_path": "/tmp/tank-run-<run_id>.stream",
        "pid_path":    "/tmp/tank-run-<run_id>.pid"
    }
"""
from __future__ import annotations

import asyncio
import glob
import json
import os
import secrets
import sys
from pathlib import Path

try:
    from claude_code_sdk import (  # type: ignore[import]
        ClaudeCodeOptions,
        query as sdk_query,
    )
    # startup() keeps one claude subprocess alive across turns.
    # Available since claude-code-sdk 0.0.14; fall back to per-turn query()
    # if it isn't exported yet (older image builds).
    try:
        from claude_code_sdk import startup as sdk_startup  # type: ignore[import]
        _HAS_STARTUP = True
    except ImportError:
        _HAS_STARTUP = False
except ImportError:
    sys.stderr.write("claude-code-sdk not installed — add to Dockerfile pip install\n")
    sys.exit(1)

SESSION_ID = sys.argv[1]
TURNS_DIR = sys.argv[2]
AGENT_PID_PATH = sys.argv[3]

_POLL_INTERVAL = 0.1  # seconds between turns-dir polls


async def _poll_next_turn(turns_dir: str) -> dict:
    """Block (async) until a .json turn file appears; return its contents."""
    loop = asyncio.get_running_loop()
    while True:
        # Run glob in executor so we don't block the event loop.
        files = await loop.run_in_executor(
            None, lambda: sorted(glob.glob(f"{turns_dir}/*.json"))
        )
        if files:
            path = files[0]
            try:
                data = json.loads(Path(path).read_text(encoding="utf-8"))
                Path(path).unlink(missing_ok=True)
                return data
            except Exception as exc:
                sys.stderr.write(f"bad turn file {path}: {exc}\n")
                try:
                    Path(path).unlink(missing_ok=True)
                except Exception:
                    pass
        await asyncio.sleep(_POLL_INTERVAL)


def _extract_wakeup(events: list[object]) -> tuple[int | None, str | None]:
    """Scan collected stream events for a ScheduleWakeup tool_use."""
    for event in events:
        if not isinstance(event, dict) or event.get("type") != "assistant":
            continue
        message = event.get("message")
        if not isinstance(message, dict):
            continue
        for block in message.get("content") or []:
            if not isinstance(block, dict) or block.get("type") != "tool_use":
                continue
            if (block.get("name") or "").lower() != "schedulewakeup":
                continue
            inp = block.get("input") or {}
            try:
                delay = max(0, int(inp["delaySeconds"]))
                prompt = str(inp["prompt"])
                return delay, prompt
            except (KeyError, TypeError, ValueError):
                pass
    return None, None


async def _run_turn(
    session,  # warm SDK handle (or None if using per-turn query)
    prompt: str,
    stream_path: str,
    pid_path: str,
    is_first_ever: bool,
) -> tuple[int | None, str | None]:
    """Run one agent turn; return (wakeup_delay_seconds, wakeup_prompt)."""
    # Write a pid file so /run/active endpoint considers this run live.
    # We use our own PID since there's no separate claude subprocess to track.
    Path(pid_path).write_text(str(os.getpid()), encoding="utf-8")

    collected: list[object] = []
    try:
        with open(stream_path, "a", encoding="utf-8") as stream_file:
            if _HAS_STARTUP and session is not None:
                # Persistent subprocess path — single claude process handles
                # all turns; conversation history lives in its memory.
                # TODO: verify exact method name against installed SDK version.
                aiter = session.query(prompt=prompt)
            else:
                # Fallback: per-turn SDK query with --continue for continuity.
                # Equivalent to the old `claude -p --continue` but running
                # inside this persistent Python wrapper so ScheduleWakeup is
                # caught here rather than needing the orchestrator watcher.
                opts = ClaudeCodeOptions(
                    cwd="/workspace",
                    # resume="last" is the SDK equivalent of --continue.
                    # TODO: verify field name; may be `continue_conversation=True`.
                    resume="last" if not is_first_ever else None,
                )
                aiter = sdk_query(prompt=prompt, options=opts)

            async for event in aiter:
                raw = event if isinstance(event, str) else json.dumps(event)
                stream_file.write(raw + "\n")
                stream_file.flush()
                if isinstance(event, dict):
                    collected.append(event)
    finally:
        # Remove pid file so the browser knows the turn finished.
        Path(pid_path).unlink(missing_ok=True)

    return _extract_wakeup(collected)


async def main() -> None:
    Path(TURNS_DIR).mkdir(parents=True, exist_ok=True)
    Path(AGENT_PID_PATH).write_text(str(os.getpid()), encoding="utf-8")

    session = None
    if _HAS_STARTUP:
        opts = ClaudeCodeOptions(cwd="/workspace")
        session = await sdk_startup(options=opts)

    try:
        is_first_ever = True
        # Internal wakeup queue: dicts with same shape as turn descriptor files.
        # Wakeup turns bypass the turns_dir so the orchestrator isn't involved.
        pending_wakeups: list[dict] = []

        while True:
            if pending_wakeups:
                turn = pending_wakeups.pop(0)
            else:
                turn = await _poll_next_turn(TURNS_DIR)

            prompt = turn["prompt"]
            run_id = turn["run_id"]
            stream_path = turn.get("stream_path", f"/tmp/tank-run-{run_id}.stream")
            pid_path = turn.get("pid_path", f"/tmp/tank-run-{run_id}.pid")

            delay, wakeup_prompt = await _run_turn(
                session, prompt, stream_path, pid_path, is_first_ever
            )
            is_first_ever = False

            if delay is not None and wakeup_prompt is not None:
                # Sleep natively — no k8s exec polling, no orchestrator round-trip.
                await asyncio.sleep(delay)
                wakeup_run_id = secrets.token_hex(12)
                pending_wakeups.append({
                    "run_id": wakeup_run_id,
                    "prompt": wakeup_prompt,
                    "stream_path": f"/tmp/tank-run-{wakeup_run_id}.stream",
                    "pid_path": f"/tmp/tank-run-{wakeup_run_id}.pid",
                })
    finally:
        if session is not None:
            try:
                await session.close()
            except Exception:
                pass
        Path(AGENT_PID_PATH).unlink(missing_ok=True)


if __name__ == "__main__":
    asyncio.run(main())
