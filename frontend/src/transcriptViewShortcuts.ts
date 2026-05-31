// Pure predicates for keyboard shortcuts that switch between the transcript
// and its turn-detail view. App.tsx owns DOM wiring and navigation side effects.

export interface TranscriptViewShortcutContext {
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
  /** Whether the shared transcript `<main>` element is the event target. */
  targetIsTranscript: boolean;
  /** Whether the event target is a text-entry control. */
  targetIsTextEntry: boolean;
  activeTab: string;
  turnsAvailable: boolean;
  /** Whether a slash / mention / MCP palette currently owns keystrokes. */
  palettesOpen: boolean;
}

function isPlainNonRepeatingKey(ctx: TranscriptViewShortcutContext): boolean {
  if (ctx.repeat) return false;
  if (ctx.isComposing) return false;
  if (ctx.altKey || ctx.ctrlKey || ctx.metaKey || ctx.shiftKey) return false;
  if (ctx.palettesOpen) return false;
  return true;
}

export function isTranscriptToTurnsShortcut(
  ctx: TranscriptViewShortcutContext,
): boolean {
  if (!isPlainNonRepeatingKey(ctx)) return false;
  if (ctx.activeTab !== "chat") return false;
  if (!ctx.turnsAvailable) return false;
  if (!ctx.targetIsTranscript) return false;
  return ctx.key.toLowerCase() === "t";
}

export function isTurnsToTranscriptShortcut(
  ctx: TranscriptViewShortcutContext,
): boolean {
  if (!isPlainNonRepeatingKey(ctx)) return false;
  if (ctx.activeTab !== "turns") return false;
  if (ctx.targetIsTextEntry) return false;
  return ctx.key === "Escape";
}
