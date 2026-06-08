// AgyDriver drives one Antigravity (agy) turn as a subprocess and surfaces its
// structured step stream. agy has no streaming SDK: it appends ordered JSON
// step records to a per-conversation transcript file
// (brain/<conv>/.system_generated/logs/transcript_full.jsonl) as the turn runs.
// The driver spawns agy, tails that file, and yields each NEW step to the
// adapter. One agy conversation == one Tank session: the first turn creates the
// conversation, later turns resume it with --continue (the data dir persists
// across runner restarts in the same live pod, so --continue survives a runner
// crash too).
//
// Only this turn's steps are emitted: the tailer snapshots each transcript's
// byte size before spawning agy and only forwards appended complete JSONL
// records, so a cumulative transcript (--continue appending) does not re-emit
// prior turns. The adapter additionally dedupes by step_index, so a repeated
// provider update is harmless.

import { spawn, type ChildProcess } from "node:child_process";
import { watch, type FSWatcher } from "node:fs";
import { mkdir } from "node:fs/promises";

import type { AgyStep } from "./adapters/antigravity.js";
import { TranscriptTailer } from "./transcriptTailer.js";

export interface AgyTurnRequest {
  prompt: string;
  model?: string;
  /** false for the session's first turn, true to resume the conversation. */
  resume: boolean;
  workspace: string;
}

export interface AgyTurnResult {
  exitCode: number | null;
  /** True when the turn ended because interrupt() killed the process. */
  killed: boolean;
  /** Captured stdout (agy prints the final answer in -p mode). */
  stdout: string;
  stderr: string;
}

type TranscriptEventKind =
  | "initial_scan"
  | "transcript_changed"
  | "provider_output"
  | "process_exit"
  | "watcher_error";

export type TranscriptEventSourceObservation =
  | "started"
  | "unavailable"
  | "error";

export interface AgyDriverObserver {
  recordTranscriptEventSource?: (
    result: TranscriptEventSourceObservation,
  ) => void;
}

export class TranscriptEventSourceUnavailableError extends Error {
  constructor(
    public readonly reason:
      | "transcript_event_source_unavailable"
      | "transcript_event_source_error",
    message = reason,
  ) {
    super(message);
    this.name = "TranscriptEventSourceUnavailableError";
  }
}

export class AgyDriver {
  private child: ChildProcess | null = null;
  private killed = false;

  constructor(
    private readonly agyHome: string,
    private readonly agyBin: string = "agy",
    private readonly observer: AgyDriverObserver = {},
  ) {}

  /**
   * Run one agy turn. Calls `onStep` for each new transcript step in order,
   * resolves when agy exits.
   */
  async runTurn(
    req: AgyTurnRequest,
    onStep: (step: AgyStep) => Promise<void> | void,
    signal: AbortSignal,
  ): Promise<AgyTurnResult> {
    await mkdir(this.agyHome, { recursive: true });
    const transcriptTailer = new TranscriptTailer(this.agyHome);
    await transcriptTailer.snapshotExisting();
    const transcriptEvents = new TranscriptEventSource(
      this.agyHome,
      this.observer,
    );
    transcriptEvents.start();

    const args: string[] = [];
    if (req.resume) args.push("--continue");
    args.push("--dangerously-skip-permissions");
    if (req.model) args.push("--model", req.model);
    args.push("-p", req.prompt);

    this.killed = false;
    const child = spawn(this.agyBin, args, {
      cwd: req.workspace,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    this.child = child;

    let stdout = "";
    let stderr = "";
    child.stdout?.on("data", (d: Buffer) => {
      stdout += d.toString();
      transcriptEvents.emit("provider_output");
    });
    child.stderr?.on("data", (d: Buffer) => {
      stderr += d.toString();
      transcriptEvents.emit("provider_output");
    });

    const onAbort = () => this.interrupt();
    signal.addEventListener("abort", onAbort, { once: true });

    const exitPromise = new Promise<{ code: number | null }>((resolve) => {
      child.once("exit", (code) => resolve({ code }));
      child.once("error", () => resolve({ code: null }));
    });

    // Drain on provider output or filesystem notifications, then do one final
    // pass after exit to catch records written just before the process ends.
    let exited = false;
    exitPromise.then(() => {
      exited = true;
      transcriptEvents.emit("process_exit");
    });
    try {
      transcriptEvents.emit("initial_scan");
      while (!exited) {
        const event = await transcriptEvents.next();
        if (event === "watcher_error") {
          this.interrupt();
          await exitPromise;
          throw new TranscriptEventSourceUnavailableError(
            "transcript_event_source_error",
          );
        }
        await transcriptTailer.drain(onStep);
      }
      const { code } = await exitPromise;
      await transcriptTailer.drain(onStep);

      return { exitCode: code, killed: this.killed, stdout, stderr };
    } finally {
      signal.removeEventListener("abort", onAbort);
      transcriptEvents.close();
      this.child = null;
    }
  }

  /** Signal agy to stop the in-flight turn (Stop / shutdown). */
  interrupt(): void {
    if (this.child && !this.child.killed) {
      this.killed = true;
      this.child.kill("SIGINT");
      // Escalate if it does not exit promptly.
      const child = this.child;
      setTimeout(() => {
        if (child && !child.killed) child.kill("SIGKILL");
      }, 4000).unref();
    }
  }

}

class TranscriptEventSource {
  private watcher: FSWatcher | null = null;
  private readonly queue: TranscriptEventKind[] = [];
  private resolveWaiter: (() => void) | null = null;

  constructor(
    private readonly agyHome: string,
    private readonly observer: AgyDriverObserver,
  ) {}

  start(): void {
    try {
      this.watcher = watch(this.agyHome, { recursive: true }, () => {
        this.emit("transcript_changed");
      });
      this.observer.recordTranscriptEventSource?.("started");
      this.watcher.on("error", (err) => {
        console.warn(
          `antigravity transcript watcher error: ${
            err instanceof Error ? err.message : String(err)
          }`,
        );
        this.observer.recordTranscriptEventSource?.("error");
        this.emit("watcher_error");
      });
    } catch (err) {
      console.warn(
        `antigravity transcript watcher unavailable: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
      this.observer.recordTranscriptEventSource?.("unavailable");
      throw new TranscriptEventSourceUnavailableError(
        "transcript_event_source_unavailable",
      );
    }
  }

  emit(kind: TranscriptEventKind): void {
    this.queue.push(kind);
    const resolve = this.resolveWaiter;
    if (!resolve) return;
    this.resolveWaiter = null;
    resolve();
  }

  next(): Promise<TranscriptEventKind> {
    const event = this.queue.shift();
    if (event) {
      return Promise.resolve(event);
    }
    return new Promise((resolve) => {
      this.resolveWaiter = () => {
        resolve(this.queue.shift() ?? "transcript_changed");
      };
    });
  }

  close(): void {
    this.watcher?.close();
    this.watcher = null;
  }
}
