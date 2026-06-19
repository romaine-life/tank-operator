import type { SessionBusConfig } from "./sessionBus.js";

export interface ResumeTranscript {
  sdkSessionId: string;
  relPath: string;
  sdkVersion: string;
  bytes: Uint8Array;
}

export function fetchResumeTranscript(
  cfg: SessionBusConfig,
): Promise<ResumeTranscript | null>;
