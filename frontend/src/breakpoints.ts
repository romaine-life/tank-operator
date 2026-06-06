// Canonical responsive breakpoints for tank-operator's compact ("mobile") shell.
//
// These two widths are the single source of truth for "is this a phone-sized
// viewport" decisions. JS reads them through useViewport() (see useViewport.ts);
// index.css mirrors the same widths in its compact media queries (search
// "BP_COMPACT" in the "Compact / mobile shell" section). Keep the two in sync —
// breakpoints.test.ts pins the values and the derived queries so drift fails CI.
//
// Why two tiers: 768 is where the 260px sidebar starts crowding the work pane,
// so the shell switches to a single column + off-canvas drawer there. 640 is the
// denser phone tuning tier (label hiding, tighter gutters). The boundary is
// inclusive: a viewport whose width is <= the value is "in" that tier.

export const BP_COMPACT = 768;
export const BP_PHONE = 640;

/** matchMedia query: viewport is compact (single-column shell + nav drawer). */
export const MQ_COMPACT = `(max-width: ${BP_COMPACT}px)`;
/** matchMedia query: viewport is phone-sized (densest tuning). */
export const MQ_PHONE = `(max-width: ${BP_PHONE}px)`;

/** Pure predicate form, used by tests and any width-driven logic. */
export const isCompactWidth = (width: number): boolean => width <= BP_COMPACT;
export const isPhoneWidth = (width: number): boolean => width <= BP_PHONE;
