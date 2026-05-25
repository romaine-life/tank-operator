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
