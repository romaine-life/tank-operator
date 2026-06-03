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

## AskUserQuestion Handoff Row

Status: active

Intent:
Promote a pending AskUserQuestion into the settled main transcript as a
"Claude is waiting on you" handoff row, so the user sees — in the conversation
surface they actually read — that the agent paused for input, with a one-click
path to the question card in Turn activity. The row reflects the durable state
of the handoff, not local React optimism, so a fresh tab renders the same
thing.

The row has three states:
- waiting — unanswered and the owning turn is still live. The only state that
  uses the attention-grabbing active accent and the high-emphasis "Open in
  Turns" CTA.
- answered — the user submitted an answer (durable `tool.approval_resolved`).
  Muted "Answered" with a secondary "View in Turns".
- settled — unanswered, but the owning turn reached a terminal state (the user
  stopped it, or it failed). Nothing is being waited on, so the row renders
  muted as "No longer waiting" with a secondary "View in Turns". The function
  is unchanged — the user can still open the question in Turns; only the visual
  demand drops.

Affected contracts:
- Transcript
- Transcript Navigation

Contract impact:
- The row is a promotion-only projection of the durable AskUserQuestion item;
  it is not a second ledger and does not relocate a rendered row between the
  activity/log and settled surfaces.
- answered/settled state is derived from durable facts (`announcement.answered`
  from `tool.approval_resolved`, and the owning turn's terminal status), never
  a local "I submitted / I abandoned" flag, so historical replay matches live.
- The settled state must not keep the active needs-input accent: an interrupted
  or failed turn clears the session-level needs-input signal, so the handoff
  row must visually agree that nothing is pending.

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
(Codex app-server token usage; the Anthropic Models API `max_input_tokens`) and
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
