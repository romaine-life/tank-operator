import type { TranscriptEntry } from "./App";

export type ActivityEntriesByTurn = Record<string, TranscriptEntry[] | undefined>;

function rowTurnId(row: TranscriptEntry): string {
  return (row.turnId ?? row.activity?.turnId ?? "").trim();
}

function activityCacheHasLoadedTurn(
  activityEntriesByTurn: ActivityEntriesByTurn,
  turnId: string,
): boolean {
  return activityEntriesByTurn[turnId] !== undefined;
}

function transcriptRowCanChangeTurnActivityBody(row: TranscriptEntry): boolean {
  if (row.kind === "turn_activity") return true;
  if (row.kind === "message" && row.role === "user") return false;
  if (row.metaKind === "needs_input_announcement") return false;
  return rowTurnId(row) !== "";
}

export function turnActivityRefreshTargetsForTranscriptRows(
  rows: TranscriptEntry[],
  activityEntriesByTurn: ActivityEntriesByTurn,
): string[] {
  const targets = new Set<string>();
  for (const row of rows) {
    const turnId = rowTurnId(row);
    if (!turnId) continue;
    if (!activityCacheHasLoadedTurn(activityEntriesByTurn, turnId)) continue;
    if (!transcriptRowCanChangeTurnActivityBody(row)) continue;
    targets.add(turnId);
  }
  return Array.from(targets);
}
