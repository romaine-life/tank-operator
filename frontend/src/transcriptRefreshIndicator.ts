// Pure helpers for the transient "Refreshed" confirmation shown after an
// R-driven transcript refresh.
//
// Pressing R force-pulls the durable transcript tail (see
// transcriptRefreshShortcut.ts for the gate, App.tsx for the pull). Because
// that recovery is otherwise invisible — a successful pull that delivers no
// new rows looks identical to "nothing happened" — we surface a brief, plain
// confirmation in the same slot as the connection pill so the keypress has
// visible feedback. This is a debug affordance; it is deliberately transient
// rather than persistent chrome.
//
// This module owns ONLY the timing constant and the decision of what label to
// show for a given pane state. The flash lifecycle (timer, per-session
// plumbing, rendering) lives in App.tsx. Keeping the surface gate pure makes
// it unit-testable without a DOM and keeps it aligned with the refresh
// shortcut's own chat+turns gate.

// How long the confirmation stays visible after a refresh. Long enough to
// read, short enough to feel transient.
export const REFRESH_FLASH_DURATION_MS = 1400;

// The confirmation copy. Past tense: the force-pull was already kicked off by
// the time this shows.
export const REFRESH_FLASH_LABEL = "Refreshed";

export interface RefreshFlashContext {
  /** Whether this pane is the visible/active workspace pane. */
  visible: boolean;
  /** The active workspace tab. */
  activeTab: string;
  /** Whether a refresh flash is currently within its display window. */
  active: boolean;
}

/**
 * The transient label to surface in the connection-pill slot, or null when no
 * flash should show.
 *
 * The surface gate mirrors the refresh shortcut itself: chat + turns only, and
 * only while the pane is visible. A flash that started on chat must not bleed
 * onto an unrelated tab if the user switches mid-window.
 */
export function refreshFlashLabel(ctx: RefreshFlashContext): string | null {
  if (!ctx.active) return null;
  if (!ctx.visible) return null;
  if (ctx.activeTab !== "chat" && ctx.activeTab !== "turns") return null;
  return REFRESH_FLASH_LABEL;
}
