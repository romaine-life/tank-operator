import asyncio
from collections import defaultdict
from contextlib import asynccontextmanager
from typing import AsyncIterator


class SessionEventBus:
    """Owner-scoped invalidation bus for browser session lists."""

    def __init__(self) -> None:
        self._subscribers: dict[str, set[asyncio.Queue[str]]] = defaultdict(set)

    @asynccontextmanager
    async def subscribe(self, owner: str) -> AsyncIterator[asyncio.Queue[str]]:
        queue: asyncio.Queue[str] = asyncio.Queue(maxsize=1)
        self._subscribers[owner].add(queue)
        try:
            yield queue
        finally:
            subscribers = self._subscribers.get(owner)
            if subscribers is None:
                return
            subscribers.discard(queue)
            if not subscribers:
                self._subscribers.pop(owner, None)

    def publish(self, owner: str) -> None:
        for queue in list(self._subscribers.get(owner, ())):
            if queue.full():
                try:
                    queue.get_nowait()
                except asyncio.QueueEmpty:
                    pass
            queue.put_nowait("session-list-changed")
