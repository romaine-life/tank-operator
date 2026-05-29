// Pure helpers for merging the durable server-projected transcript with the
// optimistic ("realtime") entries the SPA appends locally before the matching
// Tank events land. These were extracted verbatim from App.tsx so the
// dedup/merge contract can be unit-tested without standing up the whole React
// shell; App.tsx imports them back in.
//
// The load-bearing invariant: every optimistic entry must be pruned once the
// server emits its durable equivalent, otherwise the user sees the same row
// twice. Plain user/assistant messages dedup on `${role}:${text}`. Skill
// invocations need special handling because the optimistic card and the
// durable projection carry *different* `text` — see entryMessageFingerprint.

import type { TranscriptEntry } from "./App";

const META_FINGERPRINT_SEP = String.fromCharCode(0);

// Skill invocation rows render the same action label ("Test skill") in both
// shapes, but their raw `text` differs:
//   - optimistic local card: text = skillActionText(name) ("Test skill"),
//     messageKind = "skill-action", skillName = name
//   - durable server projection: text = the raw "/test ..." prompt that was
//     submitted, display.kind = "skill_invocation", display.skill_name = name
// A text-based fingerprint therefore never matched, so the optimistic card was
// never pruned once the durable event arrived and the skill card rendered
// twice (the skill-card duplication bug). entrySkillName recovers the skill
// identity from whichever shape an entry is in.
export function entrySkillName(entry: TranscriptEntry): string | null {
  if (entry.kind !== "message" || entry.role !== "user") return null;
  const explicit = (entry as Record<string, unknown>).skillName;
  if (typeof explicit === "string" && explicit.length > 0) return explicit;
  if (entry.display?.kind === "skill_invocation" && entry.display.skill_name) {
    return entry.display.skill_name;
  }
  return null;
}

export function entryMessageFingerprint(entry: TranscriptEntry): string | null {
  if (entry.kind !== "message" || !entry.role) return null;
  // Skill invocations fingerprint by skill identity + client nonce so the
  // optimistic card collapses onto its durable projection (their raw text
  // differs), while distinct invocations of the same skill — which carry
  // distinct client nonces — never cross-prune each other.
  const skillName = entrySkillName(entry);
  if (skillName) {
    return `${entry.role}:skill:${skillName}:${entry.clientNonce ?? ""}`;
  }
  if (!entry.text) return null;
  const text = entry.text.trim();
  return text ? `${entry.role}:${text}` : null;
}

export function entryMetaFingerprint(entry: TranscriptEntry): string | null {
  if (entry.kind !== "meta") return null;
  return [
    entry.meta?.title ?? "",
    entry.meta?.detail ?? "",
    entry.meta?.severity ?? "",
  ].join(META_FINGERPRINT_SEP);
}

export function shouldDropRealtimeEntry(
  entry: TranscriptEntry,
  serverIds: Set<string>,
  serverEventIds: Set<string>,
  serverMetaFingerprints: Set<string>,
  serverMessageFingerprints: Set<string>,
): boolean {
  if (serverIds.has(entry.id)) return true;
  if (entry.sourceEventId && serverEventIds.has(entry.sourceEventId)) return true;
  const messageFingerprint = entryMessageFingerprint(entry);
  if (
    entry.localOnly &&
    messageFingerprint &&
    serverMessageFingerprints.has(messageFingerprint)
  ) {
    return true;
  }
  const metaFingerprint = entryMetaFingerprint(entry);
  return Boolean(
    metaFingerprint &&
      entry.localOnly &&
      serverMetaFingerprints.has(metaFingerprint),
  );
}

export function pruneRealtimeEntries(
  server: TranscriptEntry[],
  realtime: TranscriptEntry[],
): TranscriptEntry[] {
  if (server.length === 0) return realtime;
  const serverIds = new Set(server.map((entry) => entry.id));
  const serverEventIds = new Set(
    server.map((entry) => entry.sourceEventId).filter((id): id is string => Boolean(id)),
  );
  const serverMetaFingerprints = new Set(
    server
      .map(entryMetaFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  const serverMessageFingerprints = new Set(
    server
      .map(entryMessageFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  return realtime.filter(
    (entry) =>
      !shouldDropRealtimeEntry(
        entry,
        serverIds,
        serverEventIds,
        serverMetaFingerprints,
        serverMessageFingerprints,
      ),
  );
}

export function pruneLocalRealtimeEchoes(realtime: TranscriptEntry[]): TranscriptEntry[] {
  const nonLocalMessageFingerprints = new Set(
    realtime
      .filter((entry) => !entry.localOnly)
      .map(entryMessageFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  if (nonLocalMessageFingerprints.size === 0) return realtime;
  return realtime.filter((entry) => {
    if (!entry.localOnly) return true;
    const fingerprint = entryMessageFingerprint(entry);
    return !fingerprint || !nonLocalMessageFingerprints.has(fingerprint);
  });
}

export function dedupeAdjacentAssistantEchoes(entries: TranscriptEntry[]): TranscriptEntry[] {
  const out: TranscriptEntry[] = [];
  for (const entry of entries) {
    const prev = out[out.length - 1];
    if (
      prev?.kind === "message" &&
      entry.kind === "message" &&
      prev.role === "assistant" &&
      entry.role === "assistant" &&
      prev.text?.trim() &&
      prev.text.trim() === entry.text?.trim()
    ) {
      out[out.length - 1] = entry.transcriptSource === "server" ? entry : prev;
      continue;
    }
    out.push(entry);
  }
  return out;
}

export function mergeSdkTranscript(
  server: TranscriptEntry[],
  realtime: TranscriptEntry[],
): TranscriptEntry[] {
  if (realtime.length === 0) return server;
  const extra = pruneRealtimeEntries(server, realtime);
  if (server.length === 0) return dedupeAdjacentAssistantEchoes(extra);
  if (extra.length === 0) return server;
  return dedupeAdjacentAssistantEchoes([...server, ...extra]);
}
