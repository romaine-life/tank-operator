# Transcript Capabilities

This ledger names user-facing behavior under the Transcript feature area. It is
not a backlog; entries exist when behavior needs a stable handle for planning,
review, tests, incident follow-up, or retirement.

## Public Message Links

Status: active

Intent:
Copying a transcript message link mints a durable opaque bearer share token so
the URL can open for unauthenticated viewers without exposing transcripts by
guessable `session` and `message` query parameters alone. **Message-link
shares grant the whole session read-only (owner decision 2026-06-12, #1077):**
the link anchors the viewer at the shared message but the token authorizes the
full transcript (any anchor/cursor, full back/forward pagination) and the
Turns views (any turn's activity detail in the shared session), not just a
window around one message. The token is a 32-byte opaque random hex value
(`messageLinkShareTokenBytes`), non-enumerable, scoped to exactly one session
(`share.SessionScope` + `share.SessionID` from the validated row — never
caller-supplied session ids). The public view is a read-only transcript
surface: no session sidebar, no composer, no Files, Settings, Background, or
mutable controls.

Share-token route surface (complete enumeration — every route that accepts the
token; all GET-only, all reads scoped to the share row's session):
- `GET /api/public/message-links/{token}` — session metadata + redacted owner.
- `GET /api/public/message-links/{token}/timeline` — transcript rows; honors
  the same anchor/cursor read intents as the authenticated timeline, scoped to
  `share.SessionID`.
- `GET /api/public/message-links/{token}/turns/{turn_id}/activity` — turn
  activity pages for any turn in the shared session.
- `GET /api/public/message-links/{token}/avatars` and
  `.../avatars/{avatar_id}/{image,backing}` — only the session's assigned
  agent/system avatar ids are served.
- `GET /` with `?share={token}` — the SPA shell / JSON message-link contract
  (`handleTankMessageLink`), which inlines the same public timeline body.
- `GET /api/public/session-report-shares/{token}` shares the token store but
  is a separate snapshot surface; report tokens fail closed on message-link
  routes (no registered session) and message-link tokens fail closed on the
  report route (snapshot decode rejects them).
There is no write-method registration under `/api/public/`; minting shares
stays on the authenticated, owner-scoped
`POST /api/sessions/{session_id}/message-links`.

Affected contracts:
- Transcript
- Transcript Navigation
- Auth And Streams
- App Chrome

Contract impact:
- Public reads are explicitly bearer-token gated; unauthenticated access to
  the authenticated session API remains unsupported.
- The copied link still targets durable transcript-row identities and pages
  the same server-owned transcript row model as authenticated timeline reads.
- Public timeline and public turn-activity reads materialize-on-read
  (`ensureSessionTranscriptRows`) exactly like the authenticated timeline and
  fail closed with 503 `transcript materialization failed` — after a
  projection-version bump a share link serves the re-projected session
  immediately instead of stale/empty rows that only an authenticated read
  would repair.
- Live updates: public viewers are **snapshot-on-load**. There is no public
  SSE path — `/api/sessions/{id}/events` requires a browser stream ticket
  minted through bearer auth, and the SPA's `publicView` mode never opens the
  event stream (`openSdkEventStream` returns early). Reloading or paging
  re-reads the durable projection. Building a token-scoped public stream is a
  deliberate non-goal until the product asks for it; do not bolt the
  authenticated stream-ticket path onto share tokens.
- The public SPA route renders a distinct full-screen workspace without the
  authenticated app sidebar or composer, preserving App Chrome's ownership of
  signed-in navigation. `publicView` routes timeline and turn-activity
  fetches through the `/api/public/message-links/...` endpoints with plain
  `fetch` (no Authorization header) and gates off composer, quote/fork,
  read-state writes, background/control fetches, and SSE.

Evidence:
- Backend: `backend-go/cmd/tank-operator/handlers_message_link_share_test.go`
  covers share creation, unauthenticated whole-session timeline reads
  (`TestHandlePublicMessageLinkTimelineMaterializesStaleTranscriptRowsBeforeRead`),
  arbitrary-turn activity reads with materialize-on-read
  (`TestHandlePublicMessageLinkTurnActivityMaterializesAndServesArbitraryTurn`),
  503 fail-closed on materialization failure, and the GET-only route guard
  (`TestPublicMessageLinkRoutesAreGETOnly`).
- Frontend: `frontend/src/migrationPolicy.test.ts` pins the public message-link
  shell, the public API path wiring for both timeline and turn-activity, and
  the hidden-composer invariant.

## Compact Agent Activity

Status: active

Intent:
Condense the noisy work inside an agent turn into a stable Turn activity row
without making transcript items visibly bounce between the activity/log surface
and the settled conversation surface.

Affected contracts:
- Transcript
- Transcript Navigation
- Tank Conversation Protocol

Contract impact:
- The durable `session_events` ledger remains the only source of transcript
  events and order.
- Turn activity is an activity/log projection; the main transcript is the
  settled conversation projection.
- Assistant prose may be duplicated across those projections, but a rendered row
  must not relocate between them.
- The dedicated Turns detail view gets its final-answer display from
  `/turns/{id}/activity` `final_answer.entries`, which is projected from durable
  `turn.completed.payload.final_answer.timeline_ids`. The browser no longer
  decides that an assistant row is final because it is absent from
  `compactedEntryIds`.
- Collapsing agent activity is the default Turns chat projection. Active turns
  default compacted from the server-owned active shell, keeping only context plus
  the generic `Thinking...` affordance visible while new self-talk/tool rows
  remain hidden until the user expands that turn. Completed turns with
  server-projected `collapse.default_collapsed=true` default to final-answer
  compact mode, keeping the final answer and server-owned wake/context rows
  visible. Failed, interrupted, and no-final completed turns do not expose a
  compacted final-answer projection because there is no durable assistant result
  to show.
- The settled main-transcript answer remains canonical for message links,
  unread counts, latest-message state, and fork-from-message actions.

Evidence:
- Contract/docs: `docs/tank-conversation-protocol.md` and
  `docs/features/transcript/contract.md` name the no-bounce projection rule.
- Required before future implementation work is complete: a focused unit or
  browser test proving active-turn assistant prose followed by later work does
  not first appear as a settled transcript row and then move into Turn activity.
- Required before future implementation work is complete: a check proving a
  completed turn can retain a Turn activity log copy of assistant prose while
  exposing only one settled transcript message for counts and message actions.

## Turn Activity Pagination

Status: active

Intent:
Show every event of an arbitrarily long turn, and always reflect the turn's
durable terminal, without folding a fixed-size prefix that drops the terminal
and renders a finished turn as perpetually active.

Affected contracts:
- Transcript
- Transcript Navigation
- Tank Conversation Protocol

Contract impact:
- The turn-activity shell's `active`/terminal status, counts, and `completedAt`
  are folded from the **complete** set of a turn's `session_events`, never a
  bounded prefix. The terminal is the last event of a long turn and can never be
  a casualty of a body window.
- The expansion body paginates: a turn splits into pages sealed at
  `turnPageEventLimit` (1000) events. The endpoint (`server_turn_activity_v3`)
  returns the page directory and defaults to the last page; `?page=N` selects
  another. Page boundaries are durable `order_key` ranges, so a selected page is
  stable across reload and deep links.
- The same endpoint returns `turn_context`, a server-projected copy of the
  initiating durable user message when the turn has one. The dedicated Turns
  view renders that context above the paged body so a numbered turn route stays
  oriented on every page; the prompt is not duplicated into `entries`, and
  canonical message actions remain owned by the main transcript row.
- The page navigator is an always-present affordance, not a threshold-gated
  control. It lives in the dedicated Turns view (`RunTurnActivityScreen`, the
  surface users open to inspect a turn) as a **Page dropdown** beside the turn
  selector: rendered even for a single-page turn (disabled "Page 1 of 1"), and a
  page picker (Page 1..N) once a turn crosses the event seal. So pagination never
  reads as absent on a normal-length turn, and a long turn is never silently
  capped to its last page in the Turns view. `frontend/src/turnActivityPager.ts`
  is the pure gate for the current page / total count / disabled state.
- The fixed-size per-turn read that truncated long turns oldest-first is deleted
  end to end (the bounded `EventsForTurn` store method no longer exists); reads
  go through `EventsForTurnAfter` paged to exhaustion.

Evidence:
- Backend unit: `backend-go/cmd/tank-operator/turn_pages_test.go` proves an
  over-limit turn keeps a `completed` shell and seals pages at the threshold.
- Backend contract: `TestHandleSessionTurnActivityPaginatesOverLimitTurnWithTerminalShell`
  proves the endpoint reports a completed shell, `page_count >= 2`, defaults to
  the last page, and returns server-owned `final_answer.entries` plus collapse
  metadata.
- Observability: `tank_transcript_materialization_invariant_violation_total{invariant="active_shell_after_terminal"}`
  + `TankTurnActiveWithDurableTerminal` guard the regression; `tank_turn_activity_event_count`
  / `tank_turn_activity_page_count` track long-turn frequency.
- Frontend gate: `frontend/src/turnActivityPager.ts` +
  `frontend/src/turnActivityPager.test.ts` prove the control stays visible
  (disabled, "Page 1 of 1") at a single page and expose the clamped current page
  + total count for the picker — the regression guard against a threshold-hidden
  control. The Page dropdown is rendered by the Turns view
  (`RunTurnActivityScreen`) in `frontend/src/App.tsx`.

## Turns View Chat Submission

Status: active

Intent:
Let the Turns view become a durable continuation surface for overloaded
conversations. A user reading Turn activity can send the next message without
returning to the main transcript; the message creates a normal new Tank turn and
the Turns view selects that new turn as soon as the backend accepts the durable
boundary.

Affected contracts:
- Transcript
- Transcript Navigation
- Agent Runners
- Auth And Streams

Contract impact:
- The authenticated Turns view renders the same `ChatComposer` and tool-button
  surface as the main transcript. Public message-link views remain read-only and
  do not expose the composer.
- Submit still goes through `POST /api/sessions/{session_id}/turns`; there is
  no turns-only route, local queue, or parallel submit path.
- The backend response includes the deterministic `turn_id` and the durable
  `turn_number` when session turn numbering is active. The browser uses that
  identity to select and route `/sessions/{id}/turns/{n}` while the
  server-projected transcript row catches up; it never derives the number from
  loaded row position.
- Queued follow-up inputs remember whether they were created from Turns, so a
  message typed there while another turn is running still selects its own new
  turn when it is later submitted.

Evidence:
- Backend: `backend-go/cmd/tank-operator/handlers_turns_test.go` proves the
  normal `/turns` submit response includes the durable turn id and number after
  boundary persistence.
- Frontend: `frontend/src/migrationPolicy.test.ts` pins the authenticated
  chat-or-Turns composer visibility, the Turns-origin submit surface, and the
  `turn_number` route anchor.

## Context Compaction Notice

Status: active (Claude); Codex blocked on provider signal

Intent:
Surface provider context compaction — the agent summarizing earlier
conversation to reclaim context-window space — as a durable, visible
Turn-activity row, so a user inspecting a long turn can see that the agent's
memory of earlier turns was condensed, and whether it was automatic or a manual
`/compact`. It is intra-turn system noise (the same tier as tool calls and
reasoning), so it lives in the turn's activity disclosure, not the settled
transcript. Previously this was invisible: the Claude SDK's
`system/compact_boundary` fell through the runner adapter to a silent `return
[]`, so neither the durable ledger nor the UI recorded it. Two sessions that
compacted left no transcript trace, which is what surfaced this gap.

Affected contracts:
- Transcript
- Tank Conversation Protocol

Contract impact:
- `context.compacted` is a first-class Tank event type (schema + runner-shared
  + Go contract, kept in lockstep by the contract checks). The runner is its
  sole producer; the durable `session_events` ledger records every compaction,
  queryable independently of the UI.
- The server projection records it as an ordinary mid-turn Turn-activity row
  (`meta`, `metaKind: context_compacted`), folded into the turn's collapsed
  activity shell like any other non-final-answer row and absent from the settled
  transcript — satisfying the Transcript contract's no-bounce invariant. It
  shares AskUserQuestion's settled-transcript exclusion, while AskUserQuestion
  additionally owns a semantic `question_set` turn page because it requires
  user action. The frontend renders compaction through the existing
  `RunMetaBlock` primitive in the Turn-activity disclosure. An earlier
  implementation promoted it into the settled transcript and excluded it from
  the activity compact, which made it flash-then-vanish on the per-turn detail
  screen; that promotion path was deleted.
- The silent-drop class that hid it is now observable:
  `tank_runner_unmapped_provider_event_total{type,subtype}` counts any provider
  event the adapter neither maps nor explicitly ignores. Steady state zero.

Evidence:
- Schema/contract: `schemas/tank-conversation-event.schema.json`,
  `runner-shared/conversation.{js,d.ts}`,
  `backend-go/internal/conversation/types.go`;
  `schemas/tank-conversation-event.fixtures.json` carries the canonical fixture
  validated by `scripts/check-tank-conversation-contract.mjs` and the Go
  contract test.
- Producer: `claude-runner/src/adapters/claude.ts` maps
  `system/compact_boundary`; `claude-runner/src/adapters/claude.test.ts` pins
  the mapping and the malformed-metadata default.
- Producer: `codex-runner/src/appServerTransport.ts` maps the Codex App Server
  `thread/compacted` notification and generated `contextCompaction` item
  lifecycle to the runner's `context.compacted` event, deduped by provider turn
  id. `codex-runner/src/adapters/codex.ts` emits the durable Tank envelope with
  `source=codex`.
- Projection: `backend-go/cmd/tank-operator/transcript_projection.go`
  (`applyContextCompacted`); `transcript_projection_test.go`
  (`TestProjectTranscriptEventsRecordsContextCompactedAsTurnActivity`) proves it
  is recorded as a turn-activity child folded into the shell and is absent from
  the settled transcript — the guard against the promotion path returning.
- Observability: `claude-runner/src/metrics.ts` →
  `tank_runner_unmapped_provider_event_total`.

Codex-specific notes:
- The installed Codex App Server protocol (`@openai/codex@0.130.0`,
  generated via `codex app-server generate-ts`) exposes
  `thread/compacted { threadId, turnId }`, marked deprecated in favor of a
  `contextCompaction` item type. Tank maps both surfaces to the same durable
  notice and records one row per provider turn. Codex does not expose reliable
  manual/auto trigger or pre-token metadata on these surfaces, so the runner
  defaults `payload.trigger` to `auto`.

## Background Wake Continuation Projection

Status: active

Intent:
Treat a `run_in_background` wake as a continuation mechanic inside the user's
larger simulated turn, not as a new standalone chat exchange. The wake prompt
and wake activity are useful audit data for the Turns view, but they should not
trick the user into reading operational noise as settled conversation. The main
transcript remains quiet until the resumed agent reaches a true final answer.

Affected contracts:
- Transcript
- Tank Conversation Protocol

Contract impact:
- The background-task wake fire path omits the synthetic wake prompt from
  `user_message.created`; it persists the text on the `turn.submitted`
  boundary as `payload.prompt` and publishes the runner command with
  `source=background-task`.
- `turn.submitted` stays schema-valid (`source=tank`) and carries
  `payload.source=background-task` so the server projection can recognize the
  continuation without weakening the event envelope.
- Background wake turn-activity shells are suppressed from the settled main
  transcript whenever the originating turn is derivable: the wake body then
  folds — in durable order-key order — into the originating turn's shell, which
  is that content's durable home. A wake turn whose lineage cannot be derived
  keeps its own shell instead (fail-soft); projected content is never dropped
  without a surviving container.
- The background wake boundary projects as a `meta` chip
  (`metaKind: background_task_wake`) inside Turn activity — "Background task
  finished — agent re-invoked" (or "Agent continued on its own" for the
  antigravity self-continuation relay) — never as a user-side message bubble.
  The `turn.submitted.payload.prompt` text is AGENT-DIRECTED harness
  instruction; rendering it raw in the user's chat voice was the "wake-notice
  prose rendered raw" defect. The full prompt stays on the chip's
  `payload.prompt` as audit/debug detail, and the chip keeps the
  `wakePrompt`/`turnOnly` flags so it stays always-visible when a completed
  turn's activity collapses.
- The turn that parked on a still-running background task KEEPS its activity
  shell in the settled projection — parked is a state on the shell
  (`activity.continuation: true`), not grounds for suppression. The shell is
  what carries the durable stamped turn number and what makes the compacted
  body reachable; suppressing it annihilated parked turns' content from the
  durable read model (the tank-operator-slot-1 session-161 bug museum, whose
  real ledger now replays in
  `transcript_projection_replay_test.go`). The parked turn's provisional
  assistant prose and background-task row still stay inside Turn activity —
  they do not settle as main-transcript prose.
- A final assistant answer from the resumed turn can still be promoted into the
  main transcript, but only through the normal
  `turn.completed.payload.final_answer.timeline_ids` marker. The promoted row is
  attributed to the originating user-visible turn and retains the wake backend
  turn id for audit/debug detail.
- Across a folded continuation chain, the chain's LAST completed terminal owns
  the turn-detail final answer. A parked origin turn's promoted ack is
  superseded by its continuation; rendering it as the page's final answer below
  later wake content is the answer-replacement defect. When the last completed
  link promoted nothing, the turn has no final answer — no fallback may
  resurrect the superseded ack.

Evidence:
- Walkthrough: `docs/features/transcript/background-wake-turn-flow.html`
  records the intended main-transcript, Turns-view, backend-boundary, and
  rejected-shape projections for background wake continuations.
- Backend fire path: `backend-go/cmd/tank-operator/background_task_wakes_test.go`
  (`TestFireBackgroundTaskWakeUsesDurableTurnBoundary`) proves the wake writes
  `turn.submitted` only, with `payload.source=background-task` and
  `payload.prompt` carrying the system-user wake text.
- Projection: `backend-go/cmd/tank-operator/transcript_projection_test.go`
  (`TestProjectTranscriptEventsKeepsBackgroundTaskWakeMechanicsOutOfMainTranscript`
  `TestProjectTranscriptEventsPromotesFinalAnswerFromBackgroundTaskWake`, and
  `TestProjectTranscriptEventsHidesBackgroundContinuationTurnFromMainTranscript`)
  proves wake mechanics and provisional parking-turn prose stay out of the main
  transcript while true final-answer promotion still works and stays owned by the
  originating user-visible turn.
- Turn detail: `backend-go/cmd/tank-operator/handlers_session_events_test.go`
  (`TestHandleSessionTurnActivityIncludesBackgroundWakeContinuation`) proves the
  parent Turn activity payload includes both the background/timer row and the
  resumed wake final message, and that the wake prompt row carries
  `wakePrompt`/`turnOnly` (the flags the Turns view keys on to render the
  system-user bubble; without them the row is dropped as an ordinary user
  message — the "wake never shows" regression guard).

Chained-wake hardening (a wake turn that itself launches a background task):
- A wake-of-a-wake collapses transitively to the originating *real* turn, never
  to an intermediate wake turn, so no synthetic `turn_bgtask-*` turn surfaces as
  a standalone user-visible turn (the session 655 / turn 56 "system message shows
  twice" defect). `backgroundTaskWakeParentTurnsFromTasks` walks the continuation
  chain to the non-wake ancestor; `readUserFacingTurnEvents` reads the whole
  transitive chain into the origin turn's `/activity` body.
- A re-fired wake (a stale-claim re-submit publishes a second
  `turn.submitted` with the same wake turn id) projects exactly one prompt: the
  apply path is idempotent per wake turn, and the fire path
  (`fireBackgroundTaskWake`) skips a wake whose deterministic turn already exists
  in the durable ledger (`tank_background_task_wake_fire_total{result="already_fired"}`).
- Wake turns are no longer assigned user-facing turn numbers (migration `0139`
  excludes `turn_bgtask-*` from `tank_allocate_session_turn_number`); historical
  wake-turn numbers fold to their originating real turn at resolve time
  (`handleResolveSessionTurnNumber` → `resolveBackgroundWakeOriginTurn`,
  `tank_turn_number_resolve_total{result="folded_wake"}`).
- The system-user wake prompt stays visible in the Turns view even when a
  completed turn's activity log is collapsed behind the divider
  (`isAlwaysVisibleTurnDetailEntry`), since it is settled context (why the agent
  resumed), not collapsible tool noise.
- Evidence: `transcript_projection_test.go`
  (`TestProjectTranscriptEventsCollapsesChainedBackgroundWakeIntoOriginTurn`),
  `frontend/src/turnActivityCache.test.ts` (wake prompt stays visible under
  collapse), and the `wakePrompt`/`turnOnly` assertions in
  `TestHandleSessionTurnActivityIncludesBackgroundWakeContinuation`.

## Session Background Task Ledger

Status: active

Intent:
Surface a session's background (`run_in_background`) shell tasks — a timer, a
watcher, a sub-agent — in the Background screen, both running and recently
completed. Background tasks are durable `shell_task.*` events that fold into
per-turn activity; they were never top-level transcript rows, so the Background
screen's old `renderedEntries.filter(isBackgroundTaskEntry)` was structurally
always empty and a backgrounded task (e.g. a `sleep` timer that finished while
the session was idle) showed nowhere.

Affected contracts:
- Transcript
- App Chrome (owns the Background screen)
- Observability

Contract impact:
- The feed is a projection over the durable `session_events` shell-task ledger,
  not browser-local optimism and not the main transcript rows. `GET
  /api/sessions/{id}/background-tasks` returns the projected `background_task`
  entries; the SPA feeds the Background "shells" view (running AND recently
  completed), while the active-only subset drives the badge count so the pill
  does not grow unbounded.
- The read is bounded regardless of ledger size: a partial index
  (`session_events_shell_task`, migration `0140`) makes it an indexed scan over
  only shell-task rows, so the polled endpoint never re-reads the whole event
  ledger.
- The Background screen's `background_task` source moves off `renderedEntries`
  (which never contained `background_task` rows — they live only inside per-turn
  activity bodies) onto the durable feed; the old empty-by-construction
  transcript-row filter is not kept as a fallback.

Evidence:
- Backend: `backend-go/cmd/tank-operator/transcript_projection_test.go`

## Scheduled Wakeup Event Ledger

Status: active

Intent:
Make an agent's self-scheduled timer visible while it is still pending. A
successful `ScheduleWakeup`/`schedule` registration must not leave the user with
only an invisible backend row and a promise that the wakeup will happen later.

Affected contracts:
- Transcript
- Agent Runners
- App Chrome (owns the Background screen)

Contract impact:
- Scheduled wakeup lifecycle transitions persist `scheduled_wakeup.updated` in
  the durable `session_events` ledger. The event is authored by Tank, keyed by
  `timeline_id=scheduled-wakeup:{wakeup_id}`, and carries the due time, prompt,
  status, client nonce, provider item id, fired turn id, and error fields needed
  to explain the timer.
- `/timeline` returns `scheduled_background_tasks` as a one-shot bootstrap from
  durable scheduled-wakeup rows. After bootstrap, the SPA updates Background ->
  Scheduled from the existing session event stream's projected `transcript-rows`
  events. Browser polling of `/scheduled-wakeups` is not the live path.
- Scheduled wakeup projections are `background_task` rows with
  `taskKind=scheduled_wakeup` and `backgroundOnly=true`; they are available to
  the Background screen without leaking into the main transcript.
  (`TestProjectSessionBackgroundTasksListsRunningAndCompleted`) proves the
  shell-task lifecycle projects to running + completed `background_task` entries.
- Store: `postgresSessionEventStore.ShellTaskEvents`, served by the
  `session_events_shell_task` partial index.
- Observability: `tank_session_background_tasks_list_total{result}`.

## Session Lifecycle In Turn Activity

Status: active

Intent:
Treat session-startup notices (`session.status` `Session is loading.` /
`Session is ready.`) as turn noise, not conversation. They are the same tier as
tool calls, reasoning, AskUserQuestion, and context compaction, so they fold
into the owning turn's Turn activity (the Turns view) instead of rendering as
standalone system rows in the main transcript. This keeps chat a clean record of
user/agent messages and fixes the turn-one defect where a startup notice — whose
durable `order_key` predates the first user message — sorted *above* that message
in the conversation. Provider credential banners and any `failed` status are not
startup noise: they stay promoted as top-level system messages so failures and
recoveries stay visible.

Affected contracts:
- Transcript
- Tank Conversation Protocol

Contract impact:
- The server projection marks only plain startup notices (`loading`/`ready`
  whose `timeline_id` is not a `.../provider/.../status` banner) as foldable,
  assigns each the owning turn (the turn whose `order_key` epoch contains it; a
  notice before the first user message is owned by that first turn), and folds it
  into that turn's activity via the existing compaction. A foldable notice with
  no owning turn produces no row. The activity shell's `startOrderKey` is
  anchored to the turn's first post-message event, so folded pre-message notices
  never drag the shell above the user message in the durable row order.
- Provider banners (`.../provider/.../status`, including the recovery "back
  online" `ready`) and `failed` keep their top-level placement and severity.
- No new event type, schema, fixture, or runner change — `session.status` events
  are unchanged; only their projection altitude moves.
- The sessions trigger writes `session.status` loading/ready into the durable
  `session_events` ledger only; it does not seed `session_transcript_rows` for
  those startup notices. Direct startup transcript rows from the old trigger are
  deleted by the forward migration, and the transcript-row projection version is
  bumped so stale materializations rebuild through the server projection.

Evidence:
- Projection: `backend-go/cmd/tank-operator/transcript_projection.go`
  (`applySessionStatus` marker, `assignSessionStatusOwnership`,
  `dropOrphanSessionLifecycle`, shell `startOrderKey` anchor) and the
  `adoptLeadingSessionLifecycle` seam in `turn_pages.go` /
  `transcript_rows_materializer.go` so the materializer and the lazy `/activity`
  body fold identically.
- Tests: `transcript_projection_test.go`
  (`TestProjectTranscriptEventsFoldsSessionLifecycleIntoTurn`,
  `…DropsOrphanSessionLifecycle`, `…KeepsFailedSessionBannerPromoted`,
  `…KeepsProviderRecoveryBannerPromoted`), plus
  `transcript_rows_backfill_integration_test.go`
  (`TestTranscriptRowBackfillDoesNotPreserveStartupStatusRows`,
  `TestFailedSessionStatusStillCreatesTranscriptRow`).
- Migration guard: `backend-go/internal/pgstore/migrations.go` migration `0127`
  replaces `tank_upsert_session_status_event` so loading/ready return before the
  transcript-row insert, deletes stale direct startup rows, and leaves failed
  startup banners promoted.
- Client mirror: `frontend/src/conversationProjection.ts` drops startup notices
  to match; `conversationProjection.test.ts` ("session-startup notices are turn
  noise, not main-transcript messages").
- Live: validated on a Glimmung slot — fresh codex session, first turn shows
  user → reply in chat with `Session is loading./ready.` in the Turns view; the
  active-turn placeholder sorts below the user message.

## Composer Compaction Count

Status: active

Intent:
Show, in the composer context indicator, how many times a session's context has
been compacted — a durable, monotonic per-session count rendered as a third
`cmp` metric beside the `ctx` used/window fraction and the `usd` cost. Occupancy
alone cannot convey this: the live `ctx` numerator self-resets after each
compaction (the next prompt is summary + recent turns), so a session that has
compacted ten times reads identically to one that never has. The compaction
count is the durable signal of how much earlier context has been summarized
away — how lossy the session's working memory has become — which a user
weighing "should I start a fresh session" needs and the occupancy gauge
structurally hides. This is the stat the originating session asked for after
observing the indicator climb past the window: the running number was cumulative
spend, not occupancy, and "how many compactions" was the missing durable fact.

Affected contracts:
- Transcript (owns the composer context indicator and its durable sources)
- Session Bar (the count rides the same durable session row as other indicators)
- Observability

Contract impact:
- The count is durable session metadata: a `sessions.compaction_count` column,
  maintained server-side as a projection over the append-only `session_events`
  ledger, carried on the same snapshot/SSE row payload as
  `runtime_context_window_tokens`. It is NOT derived from whatever transcript
  entries the browser has loaded — a client-side count would undercount once
  older `context.compacted` events scroll past the loaded window and disagree
  across reload / fresh tab, the exact local-vs-durable contradiction the
  Transcript and Session Bar contracts forbid.
- The projection is idempotent under at-least-once delivery: the chat-activity
  emitter recomputes `COUNT(*)` of `context.compacted` events on each such
  upsert and writes the row only when the total advances, so a redelivered
  event neither bumps `row_version` nor double-counts. The count is monotonic
  because the ledger is append-only.
- The bounded activity-summary fold (latest 50 lifecycle events) is
  deliberately NOT the source — it cannot see compactions older than its window.
  `context.compacted` stays out of `LifecycleChatEventTypes` (it must not move
  run status); the dedicated full-history count column is the source instead.
- The composer renders the metric in the chat-box cost chip, including the zero
  state before the first compaction. The pre-session splash composer renders
  `cmp 0` as an empty/stable value rather than introducing a new metric after a
  session starts; active sessions replace that placeholder with the durable
  session-row count. The per-turn pill does not carry it (compaction is a
  session-lifetime fact, not a turn fact). The chip widens via a
  compaction-metric modifier rather than squeezing the `ctx` fraction into an
  ellipsis.

Evidence:
- Schema: `backend-go/internal/pgstore/migrations.go` migration 0125
  (`compaction_count` column) and 0126 (partial index
  `session_events_context_compacted`).
- Store: `store.CountContextCompactions` +
  `backend-go/internal/pgstore/session_events_compaction_integration_test.go`
  (`TestCountContextCompactionsCountsScopedCompactionRows`) prove the count is
  compaction-only and session-scoped.
- Projection: `sessioncontroller.ChatActivityEmitter.refreshCompactionCount`;
  `chat_activity_test.go` (`TestEmitChatActivityDeltaRecordsAdvancingCompaction`,
  `TestEmitChatActivityDeltaDeduplicatesRedeliveredCompaction`,
  `TestDeriveActivitySummaryIgnoresContextCompacted`) prove the advance-only
  write, the at-least-once dedup, and that compaction does not move run status;
  `writer_test.go` pins the `EventTypeCompactionChanged` → `compaction_count`
  column mapping.
- Frontend: `frontend/src/turnCostEstimateUi.test.ts` and
  `frontend/src/composerCss.test.ts` prove the durable-sourced `cmp` metric, its
  session-only scope, and the fixed-footprint widening.
- Observability: `tank_session_compaction_total{provider,trigger}` counts each
  newly-observed compaction (the exact per-session total is the durable column).

## Transcript Refresh Shortcut (R)

Status: active

Intent:
Pressing R while the transcript region is focused force-pulls the durable
transcript tail (chat + Turns), recovering newest messages that a live SSE gap
failed to deliver without a full browser reload. Because a successful pull that
delivers no new rows is visually indistinguishable from "nothing happened," the
shortcut also flashes a brief, transient "Refreshed" confirmation in the same
title-overlay slot as the connection pill.

The confirmation is an intentionally lightweight debug affordance, not
permanent chrome — it is expected to be retireable (or demoted behind a debug
toggle) once the SSE-gap recovery it backstops is no longer a routine concern.
This ledger entry is the handle a future agent uses to retire it without
reconstructing intent from chat history.

Affected contracts:
- Transcript Navigation (owns the force-pull recovery the shortcut performs)
- App Chrome (owns the title-overlay slot the confirmation renders in)

Contract impact:
- The force-pull re-fetches the newest durable timeline window and reconciles
  it into rendered rows; it must not move the viewport for a reader in
  historical-anchor mode (Transcript Navigation: "Load, ready, reconnect, and
  resync must not reset the viewport").
- The confirmation pill is absolutely positioned with `pointer-events: none`
  and does not reflow the transcript or terminal, satisfying App Chrome's
  "remain visually stable / no layout jumps unrelated to the user's action."
- The pill is a momentary success cue, visually distinct (emerald) from the
  amber connection-state pill, and clears itself after a short window.

Evidence:
- Gate (pure, unit tested): `frontend/src/transcriptRefreshShortcut.ts` +
  `frontend/src/transcriptRefreshShortcut.test.ts` for the keypress decision;
  `frontend/src/transcriptRefreshIndicator.ts` +
  `frontend/src/transcriptRefreshIndicator.test.ts` for the confirmation's
  surface gate (chat + turns, visible pane only) and transient duration.
- Wiring: `frontend/src/App.tsx` performs the force-pull
  (`refreshSdkRunHistoryResult(..., "keyboard-refresh")`) and bubbles the
  transient label per-session into the same pill channel as the connection
  label.

## Live Turns Activity Reconciliation

Status: in progress

Intent:
Keep an already-open Turns detail synchronized with the durable server
projection while the per-session SSE stream is active. The live stream wakes the
browser; it does not become the child-row source of truth. When projected
transcript rows arrive for a turn whose activity detail is already cached, the
browser invalidates that cache and re-reads `/turns/{id}/activity`.

Affected contracts:
- Transcript

Contract impact:
- Turn activity detail remains a cached server projection over
  `session_events`, not a browser-local reducer fed by live shell rows.
- The browser only refreshes details the user has already loaded, so a busy live
  turn cannot fan out one request per unseen historical turn.
- Refresh failures are bounded: the SPA retries, emits
  `tank_session_event_client_events_total` labels for failure/give-up/recovery,
  and leaves a visible retry state in the Turns detail instead of silently
  showing stale rows.

Evidence:
- Shipped in this PR: `frontend/src/turnActivityCache.ts` and
  `frontend/src/turnActivityCache.test.ts` prove cached-vs-uncached invalidation
  and cursor coalescing.
- Shipped in this PR: `backend-go/cmd/tank-operator/handlers_client_metrics_session_events_test.go`
  proves the bounded telemetry labels are accepted and unknown labels clamp.
- Required before status becomes active: browser/test-slot evidence that a
  loaded Turns detail re-reads `/turns/{id}/activity` after a live
  `transcript_rows` update without using a full page refresh.

## AskUserQuestion Question Page (turn.awaiting_input)

Status: active

Intent:
When the in-pod agent invokes AskUserQuestion, the asking turn records a
durable `turn.awaiting_input.invocation` event and a derived
`assistant_message.created` question message. That assistant message is the
terminal main-transcript response for the asking turn. A separate normal
numbered question turn then records durable `turn.awaiting_input` carrying the
Tank-canonical questions. The turn-activity page projection records a compact
AskUserQuestion invocation marker on the preceding activity page, then opens
one semantic `question_set` page per question in the set. Those adjacent pages carry the same
`questionSet` number and individual `questionIndex`/`questionCount` metadata,
letting the Turns UI label the set and provide previous/next question shortcuts
without creating a third navigation system. If the agent asks immediately, that
first activity page is marker-only by design: it preserves the ledger handoff
without squeezing the question UI into activity history. The main transcript
renders the derived assistant question message so the user sees the agent's
question at the same conversation level as a normal final answer. That message
links to the question set in Turns. The
interactive answer form is owned by the Turns question page, which reflects
durable state rather than local React optimism, so a fresh tab renders the same
question set and defaults to it while the turn is still waiting for input.

Answering resumes the provider callback and starts the next visible turn:
- The user's selection posts to `POST /turns/{questionTurnId}/answer`, which
  persists durable `turn.input_answered` and publishes an `input_reply`
  control-plane command to the paused runner.
- The question turn is the active needs-input turn in Tank UI. The provider
  callback may still be parked under the asking turn inside the runner harness.

Each question page has two states:
- waiting — unanswered. The page surfaces one question from the set, with its
  options (single/multi-select), the free-form textarea when `allowFreeForm` is
  set, and one set-level Submit button that only enables after every question
  page in the set has a response.
- answered — a later `turn.input_answered` event references the question set
  (`awaitingInput.answered` is true), or the user just submitted (a local
  snapshot locks the page for the round-trip). The page renders locked with the
  user's picks.

Affected contracts:
- Transcript
- Transcript Navigation

Contract impact:
- The question page is a Turn activity projection of durable
  `turn.awaiting_input`; it is not a second ledger. The main transcript uses
  the derived `assistant_message.created` question message as the assistant
  handoff and navigation target to the question set, never a standalone
  question-button row.
- The preceding activity page receives a compact `AskUserQuestion` tool marker
  derived from the same durable `turn.awaiting_input` event. It is an audit
  marker for the invocation, not the answer surface and not a dependency on
  provider-specific raw tool rows.
- Turn activity pagination is semantic as well as size-bounded: each
  `turn.awaiting_input` event starts one `question_set` page per question while
  preserving one durable answer set. The pages expose shared set identity and
  per-question position so the page selector and question card can show
  "question 1 of N" and move to the adjacent question page. Answered/history
  state remains visible when revisiting any question page.
- A pending `needs_input` turn defaults to the first unanswered `question_set`
  page; normal turns still default to the latest activity page.
- `answered` is derived from a durable fact (a later `turn.input_answered` event
  whose `payload.question_timeline_id` matches), never a local "I submitted"
  flag, so historical replay matches live.
- The answer is a normal user message in the next durable turn, while
  `turn.input_answered` settles the question turn's card state.

Evidence:
- Backend: `backend-go/cmd/tank-operator/turn_pages_test.go` proves
  `turn.awaiting_input` creates the compact invocation marker page, starts a
  `question_set` page for each question, keeps a shared durable answer set, and
  seals an answered set before resumed activity.
- Backend API: `backend-go/cmd/tank-operator/handlers_session_events_test.go`
  proves an unanswered `needs_input` turn defaults to the question page.
- Frontend: `frontend/src/migrationPolicy.test.ts` proves the main transcript
  uses the assistant question message affordance while `RunAwaitingInputCard`
  is owned by Turns.
- Migration guard: `scripts/check-askuserquestion-migration.mjs` requires the
  semantic page path and same-turn `/answer` path.

## Provider Usage UI Retirement

Status: retired

Intent:
Token usage and cost remain backend plumbing and diagnostic math, not
transcript or Turns UI. The composer still renders a context-pressure indicator:
a `used/window` fraction sourced from durable `turn.usage` snapshots and the
provider-observed context window on the session row. The runners may observe
usage and context-window values at runtime (Codex app-server token usage; the
Claude Agent SDK per-turn `modelUsage.contextWindow`) and report the
provider-observed window on the runtime-config PUT; the orchestrator persists
that value on the session row for diagnostics, admin reporting, and the
composer denominator.

Affected contracts:
- Transcript
- Session Lifecycle (owns the runtime-config PUT and the session row)

Contract impact:
- The window denominator is sourced only from the durable session-row field
  `runtime_context_window_tokens`. There is no frontend `CONTEXT_WINDOW_BY_MODEL`
  model-window table and no `getContextWindow` lookup.
- The row value is first-observed-wins and durable, so diagnostics are stable
  across reload and identical in a fresh tab; a session with no reported window
  does not make the frontend guess a default.
- The composer context/cost indicator uses the pre-regression
  `run-cost-estimate` chip. It consumes provider-observed usage fields carried
  on projected transcript/Turn activity rows plus the durable session
  `runtime_context_window_tokens` denominator; it does not use a separate
  `context_usage` timeline field or `context-usage` SSE event.
- The transcript projection may synthesize a data-only `turn_usage` row and
  usage fields for the indicator, but the frontend must render `turn_usage`
  meta rows as `null`. The "Token usage updated" message is retired.
- Provider reports are observable through
  `tank_session_context_window_report_total{provider,source,result}` with
  bounded `source` and `result` labels.

Evidence:
- Backend: `backend-go/cmd/tank-operator/handlers_internal_test.go` covers the
  runtime-config PUT persisting the window and the `ok` / `ignored` counter
  outcomes;
  `backend-go/internal/sessions/manager_test.go` covers first-observed-wins
  persistence (`TestManagerSetRuntimeContextWindowPersistsAndPublishes`).
- Migration guard: `scripts/check-context-window-table-migration.mjs` fails if
  `CONTEXT_WINDOW_BY_MODEL` or `getContextWindow` reappear under `frontend/src`;
  wired into CI via `.github/workflows/removed-chat-runtime-guard.yml`.
- Frontend: `frontend/src/turnCostEstimateUi.test.ts` guards that the composer
  renders the restored `run-cost-estimate` context/cost chip while visible
  transcript usage messages stay retired.

## Durable transcript pipeline: persister dispatch, reconciler, async backend refresh

Status: shipped (2026-06-11, tank-operator#1051 PRs 1/2a)

Intent:
The session-bus persister is the sole bus-to-Postgres writer for transcript
events, and its health is the product: when it stalls, every session freezes
at once while runners keep working invisibly (the 2026-06-11 incident). Three
named behaviors keep that pipeline trustworthy:

- Per-session dispatch with batch coalescing: one durable consumer feeds
  per-session serial queues over a bounded worker pool; a flood session
  saturates one worker, and N queued events for one turn cost one projection
  pass (RefreshEventBatch).
- MAX_DELIVERIES advisory repair + startup reconciler: an event that exhausts
  redelivery is re-persisted out of band or counted as lost
  (tank_session_event_persist_exhausted_total); on boot the reconciler
  replays the ack-floor window and repairs holes idempotently (146 events
  recovered on first deploy).
- Async backend refresh: backend-direct writers (submit, interrupt, sweeps)
  persist the ledger row synchronously but run the transcript-row projection
  on a per-session async worker with refresh-then-wake ordering, unifying
  both write paths and keeping projection cost out of HTTP handlers.

Affected contracts:
- Transcript (session_events ownership; SSE as live follower of durable rows)
- Observability (persister lag gauges read JetStream consumer state so the
  signal survives the persister itself failing; TankSessionEventPersisterBacklog,
  TankSessionEventPersistExhausted, TankSessionEventStreamTruncated)

Retirement note:
The full-batch projection (projectTranscriptEvents) is the reference
implementation and the explicit resync path; the in-flight checkpointed-fold
stages (#1051 B2-B5) change its cost profile, never its ownership.
