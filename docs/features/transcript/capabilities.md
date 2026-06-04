# Transcript Capabilities

This ledger names user-facing behavior under the Transcript feature area. It is
not a backlog; entries exist when behavior needs a stable handle for planning,
review, tests, incident follow-up, or retirement.

## Public Message Links

Status: active

Intent:
Copying a transcript message link mints a durable opaque bearer share token so
the URL can open for unauthenticated viewers without exposing transcripts by
guessable `session` and `message` query parameters alone. The public view is a
read-only transcript surface: no session sidebar, no composer, no Files,
Settings, Background, or mutable controls. The Turns detail view remains
available because it is part of understanding the transcript.

Affected contracts:
- Transcript
- Transcript Navigation
- Auth And Streams
- App Chrome

Contract impact:
- Public reads are explicitly bearer-token gated through
  `/api/public/message-links/{token}` and
  `/api/public/message-links/{token}/timeline`; unauthenticated access to the
  authenticated session API remains unsupported.
- The copied link still targets durable transcript-row identities and can page
  the same server-owned transcript row model as authenticated timeline reads.
- The public SPA route renders a distinct full-screen workspace without the
  authenticated app sidebar or composer, preserving App Chrome's ownership of
  signed-in navigation.

Evidence:
- Backend: `backend-go/cmd/tank-operator/handlers_message_link_share_test.go`
  covers share creation and unauthenticated public timeline reads.
- Frontend: `frontend/src/migrationPolicy.test.ts` pins the public message-link
  shell and public API path wiring.

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
  `turnPageEventLimit` (1000) events. The endpoint (`server_turn_activity_v2`)
  returns the page directory and defaults to the last page; `?page=N` selects
  another. Page boundaries are durable `order_key` ranges, so a selected page is
  stable across reload and deep links.
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
  proves the endpoint reports a completed shell, `page_count >= 2`, and defaults
  to the last page.
- Observability: `tank_transcript_materialization_invariant_violation_total{invariant="active_shell_after_terminal"}`
  + `TankTurnActiveWithDurableTerminal` guard the regression; `tank_turn_activity_event_count`
  / `tank_turn_activity_page_count` track long-turn frequency.
- Frontend gate: `frontend/src/turnActivityPager.ts` +
  `frontend/src/turnActivityPager.test.ts` prove the control stays visible
  (disabled, "Page 1 of 1") at a single page and expose the clamped current page
  + total count for the picker — the regression guard against a threshold-hidden
  control. The Page dropdown is rendered by the Turns view
  (`RunTurnActivityScreen`) in `frontend/src/App.tsx`.

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
- Producer: `agent-runner/src/adapters/claude.ts` maps
  `system/compact_boundary`; `agent-runner/src/adapters/claude.test.ts` pins
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
- Observability: `agent-runner/src/metrics.ts` →
  `tank_runner_unmapped_provider_event_total`.

Codex-specific notes:
- The installed Codex App Server protocol (`@openai/codex@0.130.0`,
  generated via `codex app-server generate-ts`) exposes
  `thread/compacted { threadId, turnId }`, marked deprecated in favor of a
  `contextCompaction` item type. Tank maps both surfaces to the same durable
  notice and records one row per provider turn. Codex does not expose reliable
  manual/auto trigger or pre-token metadata on these surfaces, so the runner
  defaults `payload.trigger` to `auto`.

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
- The composer renders the metric only at session scope and only when > 0; the
  per-turn pill does not carry it (compaction is a session-lifetime fact, not a
  turn fact). The chip widens via a `has-compactions` modifier rather than
  squeezing the `ctx` fraction into an ellipsis.

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
When the in-pod agent invokes AskUserQuestion, the active turn pauses with a
durable `turn.awaiting_input` event carrying the Tank-canonical questions. The
turn-activity page projection records a compact AskUserQuestion invocation
marker on the preceding activity page, then opens one semantic `question_set`
page per question in the set. Those adjacent pages carry the same
`questionSet` number and individual `questionIndex`/`questionCount` metadata,
letting the Turns UI label the set and provide previous/next question shortcuts
without creating a third navigation system. If the agent asks immediately, that
first activity page is marker-only by design: it preserves the ledger handoff
without squeezing the question UI into activity history. The main transcript
renders a restored AskUserQuestion handoff button (`RunNeedsInputAnnouncement`,
originally removed by PR #861) from the durable `awaiting_input` meta row so the
user can reach the question set from the conversation. The interactive answer
form is owned by the Turns question page, which reflects durable state rather
than local React optimism, so a fresh tab renders the same question set and
defaults to it while the turn is still waiting for input.

Answering resumes the same turn:
- The user's selection posts to `POST /turns/{askingTurnId}/answer`, which
  persists durable `turn.input_answered` and publishes an `input_reply`
  control-plane command to the paused runner.
- The asking turn remains active while awaiting input; Stop can still interrupt
  that turn because `activeTurnId` is preserved.

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
  `turn.awaiting_input`; it is not a second ledger. The same durable
  `awaiting_input` meta row appears in the main transcript as the navigation
  button to the question set, never as a standalone authored message or
  synthetic turn.
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
- The answer is part of the same durable turn — copy links, unread counts, and
  latest-message state do not point at a synthetic user-message turn.

Evidence:
- Backend: `backend-go/cmd/tank-operator/turn_pages_test.go` proves
  `turn.awaiting_input` creates the compact invocation marker page, starts a
  `question_set` page for each question, keeps a shared durable answer set, and
  seals an answered set before resumed activity.
- Backend API: `backend-go/cmd/tank-operator/handlers_session_events_test.go`
  proves an unanswered `needs_input` turn defaults to the question page.
- Frontend: `frontend/src/migrationPolicy.test.ts` proves transcript renderers
  use the restored `RunNeedsInputAnnouncement` button while
  `RunAwaitingInputCard` is owned by `RunTurnActivityScreen`.
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
