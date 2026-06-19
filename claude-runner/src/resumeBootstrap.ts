// Resume bootstrap (docs/session-transcript-capture.md, Stage 2).
//
// On a resurrected pod, before the SDK query is constructed, fetch the dead
// session's captured transcript from the orchestrator and materialize it at the
// EXACT path the SDK expects ($HOME/<relPath>), then return its SDK session id
// so the runner `resume`s that specific session instead of `continue`-ing a
// fresh one.
//
// Safety:
//   * Version gate — refuse to resume across an SDK-format gap (the captured
//     JSONL may be rejected/corrupt on a newer engine); start fresh instead of
//     producing a broken resume.
//   * Path containment — the rel-path is server-supplied; reject anything that
//     would escape HOME.
//   * Total isolation — any failure returns undefined (start fresh), never
//     throws into boot.

import { mkdir, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join, normalize, sep } from "node:path";

import type { ResumeTranscript } from "../../runner-shared/transcriptDownload.js";

export type ResumeOutcome =
  | "materialized"
  | "not_found"
  | "version_mismatch"
  | "error";

export interface ResumeBootstrapDeps {
  homeDir: string;
  runningSdkVersion: string;
  fetchTranscript(): Promise<ResumeTranscript | null>;
  log?(rec: Record<string, unknown>): void;
  onOutcome?(outcome: ResumeOutcome): void;
}

function safeJoin(homeDir: string, relPath: string): string | null {
  if (!relPath || isAbsolute(relPath)) return null;
  const base = normalize(homeDir);
  const full = normalize(join(base, relPath));
  if (full !== base && !full.startsWith(base + sep)) return null;
  return full;
}

export async function runResumeBootstrap(
  deps: ResumeBootstrapDeps,
): Promise<string | undefined> {
  const log = deps.log ?? ((r) => console.log(JSON.stringify({ component: "resume-bootstrap", ...r })));
  try {
    const t = await deps.fetchTranscript();
    if (!t || !t.sdkSessionId || !t.relPath || !t.bytes || t.bytes.length === 0) {
      deps.onOutcome?.("not_found");
      return undefined;
    }
    // Version gate: only resume when the capture and the running engine agree
    // (or the captured version is unknown). A mismatch starts fresh — never a
    // corrupt resume.
    if (t.sdkVersion && deps.runningSdkVersion && t.sdkVersion !== deps.runningSdkVersion) {
      log({ msg: "resume skipped: SDK version mismatch; starting fresh", captured: t.sdkVersion, running: deps.runningSdkVersion });
      deps.onOutcome?.("version_mismatch");
      return undefined;
    }
    const dest = safeJoin(deps.homeDir, t.relPath);
    if (!dest) {
      log({ msg: "resume skipped: unsafe transcript rel-path", rel_path: t.relPath });
      deps.onOutcome?.("error");
      return undefined;
    }
    await mkdir(dirname(dest), { recursive: true });
    await writeFile(dest, t.bytes);
    log({ msg: "resume transcript materialized", sdk_session_id: t.sdkSessionId, bytes: t.bytes.length });
    deps.onOutcome?.("materialized");
    return t.sdkSessionId;
  } catch (err) {
    log({ msg: "resume bootstrap failed; starting fresh", error: String(err) });
    deps.onOutcome?.("error");
    return undefined;
  }
}
