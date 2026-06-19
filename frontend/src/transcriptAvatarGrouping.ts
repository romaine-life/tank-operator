import {
  shouldGroupTranscriptMessageWithPrevious,
  type TranscriptAuthorGroupingEntry,
} from "./transcriptAuthorGrouping";

export type TranscriptAvatarGroupingGroup<T extends TranscriptAuthorGroupingEntry> =
  | { kind: "message"; entry: T }
  | { kind: "message_group"; entries: T[] }
  | { kind: string; entry?: T; entries?: T[] };

function messageEntryForAvatarGrouping<T extends TranscriptAuthorGroupingEntry>(
  group: TranscriptAvatarGroupingGroup<T> | null | undefined,
): T | null {
  if (!group) return null;
  return group.kind === "message" && group.entry ? group.entry : null;
}

function isTransparentAvatarActivityGroup(
  group: TranscriptAvatarGroupingGroup<TranscriptAuthorGroupingEntry> | null | undefined,
): boolean {
  if (!group) return false;
  return (
    group.kind === "tools" ||
    group.kind === "reasoning" ||
    group.kind === "background_task" ||
    group.kind === "thinking" ||
    group.kind === "activity"
  );
}

function previousMessageForAvatarGrouping<T extends TranscriptAuthorGroupingEntry>(
  groups: readonly TranscriptAvatarGroupingGroup<T>[],
  index: number,
): T | null {
  for (let i = index - 1; i >= 0; i -= 1) {
    const group = groups[i];
    const message = messageEntryForAvatarGrouping(group);
    if (message) return message;
    if (!isTransparentAvatarActivityGroup(group)) return null;
  }
  return null;
}

export function isTranscriptMessageAvatarContinuation<
  T extends TranscriptAuthorGroupingEntry,
>(
  groups: readonly TranscriptAvatarGroupingGroup<T>[],
  index: number,
): boolean {
  const current = messageEntryForAvatarGrouping(groups[index]);
  const previous = previousMessageForAvatarGrouping(groups, index);
  return shouldGroupTranscriptMessageWithPrevious(previous, current);
}
