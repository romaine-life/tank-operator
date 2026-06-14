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
