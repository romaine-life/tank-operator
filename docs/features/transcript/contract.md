# Transcript Contract

This contract applies to the GUI chat transcript ledger: the first prompt,
startup status, provider output, tool events, and live delivery into the
visible conversation. Viewport anchoring, deep links, historical pagination,
read position, and live-tail navigation are covered by
[Transcript Navigation](../transcript-navigation/contract.md).

## Product Model

The transcript is a durable conversation ledger with a live tail. It should
feel like a mature messaging surface: refresh may recover from a broken browser
state, but refresh must not be required for normal progress.

The browser DOM is not the source of truth. Provider streams are adapter input,
not the rendered protocol. The frontend renders the Tank conversation protocol
from durable events.

## Sources Of Truth

- `session_events` owns transcript entries and ordering.
- `order_key` owns transcript order and cursor movement.
- `session.status` events own startup notices shown inside the transcript.
- Provider SDK events are inputs that must be converted to Tank events before
  the UI depends on them.
- SSE is a live follower of durable events, not the transcript store.

## Migration Rules

- Browser-local startup drafts are not transcript rows.
- Client-only "loading" or "continuing" transcript placeholders are prohibited
  when a durable event exists or can be written.
- A first prompt typed on the splash screen must be written durably before
  startup status events.
- Old provider-specific transcript render paths must be deleted when replaced
  by Tank protocol rendering.
- Refresh-only recovery must not be accepted as proof that live transcript
  delivery works.

## Live Behavior

- A new transcript begins with the user's first durable message when the user
  provided one.
- Startup notices appear as durable `session.status` transcript entries after
  the first user message.
- An already-open transcript client must receive and render post-cursor durable
  events without reload.
- The live stream must keep draining persisted events until caught up whenever
  it wakes, reconnects, heartbeats, or resumes from visibility changes.
- A reconnect from an unknown cursor must trigger explicit resync instead of
  silently skipping a gap.
- Ready/load transitions must not reset, reorder, or replace the transcript.

## Failure And Recovery

- Browser reload replays from durable history and may repair local display
  state, but reload is not part of the happy path.
- Browser disconnect resumes from a durable cursor.
- Orchestrator rollout and runner-process restart must not lose already
  persisted transcript events.
- Session-pod death is the lifecycle boundary. Tank does not promise to
  resurrect the `emptyDir` workspace or continue a dead pod's conversation.

## Observability

- There must be a way to compare an open client's last applied cursor with the
  durable tail for the session.
- Stream open, reconnect, resync, heartbeat, emitted-event, and error counters
  must distinguish auth failure from live cursor lag.
- User-trust bugs should be diagnosable by querying `session_events` first,
  then live stream telemetry.
- A durable terminal event that exists but is not visible in an open transcript
  must leave enough telemetry to localize the miss.

## Acceptance Checks

- Creating a session from a splash prompt writes the user message before
  startup status.
- `session.status` loading and ready entries render from durable events.
- With an SSE stream already open at cursor X, persisting later transcript
  events causes the browser to render them without refresh.
- Reconnect from a valid cursor replays missed events exactly once.
- Reconnect from an unknown cursor triggers explicit resync.
- A browser or integration check proves that load/ready does not reset the
  transcript.
