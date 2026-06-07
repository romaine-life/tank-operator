// Pure decision for the `/` keyboard shortcut that focuses the chat composer.
// App.tsx owns the DOM wiring (locating the textarea, switching tabs, and the
// deferred focus that runs after a tab change); this module owns only the
// branch-free decision so it can be unit-tested without a DOM.

/**
 * Tabs that render the shared composer in-page (see `composerVisible` in
 * App.tsx / WorkspaceShell). On these tabs `/` focuses the composer where it
 * already is; on any other tab the composer is unmounted, so `/` first
 * navigates to the turns view, which is the primary composer surface.
 *
 * Keep this set in sync with the `composerVisible` predicate in App.tsx.
 */
const TABS_WITH_COMPOSER = new Set(["chat", "turns"]);

export interface ComposerFocusShortcutContext {
  /** `KeyboardEvent.key`. */
  key: string;
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  /** `KeyboardEvent.isComposing` — true mid-IME composition. */
  isComposing: boolean;
  /** Whether the event target is the composer textarea itself. */
  targetIsComposer: boolean;
  /** The active workspace tab (`chat`, `turns`, `files`, ...). */
  activeTab: string;
}

export type ComposerFocusShortcutAction =
  /** Not the shortcut (or the composer already owns the keystroke): do nothing. */
  | "ignore"
  /** The composer is already on this tab: focus it in place, no navigation. */
  | "focus-in-place"
  /** The composer is not on this tab: switch to turns, then focus it. */
  | "switch-to-turns";

/**
 * Decide what `/` should do when pressed.
 *
 * `/` is a "jump to the prompt" shortcut: from anywhere that is not the
 * composer textarea it focuses the composer. The turns view renders the same
 * composer as the chat transcript, so the shortcut must focus it in place
 * there. Tabs without an in-page composer fall back to navigating to turns first.
 */
export function resolveComposerFocusShortcut(
  ctx: ComposerFocusShortcutContext,
): ComposerFocusShortcutAction {
  if (ctx.key !== "/") return "ignore";
  if (ctx.altKey || ctx.ctrlKey || ctx.metaKey || ctx.shiftKey) return "ignore";
  if (ctx.isComposing) return "ignore";
  // Once the composer textarea owns the keystroke, `/` is ordinary typing that
  // opens the slash-command palette through input events.
  if (ctx.targetIsComposer) return "ignore";
  return TABS_WITH_COMPOSER.has(ctx.activeTab)
    ? "focus-in-place"
    : "switch-to-turns";
}
