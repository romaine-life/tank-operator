import type { ConversationTurnTerminalStatus } from "./conversationReducer";

export interface CompactableTranscriptEntry {
  id: string;
  kind?: string;
  role?: string;
  turnId?: string;
  turnTerminalStatus?: ConversationTurnTerminalStatus;
}

export type CompactedTranscriptGroup<T extends CompactableTranscriptEntry> =
  | { kind: "entry"; entry: T }
  | { kind: "activity"; id: string; turnId: string; entries: T[]; active?: boolean };

interface TurnActivity<T extends CompactableTranscriptEntry> {
  turnId: string;
  insertBefore: number;
  entries: T[];
  active?: boolean;
}

export function compactCompletedTurnEntries<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
  enabled: boolean,
  activeTurnId: string | null = null,
): CompactedTranscriptGroup<T>[] {
  if (!enabled) return entries.map((entry) => ({ kind: "entry", entry }));

  const activities = [
    ...completedTurnActivities(entries),
    ...activeTurnActivities(entries, activeTurnId),
  ];
  if (activities.length === 0) {
    return entries.map((entry) => ({ kind: "entry", entry }));
  }

  const activityByInsertIndex = new Map<number, TurnActivity<T>>();
  const compactedIndexes = new Set<number>();
  for (const activity of activities) {
    activityByInsertIndex.set(activity.insertBefore, activity);
    for (const entry of activity.entries) {
      const index = entries.indexOf(entry);
      if (index >= 0) compactedIndexes.add(index);
    }
  }

  const out: CompactedTranscriptGroup<T>[] = [];
  entries.forEach((entry, index) => {
    const activity = activityByInsertIndex.get(index);
    if (activity) {
      out.push({
        kind: "activity",
        id: `turn-activity-${activity.turnId}`,
        turnId: activity.turnId,
        entries: activity.entries,
        active: activity.active,
      });
    }
    if (!compactedIndexes.has(index)) out.push({ kind: "entry", entry });
  });
  return out;
}

function completedTurnActivities<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
): TurnActivity<T>[] {
  const turnIndexes = new Map<string, number[]>();
  entries.forEach((entry, index) => {
    if (!entry.turnId) return;
    const indexes = turnIndexes.get(entry.turnId) ?? [];
    indexes.push(index);
    turnIndexes.set(entry.turnId, indexes);
  });

  const activities: TurnActivity<T>[] = [];
  for (const [turnId, indexes] of turnIndexes) {
    if (!indexes.some((index) => entries[index]?.turnTerminalStatus === "completed")) {
      continue;
    }
    const finalAssistantStart = finalAssistantRunStart(entries, indexes);
    if (finalAssistantStart == null) continue;
    const activityEntries = indexes
      .filter((index) => index < finalAssistantStart)
      .map((index) => entries[index])
      .filter((entry): entry is T => Boolean(entry) && !isUserMessage(entry));
    if (activityEntries.length === 0) continue;
    activities.push({
      turnId,
      insertBefore: finalAssistantStart,
      entries: activityEntries,
    });
  }
  return activities.sort((a, b) => a.insertBefore - b.insertBefore);
}

function activeTurnActivities<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
  activeTurnId: string | null,
): TurnActivity<T>[] {
  if (!activeTurnId) return [];
  const indexes = entries
    .map((entry, index) => ({ entry, index }))
    .filter(({ entry }) => entry.turnId === activeTurnId && !entry.turnTerminalStatus)
    .map(({ index }) => index);
  if (indexes.length === 0) return [];

  const finalAssistantStart = trailingAssistantRunStart(entries, indexes);
  const activityEntries = indexes
    .filter((index) => finalAssistantStart == null || index < finalAssistantStart)
    .map((index) => entries[index])
    .filter((entry): entry is T => Boolean(entry) && !isUserMessage(entry));
  if (activityEntries.length === 0) return [];

  return [{
    turnId: activeTurnId,
    insertBefore: finalAssistantStart ?? activityEntriesIndex(entries, activityEntries[0]),
    entries: activityEntries,
    active: true,
  }];
}

function finalAssistantRunStart<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
  indexes: readonly number[],
): number | null {
  let lastAssistantPosition = -1;
  for (let pos = indexes.length - 1; pos >= 0; pos -= 1) {
    const entry = entries[indexes[pos] ?? -1];
    if (entry && isAssistantMessage(entry)) {
      lastAssistantPosition = pos;
      break;
    }
  }
  if (lastAssistantPosition < 0) return null;

  let startPosition = lastAssistantPosition;
  while (startPosition > 0) {
    const previousIndex = indexes[startPosition - 1];
    const previous = entries[previousIndex ?? -1];
    if (!previous || !isAssistantMessage(previous)) break;
    startPosition -= 1;
  }
  return indexes[startPosition] ?? null;
}

function trailingAssistantRunStart<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
  indexes: readonly number[],
): number | null {
  if (indexes.length === 0) return null;
  const lastEntry = entries[indexes[indexes.length - 1] ?? -1];
  if (!lastEntry || !isAssistantMessage(lastEntry)) return null;
  return finalAssistantRunStart(entries, indexes);
}

function activityEntriesIndex<T extends CompactableTranscriptEntry>(
  entries: readonly T[],
  entry: T | undefined,
): number {
  if (!entry) return 0;
  const index = entries.indexOf(entry);
  return index >= 0 ? index : 0;
}

function isUserMessage(entry: CompactableTranscriptEntry): boolean {
  return entry.kind === "message" && entry.role === "user";
}

function isAssistantMessage(entry: CompactableTranscriptEntry): boolean {
  return entry.kind === "message" && entry.role === "assistant";
}
