"""Terminal bridge helpers.

One-shot file/capture helpers still use Kubernetes pods/exec. The interactive
browser terminal does not: it proxies to the pod-local terminald WebSocket
through kube-rbac-proxy, avoiding a long-lived apiserver exec stream.

Frontend protocol (kept deliberately small):
- Text frames are stdin (utf-8 keystrokes from xterm.js).
- A text frame parsing as JSON `{"resize": [cols, rows]}` is a terminal
  resize control message instead of stdin.
- Server emits raw terminal bytes from the pod.
"""

from __future__ import annotations

import asyncio
import json
import logging
import shlex

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
SERVICE_ACCOUNT_TOKEN = "/var/run/secrets/kubernetes.io/serviceaccount/token"

STDIN_CHANNEL = 0
STDOUT_CHANNEL = 1
STDERR_CHANNEL = 2
ERROR_CHANNEL = 3
RESIZE_CHANNEL = 4


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


async def exec_stream_to_websocket(
    browser: WebSocket,
    namespace: str,
    pod_name: str,
    command: list[str],
    stdin: bytes,
) -> None:
    """Run a one-shot command and forward stdout/stderr chunks to the browser.

    Browser frames are JSON objects:
    - {"stream":"stdout"|"stderr","data":"..."} for command output
    - {"status":"done"} when Kubernetes reports a successful exit
    - {"status":"error","detail":"..."} for non-zero exits or stream failures
    """
    ws_client = WsApiClient()
    core = client.CoreV1Api(api_client=ws_client)
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
        error_status: dict[str, str] | None = None
        async with cm as k8s_ws:
            for offset in range(0, len(stdin), 64 * 1024):
                await k8s_ws.send_bytes(
                    bytes([STDIN_CHANNEL]) + stdin[offset : offset + 64 * 1024]
                )
            async for wsmsg in k8s_ws:
                if wsmsg.type == aiohttp.WSMsgType.BINARY:
                    if not wsmsg.data:
                        continue
                    channel = wsmsg.data[0]
                    payload = wsmsg.data[1:]
                    if channel == STDOUT_CHANNEL:
                        await browser.send_json(
                            {
                                "stream": "stdout",
                                "data": payload.decode(errors="replace"),
                            }
                        )
                    elif channel == STDERR_CHANNEL:
                        await browser.send_json(
                            {
                                "stream": "stderr",
                                "data": payload.decode(errors="replace"),
                            }
                        )
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
    except Exception as e:
        await browser.send_json({"status": "error", "detail": str(e)})
        return
    finally:
        await ws_client.close()

    if error_status is not None and error_status.get("status") != "Success":
        await browser.send_json({"status": "error", "detail": str(error_status)})
        return
    await browser.send_json({"status": "done"})


async def bridge(browser: WebSocket, pod_ip: str, terminal_port: int) -> None:
    token = _service_account_token()
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    url = f"ws://{pod_ip}:{terminal_port}/session"
    async with aiohttp.ClientSession() as session:
        async with session.ws_connect(
            url, headers=headers, heartbeat=30
        ) as terminal_ws:
            await _pump(browser, terminal_ws)


def _service_account_token() -> str | None:
    try:
        with open(SERVICE_ACCOUNT_TOKEN, encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        # Local dev without in-cluster auth; kube-rbac-proxy will reject if
        # the target actually requires Kubernetes auth.
        return None


async def _pump(
    browser: WebSocket, terminal_ws: aiohttp.ClientWebSocketResponse
) -> None:
    async def send_terminal(payload: bytes | str) -> None:
        if isinstance(payload, str):
            await terminal_ws.send_str(payload)
        else:
            await terminal_ws.send_bytes(payload)

    async def browser_to_pod() -> None:
        try:
            while True:
                msg = await browser.receive()
                msg_type = msg.get("type")
                if msg_type == "websocket.disconnect":
                    return
                if msg_type != "websocket.receive":
                    continue

                text = msg.get("text")
                if text is not None:
                    # Control frames look like JSON; everything else is raw
                    # stdin from xterm.js. Recognized: {"resize":[c,r]} for
                    # PTY size changes, {"ping":...} as a no-op heartbeat the
                    # browser sends every ~30s so Envoy's idle stream timeout
                    # (default 5min) doesn't cut a quiet WS — which would also
                    # let the orchestrator's idle reaper delete the pod.
                    if text and text[0] == "{":
                        try:
                            ctrl = json.loads(text)
                        except ValueError:
                            ctrl = None
                        if isinstance(ctrl, dict):
                            if "resize" in ctrl:
                                cols, rows = ctrl["resize"]
                                await send_terminal(
                                    json.dumps({"resize": [int(cols), int(rows)]})
                                )
                                continue
                            if "ping" in ctrl:
                                await send_terminal(text)
                                continue
                    await send_terminal(text)
                else:
                    data = msg.get("bytes")
                    if data:
                        await send_terminal(data)
        except WebSocketDisconnect:
            return
        except Exception:
            log.exception("browser → terminal loop crashed")

    async def terminal_to_browser() -> None:
        try:
            async for wsmsg in terminal_ws:
                if wsmsg.type == aiohttp.WSMsgType.BINARY:
                    await browser.send_bytes(wsmsg.data)
                elif wsmsg.type == aiohttp.WSMsgType.TEXT:
                    await browser.send_text(wsmsg.data)
                elif wsmsg.type in (
                    aiohttp.WSMsgType.CLOSE,
                    aiohttp.WSMsgType.CLOSED,
                    aiohttp.WSMsgType.ERROR,
                ):
                    return
        except Exception:
            log.exception("terminal → browser loop crashed")

    tasks = {
        asyncio.create_task(browser_to_pod()),
        asyncio.create_task(terminal_to_browser()),
    }
    done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
    for task in pending:
        task.cancel()
    await asyncio.gather(*pending, return_exceptions=True)
    for task in done:
        task.result()
