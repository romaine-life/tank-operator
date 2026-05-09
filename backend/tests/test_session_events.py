import asyncio

from tank_operator.session_events import SessionEventBus


def test_session_event_bus_notifies_only_matching_owner() -> None:
    async def run() -> None:
        bus = SessionEventBus()
        async with (
            bus.subscribe("operator@example.test") as owner_queue,
            bus.subscribe("other@example.test") as other_queue,
        ):
            bus.publish("operator@example.test")

            assert await asyncio.wait_for(owner_queue.get(), timeout=0.1)
            assert other_queue.empty()

    asyncio.run(run())


def test_session_event_bus_coalesces_pending_invalidations() -> None:
    async def run() -> None:
        bus = SessionEventBus()
        async with bus.subscribe("operator@example.test") as queue:
            bus.publish("operator@example.test")
            bus.publish("operator@example.test")

            assert await asyncio.wait_for(queue.get(), timeout=0.1)
            assert queue.empty()

    asyncio.run(run())
