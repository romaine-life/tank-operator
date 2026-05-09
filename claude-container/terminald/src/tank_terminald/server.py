from __future__ import annotations

import asyncio
import contextlib
import fcntl
import json
import logging
import os
import pty
import select
import signal
import struct
import subprocess
import termios
from collections import deque
from dataclasses import dataclass, field

from aiohttp import WSMsgType, web

LOG = logging.getLogger(__name__)

HOST = os.environ.get("TERMINALD_HOST", "127.0.0.1")
PORT = int(os.environ.get("TERMINALD_PORT", "7680"))
BOOTSTRAP = os.environ.get("TERMINALD_BOOTSTRAP", "/opt/tank/bootstrap.sh")
REPLAY_LINES = int(os.environ.get("TERMINALD_REPLAY_LINES", "1000"))
DEFAULT_COLS = int(os.environ.get("TERMINALD_COLS", "120"))
DEFAULT_ROWS = int(os.environ.get("TERMINALD_ROWS", "40"))


@dataclass
class TerminalHub:
    clients: set[web.WebSocketResponse] = field(default_factory=set)
    replay: deque[bytes] = field(default_factory=lambda: deque(maxlen=REPLAY_LINES))
    _partial_line: bytes = b""
    _master_fd: int | None = None
    _process: subprocess.Popen[bytes] | None = None
    _reader_task: asyncio.Task[None] | None = None
    _cols: int = DEFAULT_COLS
    _rows: int = DEFAULT_ROWS

    async def start(self) -> None:
        if self._process is not None and self._process.poll() is None:
            return
        master_fd, slave_fd = pty.openpty()
        self._set_winsize(master_fd, self._cols, self._rows)
        env = os.environ.copy()
        env.setdefault("TERM", "tmux-256color")
        self._process = subprocess.Popen(
            ["bash", BOOTSTRAP],
            stdin=slave_fd,
            stdout=slave_fd,
            stderr=slave_fd,
            cwd="/workspace",
            env=env,
            close_fds=True,
            preexec_fn=os.setsid,
        )
        os.close(slave_fd)
        os.set_blocking(master_fd, False)
        self._master_fd = master_fd
        self._reader_task = asyncio.create_task(self._read_pty())
        LOG.info("terminal process started pid=%s", self._process.pid)

    async def stop(self) -> None:
        task = self._reader_task
        self._reader_task = None
        if task is not None:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        if self._master_fd is not None:
            with contextlib.suppress(OSError):
                os.close(self._master_fd)
            self._master_fd = None
        proc = self._process
        self._process = None
        if proc is not None and proc.poll() is None:
            with contextlib.suppress(OSError):
                os.killpg(proc.pid, signal.SIGTERM)

    async def attach(self, ws: web.WebSocketResponse) -> None:
        await self.start()
        self.clients.add(ws)
        replay = b"\n".join(self.replay)
        if self._partial_line:
            replay = replay + (b"\n" if replay else b"") + self._partial_line
        if replay:
            await ws.send_bytes(replay)
        LOG.info("terminal client attached count=%s", len(self.clients))

    def detach(self, ws: web.WebSocketResponse) -> None:
        self.clients.discard(ws)
        LOG.info("terminal client detached count=%s", len(self.clients))

    async def write(self, data: bytes) -> None:
        if self._master_fd is None:
            return
        await asyncio.to_thread(self._write_all, data)

    def resize(self, cols: int, rows: int) -> None:
        self._cols = max(1, int(cols))
        self._rows = max(1, int(rows))
        if self._master_fd is not None:
            self._set_winsize(self._master_fd, self._cols, self._rows)

    def status(self) -> dict[str, object]:
        proc = self._process
        return {
            "clients": len(self.clients),
            "pid": proc.pid if proc is not None else None,
            "returncode": proc.poll() if proc is not None else None,
            "cols": self._cols,
            "rows": self._rows,
            "replay_lines": len(self.replay),
        }

    async def _read_pty(self) -> None:
        assert self._master_fd is not None
        loop = asyncio.get_running_loop()
        while True:
            try:
                data = await loop.run_in_executor(None, self._read_once)
            except OSError:
                LOG.info("terminal pty closed")
                break
            if not data:
                await asyncio.sleep(0.01)
                continue
            self._record_replay(data)
            await self._broadcast(data)

    def _read_once(self) -> bytes:
        assert self._master_fd is not None
        try:
            return os.read(self._master_fd, 65536)
        except BlockingIOError:
            return b""

    def _write_all(self, data: bytes) -> None:
        assert self._master_fd is not None
        view = memoryview(data)
        while view:
            try:
                written = os.write(self._master_fd, view)
                if written == 0:
                    select.select([], [self._master_fd], [])
                    continue
                view = view[written:]
            except BlockingIOError:
                select.select([], [self._master_fd], [])
                continue

    async def _broadcast(self, data: bytes) -> None:
        stale: list[web.WebSocketResponse] = []
        for ws in list(self.clients):
            try:
                await ws.send_bytes(data)
            except Exception:
                stale.append(ws)
        for ws in stale:
            self.detach(ws)

    def _record_replay(self, data: bytes) -> None:
        parts = data.split(b"\n")
        if len(parts) == 1:
            self._partial_line += data
            self._partial_line = self._partial_line[-8192:]
            return
        self.replay.append(self._partial_line + parts[0])
        for part in parts[1:-1]:
            self.replay.append(part)
        self._partial_line = parts[-1][-8192:]

    @staticmethod
    def _set_winsize(fd: int, cols: int, rows: int) -> None:
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


async def handle_ws(request: web.Request) -> web.StreamResponse:
    hub: TerminalHub = request.app["hub"]
    ws = web.WebSocketResponse(heartbeat=30)
    await ws.prepare(request)
    await hub.attach(ws)
    try:
        async for msg in ws:
            if msg.type == WSMsgType.TEXT:
                text = msg.data
                if text.startswith("{"):
                    try:
                        ctrl = json.loads(text)
                    except ValueError:
                        ctrl = None
                    if isinstance(ctrl, dict):
                        if "resize" in ctrl:
                            cols, rows = ctrl["resize"]
                            hub.resize(int(cols), int(rows))
                            continue
                        if "ping" in ctrl:
                            continue
                await hub.write(text.encode())
            elif msg.type == WSMsgType.BINARY:
                await hub.write(msg.data)
    finally:
        hub.detach(ws)
    return ws


async def handle_health(request: web.Request) -> web.Response:
    return web.json_response({"ok": True})


async def handle_status(request: web.Request) -> web.Response:
    hub: TerminalHub = request.app["hub"]
    return web.json_response(hub.status())


async def create_app() -> web.Application:
    app = web.Application()
    app["hub"] = TerminalHub()
    app.router.add_get("/session", handle_ws)
    app.router.add_get("/healthz", handle_health)
    app.router.add_get("/debug/status", handle_status)
    return app


def main() -> None:
    logging.basicConfig(level=os.environ.get("TERMINALD_LOG_LEVEL", "INFO"))
    web.run_app(create_app(), host=HOST, port=PORT)


if __name__ == "__main__":
    main()
