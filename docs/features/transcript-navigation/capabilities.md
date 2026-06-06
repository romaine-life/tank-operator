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

## Auto-default to Turns view for substantial sessions

Status: shipped

Intent:
Opening a session from the sidebar normally lands in the main transcript. Once a
session has accumulated enough real user back-and-forths, that default flips to
the Turns view (latest turn): past a handful of exchanges the main transcript is
long enough that landing on the most recent turn beats landing on a transcript
you have to scroll, and the Turns view is the more direct read of "what happened
recently." This is the default-switch the session tab options menu (Main
transcript vs Turns view) made manual; the auto-default removes the need to flip
it by hand on every long session. It changes only where a *click* lands — it
never moves an already-open pane — and a manual choice from the tab menu always
wins.

Affected contracts:
- Transcript Navigation (primary — where a session open lands)
- Transcript (the durable per-session `user_message_count` row field)

Contract impact:
- The signal is a durable per-session count of `user_message.created` events
  (`sessions.user_message_count`), projected from the append-only
  `session_events` ledger by the chat-activity emitter — the same
  recompute-and-compare, advance-only projection model as `compaction_count`. It
  is NOT derived from the loaded transcript window (which would undercount once
  old events scroll out and disagree across reload / fresh tab — the
  local-vs-durable contradiction the Transcript and Transcript Navigation
  contracts forbid). It rides the snapshot and row-update wire alongside
  `compaction_count`.
- The count is "user back-and-forths," not SDK/turn churn: background-task wake
  continuations carry their prompt on `turn.submitted`, not
  `user_message.created`, so they never advance it. (Schedule-wakeups do write a
  `user_message.created` and so count; they are rare and are a genuine
  user-visible exchange.) Because the ledger is append-only the count is
  monotonic, so "default to Turns whenever count >= N" is equivalent to "flip
  once when it crosses N, forever after."
- The threshold is a single named constant (`AUTO_TURNS_USER_MESSAGE_THRESHOLD`,
  currently 8) owned by the pure gate `frontend/src/autoTurnsDefault.ts`. It is an
  ergonomics dial, not a failure guard — there is deliberately no attempt to find
  a "magic number."
- The open-target preference (`sessionOpenTargets`, written only by the session
  tab options menu) is the manual override and always wins; the auto-default only
  decides the landing when the user has not chosen. Both are per-tab session
  state today, consistent with the existing manual toggle; the durable signal is
  the row count, not the preference.

Evidence:
- Backend: migrations `0135` (`sessions.user_message_count` column) and `0136`
  (`session_events_user_message_by_session` partial index);
  `store.CountUserMessages` + `session_events_compaction_integration_test.go`
  (`TestCountUserMessagesCountsScopedUserMessageRows`); `chat_activity.go`
  `refreshUserMessageCount` + `chat_activity_test.go` (advancing-writes /
  redelivered-no-op); `writer.go` `EventTypeUserMessageCountChanged` →
  `user_message_count` mapping + `writer_test.go`; the field carried on the row by
  `sessionmodel`, `sessions` Info, `row_publisher`, and both `sessionregistry`
  reads.
- Frontend: `frontend/src/autoTurnsDefault.ts` + `autoTurnsDefault.test.ts` pin
  the threshold and the gate; `sessionStore.ts` carries `user_message_count` on
  the wire; `App.tsx` `sessionOpenTarget()` applies the gate with the manual
  override winning.
