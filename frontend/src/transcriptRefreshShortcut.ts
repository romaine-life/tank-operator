// Pure predicate for the transcript "refresh" keyboard shortcut (R).
//
// Pressing R while the transcript region is focused force-pulls the durable
// transcript tail so messages that a live SSE gap failed to deliver appear
// without a full browser reload. This is the lighter, in-place recovery the
// Transcript contract explicitly blesses ("refresh may recover from a broken
// browser state, but refresh must not be required for normal progress"). The
// shortcut is shared by the main chat transcript and the Turns page, which
// render inside the same focusable `<main>` scaffold (see WorkspaceShell).
//
// This module owns ONLY the decision of whether a keydown should trigger that
// refresh. The effect that wires it to the DOM and performs the durable pull
// lives in App.tsx. Keeping the gate pure makes its guard conditions —
// modifier keys, IME composition, OS key auto-repeat, open palettes, focus
// target, and active tab — unit-testable without a DOM.

export interface TranscriptRefreshKeyContext {
  /** `KeyboardEvent.key`. */
  key: string;
  /** `KeyboardEvent.repeat` — true for OS auto-repeat while the key is held. */
  repeat: boolean;
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  /** `KeyboardEvent.isComposing` — true mid-IME composition. */
  isComposing: boolean;
  /**
   * Whether the event target is the focusable transcript scaffold (the
   * `<main>` element shared by the chat transcript and the Turns page). This
   * is how "the transcript is highlighted" is expressed: the region has focus.
   */
  targetIsTranscript: boolean;
  /** The active workspace tab. Refresh applies only to `chat` and `turns`. */
  activeTab: string;
  /** Whether a slash / mention / MCP palette currently owns keystrokes. */
  palettesOpen: boolean;
}

/**
 * Returns true when a keydown should trigger a transcript refresh.
 *
 * The shortcut is intentionally conservative. It fires only for an unmodified
 * R press, only when the transcript region itself holds focus, only on the
 * chat or turns surfaces, and never while an input palette is open, an IME
 * composition is active, or the key is auto-repeating from being held down.
 *
 * Modifier combos are deliberately excluded so native shortcuts keep working —
 * most importantly Cmd+R / Ctrl+R, the browser's own full-page reload.
 */
export function isTranscriptRefreshShortcut(
  ctx: TranscriptRefreshKeyContext,
): boolean {
  // OS key auto-repeat while held would otherwise fan out into a refresh
  // storm; one press is one refresh.
  if (ctx.repeat) return false;
  if (ctx.isComposing) return false;
  // Any modifier means the user is reaching for a different (often native)
  // shortcut — leave Cmd+R / Ctrl+R reload and friends alone.
  if (ctx.altKey || ctx.ctrlKey || ctx.metaKey || ctx.shiftKey) return false;
  if (ctx.palettesOpen) return false;
  if (ctx.activeTab !== "chat" && ctx.activeTab !== "turns") return false;
  // "Highlighted" == the transcript region holds focus, matching how Home/End
  // already gate on the transcript scaffold being the event target.
  if (!ctx.targetIsTranscript) return false;
  // Accept caps-lock "R" (no shift) as well as plain "r".
  return ctx.key.toLowerCase() === "r";
}
