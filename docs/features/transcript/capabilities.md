# Transcript Capabilities

This ledger names user-facing behavior under the Transcript feature area. It is
not a backlog; entries exist when behavior needs a stable handle for planning,
review, tests, incident follow-up, or retirement.

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
