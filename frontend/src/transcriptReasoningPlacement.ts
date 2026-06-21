// Reasoning is Turn-activity material — never a settled main-transcript row.
//
// docs/features/transcript/contract.md makes the main transcript promotion-only:
// "provider activity, reasoning, tool output, and provisional assistant prose
// must not default into it." A durable `kind:"reasoning"` display item (the
// agent's "Thinking..." summary) belongs in Turn activity: the compacted
// per-turn disclosure (RunTurnActivityGroup) and the dedicated Turns view
// (RunTurnActivityScreen). The server projection folds reasoning into the
// turn-activity shell's compacted children and keeps it out of the settled
// transcript; this gate is the frontend's matching invariant, so the
// main-transcript grouper never settles reasoning even if a raw reasoning row
// reaches the loaded timeline window.
//
// Structural typing (a bare `{ kind }`) keeps this module free of an App.tsx
// import, avoiding a cycle (App.tsx imports this gate).

export interface MainTranscriptPlacementEntry {
  kind?: string | null;
}

/** A durable reasoning ("Thinking...") display item. */
export function isReasoningTranscriptEntry(
  entry: MainTranscriptPlacementEntry | null | undefined,
): boolean {
  return entry?.kind === "reasoning";
}

/**
 * True for entries that are Turn-activity-only material and must never become a
 * settled main-transcript group. Today the sole member is reasoning; this gate
 * gives the promotion-only invariant a single durable home (and a clear
 * extension point for any future activity-only display kind), so the
 * main-transcript grouper has exactly one place to consult.
 */
export function isActivityOnlyMainTranscriptEntry(
  entry: MainTranscriptPlacementEntry | null | undefined,
): boolean {
  return isReasoningTranscriptEntry(entry);
}
