// Transcript capture (docs/session-transcript-capture.md, Stage 1).
//
// The Claude Agent SDK writes its session transcript to an on-disk JSONL under
// $HOME/.claude/projects/<encoded-cwd>/<sdkSessionId>.jsonl. That file is the
// only resume-faithful record (it carries thinking/redacted_thinking blocks +
// signatures that session_events deliberately drops), and it dies with the
// pod. This component watches that tree and ships whole-file snapshots to the
// orchestrator so a session's conversation can later be resurrected onto a
// fresh pod.
//
// Design constraints (CLAUDE.md / contract-driven):
//   * Read-only on the SDK's file. We snapshot whole files; we never write,
//     lock, or truncate them. Restore (Stage 2) is the only writer.
//   * Whole-file snapshot, NOT byte-append: the SDK rewrites the file on
//     context compaction, so an append cursor would capture a torn transcript.
//   * Crash-isolated: every path is wrapped; nothing here may throw into the
//     turn loop. The runner is the load-bearing process — capture is a sink.
//   * fs.watch is the trigger; a low-frequency reconcile is the watchdog,
//     because fs.watch silently misses events (atomic rename, editors, Linux).
//
// The scan/dedup core (`scanOnce`) is dependency-injected so it can be unit
// tested against a temp directory without real fs.watch timing.

import { watch, type FSWatcher } from "node:fs";
import { readdir, readFile, stat } from "node:fs/promises";
import { join, relative, sep } from "node:path";

import type { TranscriptSnapshot } from "../../runner-shared/transcriptUpload.js";

export type CaptureResult = "ok" | "skipped" | "error";

export interface TranscriptCaptureDeps {
  /** $HOME/.claude/projects — the SDK's per-cwd transcript root. */
  projectsRoot: string;
  /** Runner HOME, used to compute the verbatim rel-path for restore. */
  homeDir: string;
  /** @anthropic-ai/claude-agent-sdk version, best-effort, for restore-compat. */
  sdkVersion: string;
  /**
   * Ship one snapshot. Returns true when durably stored, false when the
   * upload was a no-op (storage not configured). A false result must NOT
   * advance the dedup cursor so a later retry can succeed.
   */
  upload(snap: TranscriptSnapshot): Promise<boolean>;
  /** Structured log sink; defaults to console. */
  log?(rec: Record<string, unknown>): void;
  /** Clock, injectable for tests. */
  now?(): number;
  /** Per-result counter hook (wired to prom-client in production). */
  onResult?(result: CaptureResult): void;
  /** Capture-lag gauge hook (ms between file mtime and successful upload). */
  setLagMs?(ms: number): void;
  /** fs.watch event coalescing window. */
  debounceMs?: number;
  /** Watchdog reconcile interval. */
  reconcileMs?: number;
}

const DEFAULT_DEBOUNCE_MS = 1500;
const DEFAULT_RECONCILE_MS = 30_000;

export class TranscriptCapture {
  private readonly deps: Required<
    Pick<TranscriptCaptureDeps, "projectsRoot" | "homeDir" | "sdkVersion" | "upload">
  > &
    TranscriptCaptureDeps;
  // Per-file "size:mtimeMs" of the last SUCCESSFULLY uploaded snapshot.
  private readonly uploaded = new Map<string, string>();
  private readonly watchers = new Map<string, FSWatcher>();
  private debounceTimer: NodeJS.Timeout | undefined;
  private reconcileTimer: NodeJS.Timeout | undefined;
  private scanning = false;
  private rescanRequested = false;
  private stopped = false;

  constructor(deps: TranscriptCaptureDeps) {
    this.deps = deps;
  }

  private log(rec: Record<string, unknown>): void {
    (this.deps.log ?? ((r) => console.log(JSON.stringify(r))))({
      component: "transcript-capture",
      ...rec,
    });
  }

  private nowMs(): number {
    return (this.deps.now ?? Date.now)();
  }

  start(signal: AbortSignal): void {
    if (signal.aborted) return;
    signal.addEventListener("abort", () => this.stop(), { once: true });
    // Kick an initial scan + watch setup; never let it reject.
    void this.ensureWatches();
    void this.scanOnce();
    const reconcileMs = this.deps.reconcileMs ?? DEFAULT_RECONCILE_MS;
    this.reconcileTimer = setInterval(() => {
      // The watchdog both (re)attaches watches as the SDK creates dirs and
      // rescans to catch any fs.watch events that were silently dropped.
      void this.ensureWatches();
      void this.scanOnce();
    }, reconcileMs);
    // Do not keep the process alive solely for the watchdog.
    this.reconcileTimer.unref?.();
  }

  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    if (this.reconcileTimer) clearInterval(this.reconcileTimer);
    for (const w of this.watchers.values()) {
      try {
        w.close();
      } catch {
        /* best-effort */
      }
    }
    this.watchers.clear();
  }

  private scheduleDebouncedScan(): void {
    if (this.stopped) return;
    const debounceMs = this.deps.debounceMs ?? DEFAULT_DEBOUNCE_MS;
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => {
      void this.scanOnce();
    }, debounceMs);
    this.debounceTimer.unref?.();
  }

  // ensureWatches adds an fs.watch on projectsRoot and each existing subdir
  // that is not already watched. Idempotent and called from start, every
  // reconcile, and on directory-create events, so watches attach as the SDK
  // creates the projects tree (which may not exist at pod boot).
  private async ensureWatches(): Promise<void> {
    if (this.stopped) return;
    await this.watchDir(this.deps.projectsRoot, /* onEvent watches new subdirs */ true);
    let entries: Array<{ name: string; isDirectory(): boolean }>;
    try {
      entries = (await readdir(this.deps.projectsRoot, {
        withFileTypes: true,
      })) as unknown as Array<{ name: string; isDirectory(): boolean }>;
    } catch {
      return; // projectsRoot not created yet
    }
    for (const ent of entries) {
      if (ent.isDirectory()) {
        await this.watchDir(join(this.deps.projectsRoot, ent.name), false);
      }
    }
  }

  private async watchDir(dir: string, isRoot: boolean): Promise<void> {
    if (this.stopped || this.watchers.has(dir)) return;
    let w: FSWatcher;
    try {
      w = watch(dir, { persistent: false }, (_event, _name) => {
        // On a root event a new per-cwd subdir may have appeared; re-evaluate
        // watches before scanning. Either way, coalesce into a debounced scan.
        if (isRoot) void this.ensureWatches();
        this.scheduleDebouncedScan();
      });
    } catch {
      return; // dir vanished or unwatchable; reconcile will retry
    }
    w.on("error", () => {
      try {
        w.close();
      } catch {
        /* ignore */
      }
      this.watchers.delete(dir);
    });
    this.watchers.set(dir, w);
  }

  // scanOnce is the change-detection core: enumerate every *.jsonl under the
  // projects tree, and for each whose on-disk signature differs from the last
  // successful upload, snapshot + ship it. Serialized against itself; a request
  // arriving mid-scan re-runs once at the end so no change is missed.
  async scanOnce(): Promise<void> {
    if (this.stopped) return;
    if (this.scanning) {
      this.rescanRequested = true;
      return;
    }
    this.scanning = true;
    try {
      await this.scanProjects();
    } catch (err) {
      this.log({ msg: "scan failed", error: String(err) });
    } finally {
      this.scanning = false;
      if (this.rescanRequested && !this.stopped) {
        this.rescanRequested = false;
        void this.scanOnce();
      }
    }
  }

  private async scanProjects(): Promise<void> {
    let subdirs: string[];
    try {
      const entries = (await readdir(this.deps.projectsRoot, {
        withFileTypes: true,
      } as never)) as unknown as Array<{ name: string; isDirectory(): boolean }>;
      subdirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
    } catch {
      return; // projectsRoot not present yet
    }
    for (const sub of subdirs) {
      const dir = join(this.deps.projectsRoot, sub);
      let files: string[];
      try {
        files = (await readdir(dir)).filter((f) => f.endsWith(".jsonl"));
      } catch {
        continue;
      }
      for (const file of files) {
        await this.maybeUpload(join(dir, file), file.slice(0, -".jsonl".length));
      }
    }
  }

  private async maybeUpload(fullPath: string, sdkSessionId: string): Promise<void> {
    if (this.stopped || !sdkSessionId) return;
    let size: number;
    let mtimeMs: number;
    try {
      const st = await stat(fullPath);
      if (!st.isFile()) return;
      size = st.size;
      mtimeMs = st.mtimeMs;
    } catch {
      return; // file vanished between readdir and stat
    }
    const sig = `${size}:${mtimeMs}`;
    if (this.uploaded.get(fullPath) === sig) return; // unchanged since last success

    let bytes: Buffer;
    try {
      bytes = await readFile(fullPath);
    } catch {
      this.report("error");
      return;
    }
    const relPath = relative(this.deps.homeDir, fullPath).split(sep).join("/");
    try {
      const stored = await this.deps.upload({
        sdkSessionId,
        relPath,
        bytes,
        mtimeMs,
        sdkVersion: this.deps.sdkVersion,
      });
      if (stored) {
        this.uploaded.set(fullPath, sig);
        this.deps.setLagMs?.(Math.max(0, this.nowMs() - mtimeMs));
        this.report("ok");
      } else {
        // Not configured: do NOT record the signature, so we retry later.
        this.report("skipped");
      }
    } catch (err) {
      this.log({ msg: "upload failed", sdk_session_id: sdkSessionId, error: String(err) });
      this.report("error");
    }
  }

  private report(result: CaptureResult): void {
    try {
      this.deps.onResult?.(result);
    } catch {
      /* metrics must never break capture */
    }
  }
}
