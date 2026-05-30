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
