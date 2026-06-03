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

Compacted agent activity is a server-owned projection of the same durable
transcript ledger, not a second ledger. The Turn activity row is an activity/log
surface; the main transcript is the settled conversation surface. The main
transcript is promotion-only: provider activity, reasoning, tool output, and
provisional assistant prose must not default into it. Historical timeline reads
return Turn activity as primary collapsed rows, with child entries loaded on
expansion. The UI may duplicate assistant prose across those projections only
when a successful terminal event explicitly marks that prose as the final
answer; it must not visibly move a rendered row from one surface to the other.

## Sources Of Truth

- `session_events` owns transcript entries and ordering.
- `order_key` owns transcript order and cursor movement.
- `session.status` events own startup notices shown inside the transcript.
- The Tank conversation protocol owns the projection rules for Turn activity
  versus settled transcript messages.
- A `turn_activity` shell carries the durable `turnNumber` stamped from
  `session_turns` during materialization. It is a read-only projection of the
  number, not a second source of truth; the number's owner is the
  [Transcript Navigation](../transcript-navigation/contract.md) contract.
- `turn.completed.payload.final_answer.timeline_ids` is the only durable fact
  that promotes assistant prose from activity/log material into a settled
  main-transcript assistant response.
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
- Compactable activity must not be rendered first as a settled transcript row
  and later relocated into Turn activity.
- Previous-conversation loads must consume the server transcript projection;
  raw timeline events are not the authority for historical Turn activity
  grouping.

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
- Active-turn assistant prose is provisional until a successful terminal event
  carries an explicit durable final-answer marker. The server projection uses
  `turn.completed.payload.final_answer.timeline_ids` as the only final-answer
  source; it must not infer finality from a trailing assistant message/run.
- Failed, interrupted, and otherwise non-successful turns do not have a final
  assistant answer. Their non-user activity stays in Turn activity, with terminal
  context surfaced by the Turn activity disclosure row and the terminal meta
  line, not by expanding child provider rows into the main transcript.
- A server-projected active `turn_activity` shell owns the visible running
  placeholder for that turn. The browser must not hide the `...` row while
  waiting for a separately-delivered activity summary to set the same active
  turn id.
- The running placeholder's active state comes from that shell, but its chat
  placement is resolved from durable `order_key`, not from a structural
  "latest row carrying this turnId" rule. The placeholder sorts at the turn's
  live-tail order key — the furthest order key the turn has reached across both
  the shell's compacted activity (`endOrderKey`) and any turn-tagged row that
  stays in the main transcript. Two cases this must satisfy together:
  companion rows anchored to a later order key, such as answered
  AskUserQuestion handoffs, must not be overtaken by the placeholder; and
  untagged durable rows that precede the turn's activity, such as the
  `Session is loading.` / `Session is ready.` `session.status` notices on a new
  session's first turn, must stay above the placeholder. A turnId-structural
  placement rule strands the placeholder above those untagged notices because
  they carry no `turnId`.
- Mid-turn token usage updates are durable turn activity, not a live-only
  buffered status line. The projected usage row keeps the transcript position
  of the first `turn.usage` event for that turn while its payload and the
  activity shell's live-tail cursor advance with later usage updates.
- Context-window occupancy is read from per-message usage snapshots
  (`usage_observation.usage_source = "claude.message"` for Claude;
  `thread.tokenUsage.updated` for Codex), never from a cumulative turn
  terminal. The two provider shapes treat cached input oppositely: when cached
  tokens are additive to `input_tokens` (Claude: `cache_read_input_tokens` +
  `cache_creation_input_tokens`) occupancy is their sum; when the cached count
  is a subset of `input_tokens` (Codex/OpenAI) occupancy is the uncached delta
  or in-window prompt count. Reading a Claude blob with the subset rule yields
  only the uncached `input_tokens` sliver — the regression this guards
  against. The cumulative terminal usage (`claude.result`) drives cost, not
  the gauge; terminal annotation must not overwrite the dedicated usage row's
  snapshot with it.
- The composer context indicator is a `used/window` fraction. The window
  (denominator) is the provider-observed context window persisted on the
  session row as `runtime_context_window_tokens`, reported by the runners
  through `PUT /api/internal/sessions/{id}/runtime-config` (Codex app-server
  token usage; the Claude Agent SDK per-turn `modelUsage.contextWindow`). There is no
  frontend model-window table and no percent ring: the denominator is never
  a frontend-assumed default keyed off a model id. Before the provider has
  reported a window — pre-session previews on the splash composer, or any
  session whose row still carries `runtime_context_window_tokens = 0` — the
  indicator shows a placeholder (the bare used count, no fraction), never a
  guessed window. The row value is durable and first-observed-wins: the first
  positive window the runner reports is persisted and not overwritten by later
  reports, so the fraction is stable across reloads and matches a fresh tab.
- Already-open Turn activity details are a cached view of the server projection,
  not a second browser-owned ledger. A live `transcript_rows` batch for a turn
  whose details are already loaded must invalidate that cache and re-read
  `/turns/{id}/activity`; the browser must not synthesize child activity rows
  from the live shell.
- Turn activity may show a log copy of assistant prose, including prose that
  later becomes the final answer, but that copy is not a second settled
  transcript message.
- Copy links, unread counts, latest-message state, and fork-from-message actions
  must target the settled transcript projection, not duplicate activity-log
  copies.

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
- Provider context-window reports on the runtime-config PUT must be counted by
  bounded labels so "the composer never got a window" is diagnosable without
  reading runner logs. `tank_session_context_window_report_total{provider,
  source,result}` records one outcome per call around `SetRuntimeContextWindow`
  (`ok` / `not_found` / `update_failed`, and `ignored` when the call carried no
  positive window); `source` is bounded to the known observation tags plus an
  `other` bucket.
- A live Turn activity detail refresh that cannot re-read the server projection
  must be counted by bounded client telemetry and leave a visible, retryable
  state in the Turns detail instead of silently leaving stale activity on
  screen.
- A server-projected active Turn activity shell that does not produce a visible
  `...` row must emit bounded client telemetry so the missing placeholder can be
  diagnosed without guessing from screenshots. The telemetry must count active
  shells from transcript entries, not only from rendered rows, because the shell
  may be projection-only.
- A report that a message bounced between Turn activity and the main transcript
  should be diagnosable from the durable event order plus the frontend
  projection chosen before first paint.

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
- An active turn that emits assistant prose and then later emits more work does
  not show that prose as a settled main-transcript row before moving it into
  Turn activity.
- With a Turns detail already loaded and an SSE stream already open, a later
  durable activity row for the same turn causes the browser to re-read
  `/turns/{id}/activity` without refresh; repeated refresh failures surface a
  retryable Turns detail error and `tank_session_event_client_events_total`
  labels for the failure.
- A completed turn may show the final assistant prose in the main transcript
  while also retaining a log copy in Turn activity, without counting it as two
  transcript messages.
- Failed or interrupted turns keep their non-user rows in Turn activity and show
  only the user message plus terminal context in the main transcript unless a
  later successful `turn.completed` with explicit final-answer ids wins the race.
- A session whose row carries no provider-observed window
  (`runtime_context_window_tokens = 0`) shows the composer placeholder, not a
  fraction against a guessed default; once the runner reports a positive window
  the indicator renders `used/window` and stays stable (first-observed-wins)
  across reload. `scripts/check-context-window-table-migration.mjs` proves no
  `CONTEXT_WINDOW_BY_MODEL` / `getContextWindow` model table remains under
  `frontend/src` and that the composer reads `runtime_context_window_tokens`.
