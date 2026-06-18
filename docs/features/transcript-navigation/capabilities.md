# Transcript Navigation Capabilities

This ledger names user-facing behavior under transcript navigation. It is not a
backlog. Add entries only when the behavior needs a stable handle for planning,
review, tests, incident follow-up, or retirement.

## Durable per-session turn numbers

Status: shipped

Intent:
Turn deep links use a stable, human-facing per-session number
(`/sessions/{id}/turns/3`) instead of the provider-neutral `turn_<uuid>`, the
same way sessions are numbered (`/sessions/475`). The number is durable and
submission-ordered, so a shared or bookmarked turn link resolves to the same
turn after reload and across clients.

Affected contracts:
- Transcript Navigation (primary)
- Transcript (turn_activity shells carry turnNumber)
- Observability (resolve / missing-number counters + TankTurnNumberMissing alert)

Contract impact:
- `turn_id` stays the provider-neutral protocol identity that events, timelines,
  idempotency, and the activity/interrupt/answer APIs key on. `turn_number`
  is a durable public handle over it (`session_turns`), assigned exactly once by
  the `tank_session_events_allocate_turn_number` trigger on a turn's first
  durable `session_events` insert and resolved server-side via
  `GET /api/sessions/{id}/turns/{n}`. The browser never maps a number to a
  turn_id from render state; the durable row, not the loaded window, is the
  resolver.
- A missing or non-numeric target renders an explicit unavailable-target state,
  never a silent fallback to the latest turn.

Evidence:
- Backend (durable turn-number PR, merged): migrations `0087`-`0094`
  (`session_turns` + `session_turn_counters` + the idempotent allocation trigger
  + order-preserving backfill), `SessionTurnStore` + `GET /turns/{n}` resolver,
  transcript projection stamping, pgstore integration tests (allocation
  idempotency + backfill ordering), resolver handler tests (200/404/400),
  `tank_turn_number_resolve_total` / `tank_turn_number_missing_total`
  (`phase="materialize"` and `phase="submit_response"`) +
  `TankTurnNumberMissing` alert.
- Frontend (turn-number route cutover PR): `appRoutes` number param + tests
  (numeric round-trip + non-numeric → unavailable), `App.tsx` cold-load
  server-resolve + durable label + unavailable-target render, transcript
  projection version bump (re-stamps existing shells with `turnNumber`), and
  `scripts/check-removed-chat-runtime.mjs` guards against the retired
  `turn_<uuid>` route and the array-position label.

## Durable turn directory

Status: shipped

Intent:
The Turns selector lists every turn in a session — Turn 1..N — regardless of how
far back the chat transcript window has paged. Before this, the selector was
built from the bounded `/timeline` tail (~24 rows), so a session longer than the
window showed a truncated turn list starting mid-session and the earliest turns
were unreachable from the Turns view (the user could only reach them by paging
the Chat tab back). The selectable turn set is now owned by the durable ledger,
not by whatever transcript window the browser happens to have loaded.

Affected contracts:
- Transcript Navigation (primary — the selectable turn set)
- Transcript (the directory returns `turn_activity` shells, the same projection
  rows `/timeline` windows over)
- Observability (directory list/size counters)

Contract impact:
- `GET /api/sessions/{id}/turns/directory` (and the public-share / admin-hidden
  mirrors) returns the COMPLETE, submission-ordered set of `turn_activity`
  shells, each stamped with its `session_turns.turn_number`. The browser builds
  the Turns selector from this directory, never from `renderedEntries`; the live
  window only overlays fresh status onto turns the directory already lists.
- A turn is listable iff the directory lists it. A failed directory load shows a
  retryable Turns error, never a silent fall-back to the loaded window.
- Background-wake turns (`turn_bgtask-*`) carry no number and no own shell, so
  the directory excludes them by construction (matching the projection's fold).
- The directory is bounded by `TurnDirectoryMaxRows`; overflow keeps the newest
  turns and sets `truncated`, surfaced in the UI and counted — never a silent cap.

Evidence:
- Backend: migration `0164` (partial index `session_transcript_rows_turn_directory`),
  `SessionTranscriptRowStore.ListTurnDirectory`, `GET /turns/directory` on owner
  / public-share / admin-hidden surfaces, `tank_turn_directory_list_total` +
  `tank_turn_directory_size`, store integration tests (ordering, bgtask/non-shell
  exclusion, cap-keeps-newest) and handler tests (complete set, empty,
  truncation, auth, route precedence over `/turns/{number}`).
- Frontend: `turnDirectoryEntries` load via `turnDirectoryRequestPathForPane`,
  `turnViewItems` sourced from `mergeTurnDirectoryWithLiveShells(directory,
  window)` instead of the window-derived builder call, new-turn refetch, Turns
  loading/error/truncation states, `turn-directory-*` client telemetry,
  `turnDirectory.test.ts`, and the `scripts/check-removed-chat-runtime.mjs` guard
  blocking the window-derived selector.

## Deep-linkable turn activity pages

Status: active

Intent:
A turn's activity page is a first-class, shareable route coordinate:
`/sessions/{id}/turns/{n}/pages/{p}`. Opening a turn canonicalizes to its
resolved page, and paging updates the URL, so a copied link reproduces the exact
page the reader saw.

Affected contracts:
- Transcript Navigation (primary)
- App Chrome (the breadcrumb renders the page coordinate)

Contract impact:
- The page ordinal parses with the same discipline as turn numbers (positive
  integer). A malformed page within a valid turn recovers to the turn's default
  page — the turn resolves, only the page sub-coordinate was bad; this is a
  deliberate graceful recovery, not the silent fallback-to-latest that bad turn
  segments are barred from.
- Opening `/turns/{n}` resolves the server-default page and canonicalizes via
  `replaceState` to `/turns/{n}/pages/{N}`; the URL then tracks paging, sourced
  from the activity page directory (no auto-follow of the live tail; if a live
  turn seals a new page the reader stays on the sealed page and switches via the
  always-present Page dropdown — announcing the seal was deliberately descoped as
  an uncommon case not worth a durable event).
- Question-set pages keep the ordinal form; their question semantics stay
  label-only, not a separate route axis.

Evidence:
- `frontend/src/appRoutes.ts` page segment + `parsePageNumber` with
  `appRoutes.test.ts`; `App.tsx` route-driven page selection + canonicalization
  (`replaceState`, no spurious history); `breadcrumb.ts` / `breadcrumb.test.ts`
  for the page crumb.

## Turns view is the primary session view

Status: shipped

Intent:
Opening a session from the sidebar lands in the Turns view. New sessions with no
turn activity render the Turns empty state, and sessions with activity select the
latest turn. The main transcript remains available as a deliberate fallback from
Session Data at `/sessions/{id}/transcript`, but it is no longer the default
session root or a sidebar-level open-target choice.

Affected contracts:
- Transcript Navigation (primary — where a session open lands)
- Transcript (main transcript remains a durable artifact/fallback)

Contract impact:
- `/sessions/{id}` is the Turns route. A numbered turn still uses
  `/sessions/{id}/turns/{n}`.
- `/sessions/{id}/transcript` is the explicit main-transcript route.
- Sidebar row clicks and new-session activation request the Turns view, not the
  transcript.
- The sidebar session menu no longer offers Main transcript vs Turns view; it
  only carries session-level actions such as Close session.
- Session Data includes a Main transcript row with an Open transcript action.
- The prior `sessions.user_message_count` and `sessions.open_target` row fields
  remain on the wire for compatibility/diagnostics, but the frontend no longer
  uses them to choose the session landing surface.

Evidence:
- Frontend: `appRoutes.ts` maps root session routes to Turns and
  `/transcript` to `chat`; `App.tsx` sidebar open requests use
  `requestSessionTurnsOpen`; `RunTurnActivityScreen` renders its empty state for
  new sessions; `SessionDataScreen` exposes the transcript action.
- Tests: `appRoutes.test.ts`, `sessionSidebarNavigation.test.ts`,
  `sessionDataStatus.test.ts`, and `migrationPolicy.test.ts`.

## Compact transcript pager

Status: active

Intent:
On a phone-width viewport the desktop turn/page navigation (the 7-control stepper
plus the combined turn/page `Select`) is too dense, so it collapses into a single
always-present position button showing "Turn N · Page P". Tapping it opens the
identical controls in a bottom sheet, so a reader on a compact screen keeps the
same turn and page navigation a desktop reader has.

Affected contracts:
- Transcript Navigation (primary — the never-hidden page/turn affordance)
- App Chrome (the run-pane chrome reflows on the compact shell)

Contract impact:
- This is the sanctioned compact form of the never-hidden Page/turn affordance:
  on `useViewport().isCompact` (<= BP_COMPACT) `App.tsx` `RunTurnViewControls`
  renders one `.run-turn-pager-compact-trigger` button that opens a bottom `Sheet`
  (`side="bottom"`, `components/ui/sheet.tsx`) carrying the same controls fragment
  and reusing `selectTurnAndPage`. The button is never omitted while a turn is
  selected and renders disabled ("No turns") when there is none; navigating closes
  the sheet. Desktop rendering is unchanged.

Evidence:
- `frontend/src/mobileShell.test.ts` pins the compact pager branch ("turn/page
  pager collapses to a single sheet-backed button on compact"), the bottom-anchored
  `Sheet` variant ("sheet primitive supports a bottom-anchored variant"), and the
  disabled empty state ("compact pager stays present with no turns instead of
  vanishing").

## Strand-proof selected-turn activity load

Status: shipped

Intent:
Selecting a turn in the Turns view always loads its activity body. Before this,
the "Loading activity…" spinner could stay up forever: the selected turn id was
set from several paths (explicit gestures, the live-run follow, the deep-link
route match, the route-number resolver, and the default-to-latest fallback) but
only some of them also *started* a load. Landing on a turn via a deep link, via
the route resolver, or via the default-latest selection set the selection and
left the per-turn activity load `unloaded`/absent with nothing in flight — a
visible spinner with no in-flight request and no edge left to re-fire. Only a
remount or an explicit click recovered it. This was the user-reported "dead
refresh."

Affected contracts:
- Transcript Navigation (primary — a selected turn's body always loads)
- Observability (the stuck-spinner / route-session-mismatch client telemetry)

Contract impact:
- Whether a load runs for the selected turn is decided by a single, pure,
  level-triggered gate (`frontend/src/turnActivityLoadReconcile.ts`,
  `evaluateTurnActivityReconcile`): a visible Turns pane with a selected,
  non-terminal, not-loading turn ALWAYS resolves to "load". No selection path —
  present or future — can leave the body stranded, because the reconcile keys on
  the selection + its load state, not on how the selection happened. This mirrors
  the directory-load reconcile design and is the same shape as the pure
  `turnActivityPager` / `turnActivityState` gates: the truth-table test is the
  regression guard.
- Terminal states stay terminal: `loaded` is the desired state and `error` is
  left for Retry / re-selection to re-drive — the gate never auto-retries a
  failing endpoint in a hot loop. A genuinely hung load self-heals to `error`
  via the existing per-load `AbortController` timeout, then Retry.
- Hidden panes do not eagerly load (the tabs view keeps non-routed session panes
  mounted); they reconcile when they next become the visible Turns surface. The
  stranded / route-session-mismatch client telemetry is gated on the same
  visibility, so a hidden background pane reading the live URL no longer reports
  a strand or a route mismatch that no user can see.

Evidence:
- `frontend/src/turnActivityLoadReconcile.ts` (pure gate) +
  `turnActivityLoadReconcile.test.ts` (truth table, including the
  strand-without-recovery regression guard); `App.tsx` ChatPane reconcile effect
  keyed on `(visible, activeTab, effectiveSelectedTurnId, selected load status)`
  and `visible`-gated `RunTurnActivityScreen` stuck/mismatch telemetry.
- Diagnosis: production `tank_session_event_client_events_total`
  (`turn_activity_load_started`≈`_succeeded`, no failed/timed-out tail;
  `turn_activity_stuck_loading`=0) and `tank_chat_scroll_client_events_total`
  (`turn-activity-selected-loading-stranded` present, reason=absent;
  `turn-activity-selected-loading-slow`=0), plus the firing
  `TankTurnActivitySelectedLoadingStranded` /
  `TankTurnActivitySelectedRouteSessionMismatch` alerts — all of which name a
  load that never started, never a slow load.
