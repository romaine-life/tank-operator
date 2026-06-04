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
  transcript — satisfying the Transcript contract's no-bounce invariant. This is
  the same Turn-activity placement as AskUserQuestion (both are turn noise). The
  frontend renders compaction through the existing `RunMetaBlock` primitive in
  the Turn-activity disclosure. An earlier implementation promoted it into the
  settled transcript and excluded it from the activity compact, which made it
  flash-then-vanish on the per-turn detail screen; that promotion path was
  deleted.
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

## AskUserQuestion Card (turn.awaiting_input)

Status: active

Intent:
When the in-pod agent invokes AskUserQuestion, the active turn pauses with a
durable `turn.awaiting_input` event carrying the Tank-canonical questions. The
transcript projection places an interactive question card
(`metaKind: "awaiting_input"`) inside Turn activity, while the main transcript
shows the Turn activity shell. The card reflects durable state, not local React
optimism, so a fresh tab renders the same thing.

Answering resumes the same turn:
- The user's selection posts to `POST /turns/{askingTurnId}/answer`, which
  persists durable `turn.input_answered` and publishes an `input_reply`
  control-plane command to the paused runner.
- The asking turn remains active while awaiting input; Stop can still interrupt
  that turn because `activeTurnId` is preserved.

The card has two states:
- waiting — unanswered. The card surfaces the options (single/multi-select), the
  always-on free-form textarea when `allowFreeForm` is set, and a Submit button.
- answered — a later `turn.input_answered` event references the question
  (`awaitingInput.answered` is true), or the user just submitted (a local
  snapshot locks the card for the round-trip). The card renders locked with the
  user's picks.

Affected contracts:
- Transcript
- Transcript Navigation

Contract impact:
- The card is a Turn activity projection of durable `turn.awaiting_input`; it is
  not a second ledger and it does not appear as a standalone main-transcript
  message.
- `answered` is derived from a durable fact (a later `turn.input_answered` event
  whose `payload.question_timeline_id` matches), never a local "I submitted"
  flag, so historical replay matches live.
- The answer is part of the same durable turn — copy links, unread counts, and
  latest-message state do not point at a synthetic user-message turn.

Evidence:
- `frontend/src/needsInputAnnouncement.ts` is the single state machine shared
  by the live reducer projection and the server-projected (fresh-tab) path;
  `frontend/src/needsInputAnnouncement.test.ts` covers all three states,
  including that an answer wins over a later interrupt.
- `frontend/src/conversationProjection.test.ts` and
  `backend-go/cmd/tank-operator/transcript_projection_test.go` both prove an
  interrupted, unanswered AskUserQuestion announcement carries
  `turnTerminalStatus`, the fact the renderer uses to settle the row.

## Provider-Observed Context Window Fraction

Status: active

Intent:
The composer context indicator is a `used/window` fraction whose window is the
real, provider-observed context size for the running model — not a number the
frontend guesses from a model id. The runners observe the window at runtime
(Codex app-server token usage; the Claude Agent SDK per-turn `modelUsage.contextWindow`) and
report it on the runtime-config PUT; the orchestrator persists it on the session
row and the composer renders the fraction against it. Pre-session previews and
not-yet-reported sessions show a placeholder used count instead of a fraction.

Affected contracts:
- Transcript
- Session Lifecycle (owns the runtime-config PUT and the session row)

Contract impact:
- The window denominator is sourced only from the durable session-row field
  `runtime_context_window_tokens`. There is no frontend `CONTEXT_WINDOW_BY_MODEL`
  model-window table and no `getContextWindow` lookup, and the indicator is a
  fraction, not a percent ring.
- The row value is first-observed-wins and durable, so the fraction is stable
  across reload and identical in a fresh tab; a session with no reported window
  renders the placeholder, never a frontend-assumed default.
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
  `CONTEXT_WINDOW_BY_MODEL` or `getContextWindow` reappear under `frontend/src`
  and asserts the composer reads `runtime_context_window_tokens`; wired into CI
  via `.github/workflows/removed-chat-runtime-guard.yml`.
- Frontend: the composer reads `session.runtime_context_window_tokens`
  (`frontend/src/App.tsx`) as the fraction denominator, with a placeholder when
  it is 0.
- Required before status active: unit coverage that the projection emits the
  `awaiting_input` card in Turn activity and marks it answered from a later
  `turn.input_answered` event, plus the live pre-deploy-pod gate (Claude
  AskUserQuestion -> answer -> the same turn reaches `turn.completed`, not
  `turn.failed`).
