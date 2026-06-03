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
  `tank_turn_number_resolve_total` / `tank_turn_number_missing_total` +
  `TankTurnNumberMissing` alert.
- Frontend (turn-number route cutover PR): `appRoutes` number param + tests
  (numeric round-trip + non-numeric → unavailable), `App.tsx` cold-load
  server-resolve + durable label + unavailable-target render, transcript
  projection version bump (re-stamps existing shells with `turnNumber`), and
  `scripts/check-removed-chat-runtime.mjs` guards against the retired
  `turn_<uuid>` route and the array-position label.
