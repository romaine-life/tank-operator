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

## Self-healing turn-activity loading

Status: shipped

Intent:
The per-turn activity body (the `/activity` load behind "Loading activity…") must
render every time a turn is selected, including when revisiting a session tab.
The tabs view keeps panes mounted-but-hidden; a hidden→visible reactivation runs
`resetSdkTimelineBootstrapState("visible-reactivation", …)`, which used to wipe
the per-turn load map (`turnActivityLoadsByTurn`). The activity-load trigger is
edge-triggered on `[activeTab, effectiveSelectedTurnId]` — neither changes when
you revisit the tab you were already on — so the selected turn dropped to
`unloaded` and the body stranded on "Loading activity…" with no edge to re-fire,
recoverable only by remount (reload / nav-away-and-back / pressing R, which
force-loads). This is the same edge-triggered-without-reconciliation class as the
turn-directory strand above, in the activity loader.

Contract impact:
- The durable, turn-keyed activity cache is preserved across same-session
  reactivation (only a real session change invalidates it — different turns), so
  revisiting a tab renders the loaded body instantly instead of clearing and
  re-fetching. `resetSdkTimelineBootstrapState` takes `clearTurnActivityCache`
  (default true; the reactivation caller passes false); the derived memos
  (`activityEntriesByTurn` / `turnActivityPageInfo` / `loadingActivityTurns`)
  recompute from the preserved map.
- A level-triggered reconciler keyed on the selected turn's load *status*
  re-drives a load whenever it is absent/`unloaded` while a turn is selected on
  the Turns tab — so any reset that empties the map self-heals rather than
  stranding. `error` is excluded (its own retry affordance; no hot-loop).

Evidence:
- Frontend: `turnActivityState.turnActivityShouldReconcileLoad` (pure gate) +
  `turnActivityState.test.ts` (reconcile re-drives `unloaded`; leaves
  loading/loaded/error alone; off-tab/no-selection no-ops), the reconcile effect
  + `clearTurnActivityCache` gating in `App.tsx`. Recovery via R / reload / nav
  is preserved (the edge-triggered effect is kept alongside the reconciler).

## Self-healing turn-directory loading

Status: shipped

Intent:
Clicking a session in the tabs view must always load its turns. The first
turn-directory loader was edge-triggered behind a permanent single-flight
boolean latch (blocked by name in `scripts/check-removed-chat-runtime.mjs`) with
no abort, timeout, or reconciliation. In the tabs view a pane persists across
session switches, so
switching to a new session while the previous session's load was still in flight
took the latch's early return — the new load never started — and when the stale
load resolved it returned without a terminal status. The Turns view was left on
"Loading turns…" with nothing in flight and no edge to re-fire, recoverable only
by remount (reload, or nav-away-and-back). Both `idle` and `loading` rendered the
same spinner with no exit, Retry was wired only to the `error` state, and a load
that never started emitted no telemetry — so the most common failure was both
unrecoverable without remount and invisible. Loading is now a level-triggered
reconciler keyed to the session epoch, so the strand is structurally
unrepresentable and observable when it is auto-healed.

Affected contracts:
- Transcript Navigation (primary — Live Behavior + Failure And Recovery +
  Observability for directory loading)
- Observability (the new bounded client events and the stuck alert)

Contract impact:
- A visible, loadable pane whose directory status is non-terminal
  (`idle`/`loading`) with no load in flight for the current session always has a
  load running. Recovery is never remount-only.
- The in-flight load is keyed by session epoch via an `AbortController`, not a
  boolean latch: switching sessions aborts the superseded load, a stale
  completion is ignored by controller identity, and the current load owns the
  terminal status. A load that never started cannot strand the spinner.
- A load is bounded by a wall-clock timeout (`TURN_DIRECTORY_LOAD_TIMEOUT_MS`); a
  wedged connection becomes the retryable `error` state, never an eternal
  spinner. The Retry affordance is reachable from the loading state, not only
  from `error`.
- The "never started" / stuck failure is observable: `turn-directory-stuck` (the
  spinner exceeded `TURN_DIRECTORY_STUCK_THRESHOLD_MS` — the user-trust signal),
  `turn-directory-reconcile` (the reconciler auto-healed a vanished load), and
  `turn-directory-timeout` (a load abandoned into the retryable error state). The
  `TankTurnDirectoryStuck` alert fires on a sustained stuck rate.

Evidence:
- Frontend: `frontend/src/turnDirectoryLoad.ts` (pure, unit-tested reconcile
  gate: `evaluateTurnDirectoryReconcile` / `evaluateStuckWatchdog` /
  `shouldArmStuckWatchdog` + thresholds), `turnDirectoryLoad.test.ts` (the truth
  table pinning that a visible non-terminal pane with nothing in flight always
  loads), and the `App.tsx` reconcile + watchdog + abort wiring.
- Backend: `turn-directory-{reconcile,stuck,timeout}` added to both client-event
  allowlists in `handlers_client_metrics.go`, asserted in
  `handlers_client_metrics_test.go` to ride `tank_chat_scroll_client_events_total`.
- Observability + guard: the `TankTurnDirectoryStuck` PrometheusRule in
  `k8s/templates/observability.yaml`, and the migration guard in
  `scripts/check-removed-chat-runtime.mjs` blocking the retired single-flight
  latch by name.

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
