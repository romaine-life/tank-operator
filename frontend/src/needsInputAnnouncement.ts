import type { ConversationTurnTerminalStatus } from "./conversationReducer";

// The visual/semantic state of an AskUserQuestion handoff row
// (the "Claude is waiting on you" card promoted into the main transcript
// by projectNeedsInputAnnouncement). The renderer
// (RunNeedsInputAnnouncement in App.tsx) drives copy, icon, styling, and
// CTA emphasis off this single value so the three states stay coherent.
//
// - "waiting"  — unanswered and the owning turn is still live. This is the
//                only state that warrants the attention-grabbing active
//                styling; the agent genuinely cannot proceed until the user
//                answers.
// - "answered" — the user submitted an answer (durable
//                tool.approval_resolved). The handoff is resolved; render it
//                muted with a "View in Turns" affordance.
// - "settled"  — unanswered, but the owning turn has reached a terminal
//                state (the user stopped it, or it failed). Nothing is being
//                waited on anymore, so the card must NOT keep shouting for
//                input. Same muted, low-demand presentation as "answered",
//                different copy ("No longer waiting").
export type NeedsInputAnnouncementState = "waiting" | "answered" | "settled";

// needsInputAnnouncementState derives the handoff row's state from the two
// durable facts the projection can supply: whether the question was answered
// (announcement.answered, sourced from tool.approval_resolved) and the
// terminal status of the turn that owns the question (annotated onto the
// entry by annotateTurnTerminals / annotateProjectionTerminal once a
// turn.completed / turn.failed / turn.command_failed / turn.interrupted
// event lands).
//
// `answered` always wins: a question that the user answered stays "answered"
// even if the turn is later interrupted or fails for an unrelated reason —
// the handoff itself was satisfied. Only an *unanswered* question that
// outlives its turn settles into the inert state.
//
// This is a pure function with no dependency on React or the projection
// internals so it can be unit-tested directly and shared verbatim between
// the live reducer projection and the server-projected (fresh-tab) path,
// which both feed the same renderer.
export function needsInputAnnouncementState(input: {
  answered: boolean;
  turnTerminalStatus?: ConversationTurnTerminalStatus | null;
}): NeedsInputAnnouncementState {
  if (input.answered) return "answered";
  if (input.turnTerminalStatus != null) return "settled";
  return "waiting";
}

// needsInputAnnouncementIsSettled is true for any state that should render
// with the muted, non-demanding treatment (answered or settled) rather than
// the active "waiting on you" styling. Kept as a named helper so the
// renderer and tests share one definition of "this row is no longer
// shouting for input".
export function needsInputAnnouncementIsSettled(
  state: NeedsInputAnnouncementState,
): boolean {
  return state !== "waiting";
}
