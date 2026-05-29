export const TRANSCRIPT_AUTHOR_GROUP_WINDOW_MS = 7 * 60 * 1000;

export type TranscriptAuthorGroupingEntry = {
  kind?: unknown;
  role?: unknown;
  time?: unknown;
  originSessionId?: unknown;
  authorKind?: unknown;
  messageKind?: unknown;
  skillName?: unknown;
  severity?: unknown;
};

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

function messageTimeMs(entry: TranscriptAuthorGroupingEntry): number | null {
  const raw = optionalString(entry.time);
  if (!raw) return null;
  const parsed = Date.parse(raw);
  return Number.isFinite(parsed) ? parsed : null;
}

export function transcriptAuthorGroupKey(
  entry: TranscriptAuthorGroupingEntry,
): string | null {
  if (entry.kind !== "message") return null;
  if (entry.role !== "user" && entry.role !== "assistant" && entry.role !== "system") {
    return null;
  }

  const messageKind = optionalString(entry.messageKind) ?? "message";
  const skillName = optionalString(entry.skillName) ?? "";

  if (entry.role === "user") {
    const originSessionId = optionalString(entry.originSessionId);
    const authorKind = optionalString(entry.authorKind);
    // Precedence mirrors the renderer's avatar selection: a sibling-session
    // handoff (origin) first, then a bot-authored "system" turn, then the
    // interactive human owner. Distinct keys keep a bot turn from grouping
    // into a human's avatar run (and vice versa).
    const author = originSessionId
      ? `session:${originSessionId}`
      : authorKind === "system"
        ? "system"
        : "human";
    return `user:${author}:${messageKind}:${skillName}`;
  }

  if (entry.role === "system") {
    const severity = optionalString(entry.severity) ?? "info";
    return `system:${severity}:${messageKind}:${skillName}`;
  }

  return `assistant:${messageKind}:${skillName}`;
}

export function shouldGroupTranscriptMessageWithPrevious(
  previous: TranscriptAuthorGroupingEntry | null | undefined,
  current: TranscriptAuthorGroupingEntry | null | undefined,
): boolean {
  if (!previous || !current) return false;

  const previousKey = transcriptAuthorGroupKey(previous);
  if (!previousKey || previousKey !== transcriptAuthorGroupKey(current)) return false;

  const previousTime = messageTimeMs(previous);
  const currentTime = messageTimeMs(current);
  if (previousTime == null || currentTime == null) return true;

  const gap = currentTime - previousTime;
  return gap >= 0 && gap <= TRANSCRIPT_AUTHOR_GROUP_WINDOW_MS;
}
