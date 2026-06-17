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
- `sessions.user_message_count` owns the durable per-session count of human
  back-and-forths (`user_message.created` events). It is an advance-only
  projection over the append-only `session_events` ledger (the same model as
  `compaction_count`), carried on the snapshot and row-update wire; it is never
  derived from the loaded transcript window. It is metadata only for navigation:
  normal session opens land on Turns regardless of count.
- `session_transcript_row_backfills` owns whether a session's historical
  `session_events` ledger has been projected into transcript rows for the
  current projection version. Status rows alone do not satisfy backfill; stale
  sessions are materialized on demand for the requested session before
  `/timeline` or transcript SSE read from `session_transcript_rows`.
- The **durable turn directory** owns the *selectable turn set* — the list of
  turns the Turns selector enumerates. It is served by
  `GET /api/sessions/{id}/turns/directory` (and the public-share / admin-hidden
  mirrors) as the COMPLETE, submission-ordered set of `turn_activity` shells
  (each stamped with its `session_turns.turn_number`), independent of the
  bounded `/timeline` window. The browser never derives the turn set from
  whatever transcript window it has loaded: a turn is listable iff the durable
  directory lists it. This extends the turn-number resolver invariant from "the
  durable row resolves one number" to "the durable directory owns the whole
  turn set." Background-task wake turns (`turn_bgtask-*`) carry no number and no
  own shell — they fold into their originating turn — so the directory excludes
  them by construction, matching the projection. The directory is bounded by
  `TurnDirectoryMaxRows`; on overflow the newest turns survive and the response
  is marked `truncated` (observable, never a silent cap).
- Server timeline pages own bounded windows of top-level transcript rows. Raw
  events inside a collapsed Turn activity row are loaded only through the
  explicit Turn activity endpoint. The bounded `/timeline` window is the chat
  surface's history pager; it is NOT the source of the Turns selector's turn set
  (that is the durable turn directory above). A turn's expansion body still
  loads lazily through the Turn activity endpoint whether the turn is in the
  current `/timeline` window or only in the directory.
- Turn activity itself paginates. A turn's expansion body is split into pages
  sealed at `turnPageEventLimit` events and at semantic AskUserQuestion
  boundaries; each `turn.awaiting_input` event starts one `question_set` page
  per question while preserving one durable answer set. Each question page
  carries the shared `questionSet` number plus its `questionIndex` and
  `questionCount`, so the UI can label the set and offer previous/next question
  shortcuts through the existing page selector rather than introducing nested
  question navigation. The preceding activity page gets a compact
  AskUserQuestion invocation marker derived from the same durable event, even
  when that means a marker-only first page. The Turn
  activity endpoint (`server_turn_activity_v3`) returns the page directory
  (`page`, `page_count`, `pages[]`) and accepts `?page=N`. A `needs_input` turn
  defaults to the first unanswered `question_set` page; all other turns default
  to the latest page. The page boundary is a durable `order_key`-range concept,
  so a selected page is stable across reload and deep links. The shell's
  active/terminal status is never a function of which page rendered — it is
  folded from the complete turn so a finished long turn can never render as
  perpetually active. The same response carries a separate `turn_context`
  projection for the initiating instruction: human turns use the durable
  `user_message.created` row, and backend-owned background-task wake turns use
  durable `turn.submitted.payload.prompt` with system authorship. The context
  is outside the page body so numbered turn routes remain oriented on every
  selected page.
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

- Opening a session normally lands in the Turns view, on the latest turn when one
  exists and on the empty Turns state for a newly-created session.
- The main transcript is an explicit fallback/artifact reachable from Session
  Data at `/sessions/{id}/transcript`; it is no longer the root session view.
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
- The Turns view's turn selector lists every turn in the durable turn directory,
  not just the turns in the loaded `/timeline` window. Directory loading is a
  level-triggered reconciler (`frontend/src/turnDirectoryLoad.ts`): a visible,
  loadable pane whose directory is not loaded and has no load in flight for the
  current session always has a load running, and it refreshes when the live
  stream surfaces a turn the directory does not yet list (a newly submitted
  turn), so the selector reaches Turn 1 of a long session without the reader
  paging the chat window back. Selecting a turn the chat window never loaded
  resolves through the same lazy `/activity` load as any other turn — the detail
  surface is already directory-agnostic. While the directory has not loaded the
  view shows an explicit loading or retryable error state; it never silently
  lists only the loaded window. Because loading is level-triggered and keyed to
  the session epoch (an `AbortController`, not a permanent boolean latch), a
  stranded "Loading turns…" spinner cannot persist: switching the pane between
  sessions aborts the superseded load, and any non-terminal state with nothing
  in flight reconciles into a fresh load rather than waiting for a remount
  (reload / nav-away-and-back). Recovery is never remount-only; the retryable
  affordance is reachable from the loading state too, not only from `error`. The
  just-submitted turn appears immediately as the active "Current turn" (from
  `renderedActiveTurnId`) and gains its durable number when the directory
  refresh lands.
- The Turns view exposes a dedicated **Page dropdown** beside the turn selector.
  It is present whenever a turn is selected; a single-page turn renders it
  **disabled** ("Page 1 of 1") rather than omitting it, and a turn that crosses
  the per-turn event seal enables it as a page picker (Page 1..N). The control is
  never hidden — the pagination affordance stays visible even when there is
  nothing to navigate to. (The Turns view reads the page-defaulted `/activity`
  endpoint, so without this control a long turn would show only its last page
  there with no way back.)
- On a compact viewport (`useViewport().isCompact`, <= BP_COMPACT = 768px) the
  desktop stepper and the combined turn/page picker collapse into a single
  always-present position button (`.run-turn-pager-compact-trigger`, showing
  "Turn N · Page P") that opens the identical controls in a bottom `Sheet`. This
  is the sanctioned compact rendering of the never-hidden invariant: the button
  is never omitted while a turn is selected and renders disabled ("No turns")
  when there is no turn, navigating closes the sheet, and the desktop control is
  unchanged.
- The Turns view shows the selected turn's server-projected initiating message
  above the activity page body when that turn has a human `user_message.created`
  event. It must not rediscover that message from the loaded transcript window
  or treat it as a paged activity entry.

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
  an event bypassed it). The same counter uses `phase="submit_response"` when a
  freshly accepted `/turns` response cannot include the durable number, so Turns
  composer routing gaps localize to the submit boundary instead of the later
  transcript projection.
- The durable turn directory is observable on both sides.
  `tank_turn_directory_list_total{result}` counts directory reads as `ok` /
  `truncated` / `error`, and `tank_turn_directory_size` is a histogram of the
  per-session turn count returned — it observes the live distribution so the
  `TurnDirectoryMaxRows` cap can be revisited (with cursor paging) before it ever
  bites, and a sustained `truncated` rate names sessions the cap is eliding. The
  SPA emits bounded `turn-directory-request` / `turn-directory-loaded` /
  `turn-directory-error` client events so a directory load that fails (leaving
  the retryable Turns error state) is diagnosable without browser devtools.
- Client directory loading observes not only loads that *fail* but loads that
  *never start* — the failure the SPA must not record nothing for. The loader is
  a level-triggered reconciler (`frontend/src/turnDirectoryLoad.ts`): a visible
  pane with a non-terminal status and nothing in flight always re-drives a load,
  so a stranded "Loading turns…" spinner cannot survive without recovery, and a
  load is bounded by a wall-clock timeout that surfaces a wedged connection as
  the retryable `error` state rather than an eternal spinner. Three further
  bounded client events make the strand class diagnosable: `turn-directory-stuck`
  (the spinner exceeded the watchdog threshold — the user-trust signal, paired
  with the `TankTurnDirectoryStuck` alert), `turn-directory-reconcile` (the
  reconciler auto-healed a load that vanished), and `turn-directory-timeout` (a
  load was abandoned into the retryable error state). The retired edge-triggered
  single-flight loader and its boolean latch (`loadTurnDirectoryInFlightRef`),
  which could strand the spinner with no in-flight work and no telemetry, are
  blocked from reintroduction by `scripts/check-removed-chat-runtime.mjs`.

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
- The Turns selector lists every turn of a session longer than the `/timeline`
  tail window. Opening such a session and opening the turn selector shows Turn 1
  (not just the recent tail); selecting Turn 1 loads its activity. The selector's
  turn set is sourced from `GET /turns/directory`, never from the loaded
  transcript window — `scripts/check-removed-chat-runtime.mjs` fails if
  `turnViewItems` is rebuilt from `renderedEntries` (the window-derived
  selector). A failed directory load shows a retryable error in the Turns view,
  not a window-only list. Background-wake turns do not appear as separate
  selector entries (they fold into their originating turn, matching the
  directory).
- Clicking between sessions in the tabs view loads each session's turns without
  remount. A pane left showing "Loading turns…" with no load in flight recovers
  on its own (the level-triggered reconciler re-drives the load) rather than
  requiring a reload or nav-away-and-back; switching to a session while a prior
  session's load is in flight does not strand the new session on the spinner. A
  directory load that wedges times out into the retryable error state instead of
  spinning forever, and Retry is reachable from the loading state, not only from
  `error`. The retired edge-triggered single-flight loader and its
  `loadTurnDirectoryInFlightRef` latch cannot reappear without failing
  `scripts/check-removed-chat-runtime.mjs`; `frontend/src/turnDirectoryLoad.test.ts`
  pins that a visible non-terminal pane with nothing in flight always loads.
- Opening a pending `needs_input` turn lands on the question-set page, not the
  last output page. Multiple questions from one AskUserQuestion invocation stay
  in one answer set but render as adjacent semantic `question_set` pages, and
  the surrounding activity pages remain reachable through the page selector.
- The Turns view always shows the dedicated Page dropdown for a selected turn: a
  single-page turn renders it disabled ("Page 1 of 1"); a multi-page turn lists
  Page 1..N and selecting one re-reads that page via `?page=N`. The control is
  not hidden when `page_count` is 1. The pure gate
  `frontend/src/turnActivityPager.ts` (tested in `turnActivityPager.test.ts`)
  owns the clamped current page, total page count, and disabled state, blocking
  a regression to a threshold-hidden control.
- Opening any available numbered turn route shows the initiating instruction
  above the activity page body. Human turns source it from `user_message.created`;
  backend-owned background-task wake turns source it from
  `turn.submitted.payload.prompt`. Page changes keep that context visible
  without duplicating it in `entries`.
