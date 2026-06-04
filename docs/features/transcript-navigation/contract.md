# Transcript Navigation Contract

This contract applies to where the user lands in a transcript, how transcript
position is preserved, how historical pages load, how copied message links
resolve, and how live-tail behavior interacts with a reader who is not at the
tail.

## Product Model

Transcript navigation is orientation over a durable conversation ledger. It is
not message ownership; the [Transcript](../transcript/contract.md) contract
owns which messages exist and how they are delivered. This feature owns whether
the user remains oriented while those messages render.

Normal session navigation lands at the live tail. Historical position is
explicit user intent only: copied message links, manual back-pagination, and
deliberate history navigation. New live messages should be noticeable without
yanking the viewport away from a user reading history.

## Sources Of Truth

- `session_events.order_key` owns durable live-stream position.
- `session_transcript_rows.row_cursor` owns `/timeline` historical transcript
  position.
- `session_turns.turn_number` owns the durable, per-session, submission-ordered
  turn number that the public route `/sessions/{id}/turns/{n}` resolves into.
  `turn_id` (`turn_<nonce>`) remains the provider-neutral protocol identity that
  events, timelines, idempotency, and the activity/interrupt/answer APIs
  key on; the number is a stable public handle over it, not a replacement, and
  the turn_id-keyed APIs are the identity layer the public route resolves into,
  not a retained old route. A number is assigned exactly once, server-side, by
  the `tank_session_events_allocate_turn_number` trigger on the turn's first
  durable `session_events` insert, and is resolved server-side via
  `GET /api/sessions/{id}/turns/{n}`. The browser never maps a number to a
  turn_id from render state; the durable `session_turns` row, not the loaded
  transcript window, is the resolver.
- `session_transcript_row_backfills` owns whether a session's historical
  `session_events` ledger has been projected into transcript rows for the
  current projection version. Status rows alone do not satisfy backfill; stale
  sessions are materialized on demand for the requested session before
  `/timeline` or transcript SSE read from `session_transcript_rows`.
- Server timeline pages own bounded windows of top-level transcript rows. Raw
  events inside a collapsed Turn activity row are loaded only through the
  explicit Turn activity endpoint.
- Turn activity itself paginates. A turn's expansion body is split into pages
  sealed at `turnPageEventLimit` events and at semantic AskUserQuestion
  boundaries; each `turn.awaiting_input` event starts one `question_set` page
  per question while preserving one durable answer set. The preceding activity
  page gets a compact AskUserQuestion invocation marker derived from the same
  durable event, even when that means a marker-only first page. The Turn
  activity endpoint (`server_turn_activity_v2`) returns the page directory
  (`page`, `page_count`, `pages[]`) and accepts `?page=N`. A `needs_input` turn
  defaults to the first unanswered `question_set` page; all other turns default
  to the latest page. The page boundary is a durable `order_key`-range concept,
  so a selected page is stable across reload and deep links. The shell's
  active/terminal status is never a function of which page rendered — it is
  folded from the complete turn so a finished long turn can never render as
  perpetually active.
- Copied message links may name rendered timeline IDs, but the server must
  translate them to durable cursors.
- `sessions.visible` owns sidebar/list membership only. Soft-deleting a session
  tombstones it from navigation, but it does not revoke owner/admin access to
  copied transcript links or `/timeline` history while the durable row and
  transcript ledger remain in Postgres.
- Durable read state owns unread/new indicators when the indicator affects
  session or transcript state.
- Browser scroll offsets are layout state only; they are not transcript
  position source of truth.

## Migration Rules

- Do not persist or restore browser-local scroll position as normal session
  navigation state.
- Do not make the browser DOM the resolver for deep links or copied message
  anchors.
- Do not keep hidden compatibility paths that infer historical position from
  old local storage keys, previous session selection, or transient viewport
  offsets.
- Do not put non-transcript UI such as "continuing previous conversation" or
  "beginning of conversation" into the scroll flow unless it is part of the
  durable transcript model.
- Delete tests that pin old scroll-restoration behavior when the intended
  behavior is live-tail navigation or durable cursor anchoring.

## Live Behavior

- Opening a session normally lands at the live tail.
- Opening a copied message link lands on a bounded page around that durable
  message cursor.
- Manual back-pagination prepends older messages while preserving the user's
  visual anchor.
- A manual back-pagination action should either add visible projected rows,
  reach the durable oldest edge, or emit telemetry that zero new visible rows
  were returned.
- Manual forward-pagination appends newer historical messages without jumping
  the anchor unexpectedly.
- New live messages append at the tail without moving the viewport when the
  user is reading history.
- Returning to the live tail is an explicit state transition.
- Load, ready, reconnect, and resync must not reset the viewport unless the
  user has explicitly returned to live tail or the current cursor is invalid.
- The Turns view exposes a dedicated **Page dropdown** beside the turn selector.
  It is present whenever a turn is selected; a single-page turn renders it
  **disabled** ("Page 1 of 1") rather than omitting it, and a turn that crosses
  the per-turn event seal enables it as a page picker (Page 1..N). The control is
  never hidden — the pagination affordance stays visible even when there is
  nothing to navigate to. (The Turns view reads the page-defaulted `/activity`
  endpoint, so without this control a long turn would show only its last page
  there with no way back.)

## Failure And Recovery

- Browser reload reconstructs the intended navigation state from durable
  cursor inputs, not from DOM position.
- Valid durable cursors resume to an equivalent bounded transcript window.
- Unknown or expired cursors trigger explicit resync or a clear fallback to the
  live tail; they must not silently show a misleading historical position.
- Reconnect and visibility changes continue from the current navigation mode:
  live-tail mode follows the tail, while historical mode preserves the anchor
  and surfaces new activity separately.
- If a target message is absent from the durable transcript projection or is
  outside the durability boundary, the UI should show a clear
  unavailable-target state. Sidebar deletion by itself is not such a boundary.

## Observability

- There must be a way to distinguish live-tail mode from historical-anchor mode
  in client telemetry when diagnosing jumps or missed messages. The bounded
  client events
  `tank_chat_scroll_client_events_total{event="navigation-mode-entered-live-tail"|"navigation-mode-entered-historical-anchor"}`
  emit on every transition; the structured slog line carries the bounded
  transition reason.
- Timeline page requests should log or count anchor type, direction, and
  cursor validity without logging message contents.
- Resync, invalid cursor, anchor-not-found, and unexpected viewport-reset
  cases should be observable as user-trust navigation failures. The
  `TankChatScrollUserAtBottomLatched` alert is the named user-trust failure
  for "user-visible navigation state contradicts durable read cursor"; its
  runbook resolves to the durable diagnostic surface
  `GET /api/debug/conversation-read-state`.
- A report that "refresh moved me" or "new messages moved the transcript"
  should be diagnosable from durable cursor inputs plus client navigation
  telemetry — without browser devtools. The durable inputs are
  `conversation_read_state.last_read_order_key` and
  `sessions.activity_summary.last_order_key`; the client telemetry is the
  navigation-mode event stream named above.
- Navigation mode is an explicit state machine
  (`frontend/src/navigationMode.ts`), not a layout-state mirror. The
  retired DOM-distance heuristic and its supporting boolean
  (`userScrolledUp`, `transcriptVisuallyAtBottom`,
  `TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX`) are blocked from
  reintroduction by `scripts/check-removed-chat-runtime.mjs`.
- Turn-number resolution is observable. `tank_turn_number_resolve_total{result}`
  counts number→turn_id lookups as `ok` / `not_found` / `invalid`. The durable
  allocation invariant — every materialized turn has a number — is enforced by
  `tank_turn_number_missing_total{phase}` and the `TankTurnNumberMissing` alert,
  which fire if a `turn_activity` shell is ever projected for a turn that has no
  `session_turns` row while numbering is active (allocation trigger regressed or
  an event bypassed it).

## Acceptance Checks

- Normal session open lands at the live tail.
- A copied message link resolves through a durable cursor and lands on the
  target message after reload.
- Back-pagination preserves the visible anchor while older messages are
  prepended.
- Live messages received while reading history do not move the viewport to the
  tail.
- Returning to live tail resumes live-follow behavior and clears the separate
  new-message affordance.
- Load, ready, reconnect, and resync do not introduce scroll jumps in either
  live-tail mode or historical-anchor mode.
- Legacy browser-local scroll restoration cannot reappear without failing a
  migration guard or contract test.
- A turn deep link uses the durable per-session number
  (`/sessions/{id}/turns/{n}`) and resolves server-side: an in-window turn
  resolves from the loaded shells, an out-of-window turn resolves via
  `GET /api/sessions/{id}/turns/{n}`, and an unknown or non-numeric segment
  renders an explicit unavailable-target state instead of silently falling back
  to the latest turn. The retired `turn_<uuid>` public route form and the
  array-position "Turn N" label cannot reappear without failing
  `scripts/check-removed-chat-runtime.mjs`.
- Opening a pending `needs_input` turn lands on the question-set page, not the
  last output page. Multiple questions from one AskUserQuestion invocation stay
  on the same page, and the surrounding activity pages remain reachable through
  the page selector.
- The Turns view always shows the dedicated Page dropdown for a selected turn: a
  single-page turn renders it disabled ("Page 1 of 1"); a multi-page turn lists
  Page 1..N and selecting one re-reads that page via `?page=N`. The control is
  not hidden when `page_count` is 1. The pure gate
  `frontend/src/turnActivityPager.ts` (tested in `turnActivityPager.test.ts`)
  owns the clamped current page, total page count, and disabled state, blocking
  a regression to a threshold-hidden control.
