"""Kubernetes exec helpers for session pods."""

from __future__ import annotations

import asyncio
import json
import logging
import shlex
from collections.abc import Awaitable, Callable

import aiohttp
from fastapi import WebSocket, WebSocketDisconnect
from kubernetes_asyncio import client
from kubernetes_asyncio.stream import WsApiClient

log = logging.getLogger(__name__)

# Bootstrap is mounted from the session ConfigMap at /opt/tank/bootstrap.sh.
# Inlining it here is tempting but the kube-apiserver URL-encodes every byte of
# the exec command into ?command=... and rejects oversized request lines with
# HTTP 400; the script grew past that limit and broke reconnects.
# Do not use a login shell here: Alpine's /etc/profile resets PATH to the
# distro default and masks tool paths exported by the image, including Go.
EXEC_COMMAND = ["bash", "/opt/tank/bootstrap.sh"]

# Session pods have two containers (mcp-auth-proxy sidecar + claude). The
# apiserver requires container= when more than one is present, otherwise
# pods/exec returns 400 "a container name must be specified".
SESSION_CONTAINER = "claude"
STDIN_CHANNEL = 0
STDOUT_CHANNEL = 1
STDERR_CHANNEL = 2
ERROR_CHANNEL = 3
RESIZE_CHANNEL = 4
DETACHED_LAUNCH_ATTEMPTS = 3
DETACHED_LAUNCH_RETRY_DELAYS = (0.5, 1.5)


async def exec_capture(namespace: str, pod_name: str, command: list[str]) -> bytes:
    """Run a one-shot command in `pod_name` and return its stdout as bytes.

    Used for short, read-only operations (e.g. `cat /some/file`) where the
    caller needs the bytes back as a single buffer. For interactive long-
    lived streams (TTY shells), use `bridge` instead.

    Raises RuntimeError if the K8s exec error channel reports a non-Success
    status (typical when the command exits non-zero, e.g. cat on a missing
    file). stderr is logged at WARNING but not surfaced to the caller —
    callers that care should check command output instead.
    """
    ws_client = WsApiClient()
    core = client.CoreV1Api(api_client=ws_client)
    try:
        cm = await core.connect_get_namespaced_pod_exec(
            name=pod_name,
            namespace=namespace,
            container=SESSION_CONTAINER,
            command=command,
            stdin=False,
            stdout=True,
            stderr=True,
            tty=False,
            _preload_content=False,
        )
        stdout_chunks: list[bytes] = []
        stderr_chunks: list[bytes] = []
        error_status: dict[str, str] | None = None
        async with cm as k8s_ws:
            async for wsmsg in k8s_ws:
                if wsmsg.type == aiohttp.WSMsgType.BINARY:
                    if not wsmsg.data:
                        continue
                    channel = wsmsg.data[0]
                    payload = wsmsg.data[1:]
                    if channel == STDOUT_CHANNEL:
                        stdout_chunks.append(payload)
                    elif channel == STDERR_CHANNEL:
                        stderr_chunks.append(payload)
                    elif channel == ERROR_CHANNEL:
                        # K8s sends a v1.Status JSON here at end-of-stream;
                        # {"status":"Success"} on exit-0, otherwise a
                        # Failure with details (including non-zero exit
                        # code in `details.causes[].message`).
                        try:
                            error_status = json.loads(payload)
                        except ValueError:
                            error_status = {
                                "status": "Failure",
                                "message": payload.decode(errors="replace"),
                            }
                elif wsmsg.type in (
                    aiohttp.WSMsgType.CLOSE,
                    aiohttp.WSMsgType.CLOSED,
                    aiohttp.WSMsgType.ERROR,
                ):
                    break
    finally:
        await ws_client.close()

    if stderr_chunks:
        log.warning(
            "exec %s stderr: %s",
            command,
            b"".join(stderr_chunks).decode(errors="replace")[:500],
        )
    if error_status is not None and error_status.get("status") != "Success":
        raise RuntimeError(f"exec {command} failed: {error_status}")
    return b"".join(stdout_chunks)


async def exec_write_file(
    namespace: str, pod_name: str, path: str, data: bytes
) -> None:
    """Write `data` to `path` inside the session container.

    The command reads an exact byte count, so we do not need a channel-specific
    stdin close signal from the Kubernetes exec protocol.
    """
    ws_client = WsApiClient()
    core = client.CoreV1Api(api_client=ws_client)
    quoted_path = shlex.quote(path)
    quoted_dir = shlex.quote(path.rsplit("/", 1)[0] or ".")
    command = [
        "bash",
        "-lc",
        f"set -euo pipefail; mkdir -p {quoted_dir}; umask 077; head -c {len(data)} > {quoted_path}",
    ]
    try:
        cm = await core.connect_get_namespaced_pod_exec(
            name=pod_name,
            namespace=namespace,
            container=SESSION_CONTAINER,
            command=command,
            stdin=True,
            stdout=True,
            stderr=True,
            tty=False,
            _preload_content=False,
        )
        stdout_chunks: list[bytes] = []
        stderr_chunks: list[bytes] = []
        error_status: dict[str, str] | None = None
        async with cm as k8s_ws:
            for offset in range(0, len(data), 64 * 1024):
                await k8s_ws.send_bytes(
                    bytes([STDIN_CHANNEL]) + data[offset : offset + 64 * 1024]
                )
            async for wsmsg in k8s_ws:
                if wsmsg.type == aiohttp.WSMsgType.BINARY:
                    if not wsmsg.data:
                        continue
                    channel = wsmsg.data[0]
                    payload = wsmsg.data[1:]
                    if channel == STDOUT_CHANNEL:
                        stdout_chunks.append(payload)
                    elif channel == STDERR_CHANNEL:
                        stderr_chunks.append(payload)
                    elif channel == ERROR_CHANNEL:
                        try:
                            error_status = json.loads(payload)
                        except ValueError:
                            error_status = {
                                "status": "Failure",
                                "message": payload.decode(errors="replace"),
                            }
                elif wsmsg.type in (
                    aiohttp.WSMsgType.CLOSE,
                    aiohttp.WSMsgType.CLOSED,
                    aiohttp.WSMsgType.ERROR,
                ):
                    break
    finally:
        await ws_client.close()

    if stderr_chunks:
        log.warning(
            "exec write %s stderr: %s",
            path,
            b"".join(stderr_chunks).decode(errors="replace")[:500],
        )
    if error_status is not None and error_status.get("status") != "Success":
        raise RuntimeError(f"exec write {path} failed: {error_status}")


async def exec_launch_detached(
    namespace: str,
    pod_name: str,
    command: str,
    log_path: str,
    *,
    capture: Callable[[str, str, list[str]], Awaitable[bytes]] | None = None,
) -> None:
    """Launch a shell command on the pod and detach immediately.

    The exec connection lasts only as long as it takes the launcher shell
    to fork+disown. The launched process keeps running after we return,
    with stdout/stderr redirected to `log_path` for later inspection.

    Used by the headless run endpoints — the calling agent doesn't want
    to block on the receiving agent's run completion.
    """
    launcher = (
        f"set -uo pipefail; "
        f"nohup bash -c {shlex.quote(command)} "
        f"> {shlex.quote(log_path)} 2>&1 < /dev/null & "
        f"disown $! 2>/dev/null || true; "
        f"echo launched"
    )
    launch_command = ["bash", "-lc", launcher]
    capture = capture or exec_capture
    last_exc: Exception | None = None
    for attempt in range(DETACHED_LAUNCH_ATTEMPTS):
        try:
            await capture(namespace, pod_name, launch_command)
            return
        except RuntimeError:
            raise
        except Exception as exc:
            last_exc = exc
            if attempt >= DETACHED_LAUNCH_ATTEMPTS - 1:
                break
            delay = DETACHED_LAUNCH_RETRY_DELAYS[
                min(attempt, len(DETACHED_LAUNCH_RETRY_DELAYS) - 1)
            ]
            log.warning(
                "detached launch transport failure for %s/%s, retrying in %.1fs: %s",
                namespace,
                pod_name,
                delay,
                exc,
            )
            await asyncio.sleep(delay)

    assert last_exc is not None
    raise RuntimeError(f"detached launch failed: {last_exc}") from last_exc


async def exec_stream_to_websocket(
    browser: WebSocket,
    namespace: str,
    pod_name: str,
    command: list[str],
    stdin: bytes,
    cancel_command: list[str] | None = None,
) -> None:
    """Run a one-shot command and forward stdout/stderr chunks to the browser.

    Browser frames are JSON objects:
    - {"stream":"stdout"|"stderr","data":"..."} for command output
    - {"status":"done"} when Kubernetes reports a successful exit
    - {"status":"error","detail":"..."} for non-zero exits or stream failures

    While the command runs the browser may send {"stdin":"..."} frames to
    inject additional bytes into the pod's stdin channel — used for interactive
    prompts such as AskUserQuestion tool calls in headless Claude runs.
    """
    ws_client = WsApiClient()
    core = client.CoreV1Api(api_client=ws_client)
    error_status: dict[str, str] | None = None

    async def close_browser() -> None:
        try:
            await browser.close(code=1000)
        except Exception:
            pass

    try:
        cm = await core.connect_get_namespaced_pod_exec(
            name=pod_name,
            namespace=namespace,
            container=SESSION_CONTAINER,
            command=command,
            stdin=True,
            stdout=True,
            stderr=True,
            tty=False,
            _preload_content=False,
        )
        async with cm as k8s_ws:
            for offset in range(0, len(stdin), 64 * 1024):
                await k8s_ws.send_bytes(
                    bytes([STDIN_CHANNEL]) + stdin[offset : offset + 64 * 1024]
                )

            browser_disconnected = False
