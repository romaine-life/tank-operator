import type { SessionBusConfig } from "./sessionBus.js";

export interface TranscriptSnapshot {
  /** SDK session id (the JSONL filename without extension). */
  sdkSessionId: string;
  /** Path of the JSONL file relative to the runner's HOME, stored verbatim. */
  relPath: string;
  /** Whole-file JSONL bytes. */
  bytes: Uint8Array;
  /** File mtime in epoch milliseconds, for capture-lag accounting. */
  mtimeMs: number;
  /** @anthropic-ai/claude-agent-sdk version, for the restore-compat gate. */
  sdkVersion?: string;
}

export function uploadTranscriptSnapshot(
  cfg: SessionBusConfig,
  snap: TranscriptSnapshot,
): Promise<boolean>;
