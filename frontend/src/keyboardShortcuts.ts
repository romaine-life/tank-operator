// Single source of truth for the user-facing keyboard shortcuts surfaced in the
// Help panel's Keyboard section. RunHelpScreen renders this list directly, and
// keyboardShortcuts.test.ts pins that every entry's key token is actually wired
// in a handler (and that the live handlers are all represented here), so the
// help text cannot silently drift from behavior the way the old hand-maintained
// rows did — they omitted the real T / Escape shortcuts entirely.
//
// The guard covers structural parity (key-token presence in the handlers), not
// the prose accuracy of each description; the wording stays author-owned.

export type KeyboardShortcut = {
  // Stable handle for tests and future retirement.
  id: string;
  // Human-facing key label rendered in the Help panel, e.g. "R", "Home / End".
  keys: string;
  // The bare key token(s) the handler matches on (KeyboardEvent.key, lowercased
  // for letters). Used by the parity test to prove the documented key is wired.
  tokens: string[];
  description: string;
};

export const KEYBOARD_SHORTCUTS: KeyboardShortcut[] = [
  {
    id: "refresh-transcript",
    keys: "R",
    tokens: ["r"],
    description:
      "Refresh the transcript — force-pull any durable messages that haven't been delivered yet. Works on the chat transcript and the Turns page; click the transcript (or press Tab) to focus it first.",
  },
  {
    id: "jump-edges",
    keys: "Home / End",
    tokens: ["Home", "End"],
    description: "Jump to the start or the live tail of the conversation.",
  },
  {
    id: "focus-composer-transcript",
    keys: "Tab",
    tokens: ["Tab"],
    description: "Move focus between the composer and the transcript.",
  },
  {
    id: "open-turns",
    keys: "T",
    tokens: ["t"],
    description:
      "From the chat transcript, open the Turns view for the focused turn.",
  },
  {
    id: "back-to-transcript",
    keys: "Esc",
    tokens: ["Escape"],
    description: "From the Turns view, return to the main transcript.",
  },
  {
    id: "navigate-pages-turns",
    keys: "Left / Right",
    tokens: ["ArrowLeft", "ArrowRight"],
    description:
      "When the transcript is focused, navigate to the previous or next page of activity, or step to the previous or next turn if no more pages are available.",
  },
  {
    id: "rename-session",
    keys: "F2",
    tokens: ["F2"],
    description:
      "Rename the current session, or the one highlighted in the sidebar. On the new-session screen, names the session you're about to create.",
  },
];
