# Transcript Navigation Contract

This contract applies to where the user lands in a transcript, how transcript
position is preserved, how historical pages load, how copied message links
resolve, and how live-tail behavior interacts with a reader who is not at the
tail.

## Product Model

Transcript navigation is orientation over a durable conversation ledger. It is
not message ownership; the [Transcript](../transcript/contract.md) contract
owns which messages exist and how they are delivered. This feature owns whether
the user remains oriented while those messages render.

Normal session navigation lands at the live tail. Historical position is
explicit user intent only: copied message links, manual back-pagination, and
deliberate history navigation. New live messages should be noticeable without
yanking the viewport away from a user reading history.

## Sources Of Truth

- `session_events.order_key` owns durable live-stream position.
- `session_transcript_rows.row_cursor` owns `/timeline` historical transcript
  position.
- Server timeline pages own bounded windows of top-level transcript rows. Raw
  events inside a collapsed Turn activity row are loaded only through the
  explicit Turn activity endpoint.
- Copied message links may name rendered timeline IDs, but the server must
  translate them to durable cursors.
- Durable read state owns unread/new indicators when the indicator affects
  session or transcript state.
- Browser scroll offsets are layout state only; they are not transcript
  position source of truth.

## Migration Rules

- Do not persist or restore browser-local scroll position as normal session
  navigation state.
- Do not make the browser DOM the resolver for deep links or copied message
  anchors.
- Do not keep hidden compatibility paths that infer historical position from
  old local storage keys, previous session selection, or transient viewport
  offsets.
- Do not put non-transcript UI such as "continuing previous conversation" or
  "beginning of conversation" into the scroll flow unless it is part of the
  durable transcript model.
- Delete tests that pin old scroll-restoration behavior when the intended
  behavior is live-tail navigation or durable cursor anchoring.

## Live Behavior

- Opening a session normally lands at the live tail.
- Opening a copied message link lands on a bounded page around that durable
  message cursor.
- Manual back-pagination prepends older messages while preserving the user's
  visual anchor.
- A manual back-pagination action should either add visible projected rows,
  reach the durable oldest edge, or emit telemetry that zero new visible rows
  were returned.
- Manual forward-pagination appends newer historical messages without jumping
  the anchor unexpectedly.
- New live messages append at the tail without moving the viewport when the
  user is reading history.
- Returning to the live tail is an explicit state transition.
- Load, ready, reconnect, and resync must not reset the viewport unless the
  user has explicitly returned to live tail or the current cursor is invalid.

## Failure And Recovery

- Browser reload reconstructs the intended navigation state from durable
  cursor inputs, not from DOM position.
- Valid durable cursors resume to an equivalent bounded transcript window.
- Unknown or expired cursors trigger explicit resync or a clear fallback to the
  live tail; they must not silently show a misleading historical position.
- Reconnect and visibility changes continue from the current navigation mode:
  live-tail mode follows the tail, while historical mode preserves the anchor
  and surfaces new activity separately.
- If a target message was deleted or is outside the durability boundary, the UI
  should show a clear unavailable-target state.

## Observability

- There must be a way to distinguish live-tail mode from historical-anchor mode
  in client telemetry when diagnosing jumps or missed messages.
- Timeline page requests should log or count anchor type, direction, and
  cursor validity without logging message contents.
- Resync, invalid cursor, anchor-not-found, and unexpected viewport-reset
  cases should be observable as user-trust navigation failures.
- A report that "refresh moved me" or "new messages moved the transcript"
  should be diagnosable from durable cursor inputs plus client navigation
  telemetry.

## Acceptance Checks

- Normal session open lands at the live tail.
- A copied message link resolves through a durable cursor and lands on the
  target message after reload.
- Back-pagination preserves the visible anchor while older messages are
  prepended.
- Live messages received while reading history do not move the viewport to the
  tail.
- Returning to live tail resumes live-follow behavior and clears the separate
  new-message affordance.
- Load, ready, reconnect, and resync do not introduce scroll jumps in either
  live-tail mode or historical-anchor mode.
- Legacy browser-local scroll restoration cannot reappear without failing a
  migration guard or contract test.
