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
