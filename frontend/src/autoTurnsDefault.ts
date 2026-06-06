// Auto-default-to-Turns gate.
//
// Once a session has accumulated enough real user back-and-forths, opening it
// from the sidebar lands in the Turns view (latest turn) instead of the main
// transcript. The main transcript's value is "scan the whole arc at a glance,"
// which stops paying off once the conversation is long; past that point the
// Turns view — which lands you directly on the most recent exchange — is the
// better default. This is an ergonomics default-switch, not a failure guard:
// it changes where a click *lands*, never where an already-open pane sits.
//
// The signal is the durable per-session `user_message_count`: the count of
// `user_message.created` events, one per human submission. Background-task wake
// continuations deliberately do not write that event (the wake prompt rides on
// `turn.submitted.payload.prompt`), so SDK continuation turns never inflate the
// count — it is "back-and-forths the user initiated," exactly what we want, not
// raw SDK/turn churn. The count is durable, monotonic, and projected onto the
// session row, so this gate reads identically across reload and a fresh tab and
// never derives the number from the loaded transcript window.
//
// The threshold is deliberately a single named constant. We are not trying to
// find a magic number — only a good-enough "this session is substantial now"
// line. Because the count is monotonic, "default to turns whenever count >= N"
// is equivalent to "flip once when it crosses N, forever after." Keeping the
// rule here (pure + unit-tested) makes the threshold a one-line tunable and
// pins it as a regression guard.
//
// A manual open-target choice from the session tab menu always overrides this
// default; this gate only decides the default when the user has not chosen.

export const AUTO_TURNS_USER_MESSAGE_THRESHOLD = 8;

// shouldAutoDefaultToTurns reports whether a session with the given durable
// user-message count should default to the Turns view. A missing, non-finite,
// or negative count is treated as "not yet substantial" so a row that has not
// reported a count keeps the main-transcript default rather than guessing.
export function shouldAutoDefaultToTurns(
  userMessageCount: number | null | undefined,
): boolean {
  if (
    typeof userMessageCount !== "number" ||
    !Number.isFinite(userMessageCount)
  ) {
    return false;
  }
  return userMessageCount >= AUTO_TURNS_USER_MESSAGE_THRESHOLD;
}
