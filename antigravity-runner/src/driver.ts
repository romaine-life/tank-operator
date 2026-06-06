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
// Only this turn's steps are emitted: the driver snapshots each transcript's
// line count before spawning agy and only forwards lines beyond that baseline,
// so a cumulative transcript (--continue appending) does not re-emit prior
// turns. The adapter additionally dedupes by step_index, so a re-read mid-write
// is harmless.

import { spawn, type ChildProcess } from "node:child_process";
import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";

import type { AgyStep } from "./adapters/antigravity.js";

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

const TRANSCRIPT_TAIL_INTERVAL_MS = 250;

export class AgyDriver {
  private child: ChildProcess | null = null;
  private killed = false;

  constructor(
    private readonly agyHome: string,
    private readonly agyBin: string = "agy",
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
    const baseline = await this.snapshotTranscripts();
    const cursors = new Map<string, number>(baseline);

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
    });
    child.stderr?.on("data", (d: Buffer) => {
      stderr += d.toString();
    });

    const onAbort = () => this.interrupt();
    signal.addEventListener("abort", onAbort, { once: true });

    const exitPromise = new Promise<{ code: number | null }>((resolve) => {
      child.once("exit", (code) => resolve({ code }));
      child.once("error", () => resolve({ code: null }));
    });

    // Tail the active transcript until agy exits, then do one final pass to
    // catch records written between the last poll and exit.
    let exited = false;
    exitPromise.then(() => {
      exited = true;
    });
    while (!exited) {
      await this.drain(cursors, baseline, onStep);
      await sleep(TRANSCRIPT_TAIL_INTERVAL_MS);
    }
    const { code } = await exitPromise;
    await this.drain(cursors, baseline, onStep);

    signal.removeEventListener("abort", onAbort);
    this.child = null;
    return { exitCode: code, killed: this.killed, stdout, stderr };
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

  // Read every transcript that has grown past its cursor and forward the new,
  // fully-written steps in order.
  private async drain(
    cursors: Map<string, number>,
    baseline: Map<string, number>,
    onStep: (step: AgyStep) => Promise<void> | void,
  ): Promise<void> {
    const files = await this.transcriptFiles();
    // Sort by mtime so the active conversation's steps emit before any stale one.
    for (const file of files) {
      let text: string;
      try {
        text = await readFile(file, "utf8");
      } catch {
        continue;
      }
      const lines = text.split("\n");
      // Drop the final split element either way: after a trailing newline it is
      // "" (empty), and without one it is a partially-written record. Only
      // complete lines are parsed; a partial line is re-read on the next poll.
      const completeCount = lines.length - 1;
      const start = cursors.get(file) ?? baseline.get(file) ?? 0;
      for (let i = start; i < completeCount; i++) {
        const line = lines[i]?.trim();
        if (!line) continue;
        let step: AgyStep;
        try {
          step = JSON.parse(line) as AgyStep;
        } catch {
          // Partial/garbled line; stop here and re-read next poll.
          cursors.set(file, i);
          break;
        }
        await onStep(step);
        cursors.set(file, i + 1);
      }
    }
  }

  private async snapshotTranscripts(): Promise<Map<string, number>> {
    const baseline = new Map<string, number>();
    for (const file of await this.transcriptFiles()) {
      try {
        const text = await readFile(file, "utf8");
        baseline.set(file, text.split("\n").filter((l) => l.trim()).length);
      } catch {
        baseline.set(file, 0);
      }
    }
    return baseline;
  }

  // All transcript_full.jsonl files under the agy data dir, newest mtime last.
  private async transcriptFiles(): Promise<string[]> {
    const brain = path.join(this.agyHome, "brain");
    let convs: string[];
    try {
      convs = await readdir(brain);
    } catch {
      return [];
    }
    const found: Array<{ file: string; mtime: number }> = [];
    for (const conv of convs) {
      const file = path.join(
        brain,
        conv,
        ".system_generated",
        "logs",
        "transcript_full.jsonl",
      );
      try {
        const s = await stat(file);
        found.push({ file, mtime: s.mtimeMs });
      } catch {
        // no transcript for this conversation yet
      }
    }
    found.sort((a, b) => a.mtime - b.mtime);
    return found.map((f) => f.file);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
