import type { TranscriptEntry } from "./App.tsx";

export type ActivityEntriesByTurn = Record<string, TranscriptEntry[] | undefined>;

function cachedTurnExists(cache: ActivityEntriesByTurn, turnId: string): boolean {
  return Object.prototype.hasOwnProperty.call(cache, turnId);
}

function transcriptEntryTurnId(entry: TranscriptEntry): string {
  return (entry.turnId ?? entry.activity?.turnId ?? "").trim();
}

function transcriptEntryRefreshCursor(entry: TranscriptEntry): string {
  return (
    entry.activity?.endOrderKey ??
    entry.turnTerminalOrderKey ??
    entry.orderKey ??
    entry.id ??
    ""
  );
}

function isUserMessageEntry(entry: TranscriptEntry): boolean {
  return entry.kind === "message" && entry.role === "user";
}

function isTurnActivityUserMessageEntry(entry: TranscriptEntry): boolean {
  return (
    isUserMessageEntry(entry) &&
    (entry.turnOnly === true || entry.wakePrompt === true)
  );
}

// isAlwaysVisibleTurnDetailEntry reports whether a Turn-activity entry stays
// visible even when a completed turn's activity log is collapsed behind the
// divider. The final assistant answer is always visible; so is the system-user
// background-wake prompt — it is the settled message explaining why the agent
// resumed (a background task finished while the session was idle), not
// collapsible tool noise. Burying it inside the collapsed activity was the
// "the wake never shows" defect: a single-wake continuation folds into the
// origin turn correctly, but the prompt then read as missing.
export function isAlwaysVisibleTurnDetailEntry(
  entry: TranscriptEntry,
  finalDetailEntryIds: ReadonlySet<string>,
): boolean {
  return (
    finalDetailEntryIds.has(entry.id) || isTurnActivityUserMessageEntry(entry)
  );
}

export function cachedTurnActivityRefreshRequests(
  cache: ActivityEntriesByTurn,
  rows: TranscriptEntry[],
): Map<string, string> {
  const requests = new Map<string, string>();
  for (const row of rows) {
    const turnId = transcriptEntryTurnId(row);
    if (!turnId || !cachedTurnExists(cache, turnId)) continue;
    if (isUserMessageEntry(row) && !isTurnActivityUserMessageEntry(row))
      continue;
    const cursor = transcriptEntryRefreshCursor(row);
    const previous = requests.get(turnId) ?? "";
    if (!previous || (cursor && cursor > previous)) {
      requests.set(turnId, cursor);
    }
  }
  return requests;
}
