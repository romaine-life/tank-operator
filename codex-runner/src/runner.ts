// Long-lived codex runner — drives one codex thread for the pod's
// lifetime via @openai/codex-sdk. Sibling of agent-runner/src/runner.ts
// with a different inner loop shape:
//
//   claude SDK: query() iterates an AsyncIterable of user messages,
//               yielding events forever. We push to a queue; one
//               long-running iteration handles everything.
//   codex SDK:  thread.runStreamed(input) processes ONE turn and
//               returns. We pull a user message off the queue, await
//               runStreamed to completion, then loop.
//
// Multi-turn coordination is explicit: only one runStreamed in flight
// at a time. The Thread object keeps the conversation context across
// turns (codex SDK persists threads to ~/.codex/sessions, so even pod
// restart can resume via resumeThread()).
//
// Output fan-out per the producer contract:
//   1. For every event, stamp a uuid + write to Cosmos FIRST (if canonical)
//   2. Then broadcast the same bytes to WS clients
// On error: log and keep accepting new user messages. Single-turn
// failures shouldn't kill the runner.

import { Codex, type Thread } from "@openai/codex-sdk";

import type { Config } from "./config.js";
import {
  CosmosSink,
  isCanonical,
  stampEventID,
  type CodexEvent,
} from "./cosmos.js";
import { WSFanout, type ClientFrame } from "./ws.js";

// AsyncQueue — one writer, one consumer. WS frames push; the run loop
// awaits the next value. Same shape as agent-runner's queue.
class AsyncQueue<T> {
  private readonly items: T[] = [];
  private waiters: ((v: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(v: T): void {
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }

  async next(): Promise<IteratorResult<T>> {
    if (this.items.length > 0) {
      return { value: this.items.shift()!, done: false };
    }
    if (this.closed) {
      return { value: undefined as unknown as T, done: true };
    }
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  close(): void {
    this.closed = true;
    for (const w of this.waiters) {
      w({ value: undefined as unknown as T, done: true });
    }
    this.waiters = [];
  }
}

// Pull the per-event dual-sink dispatch out as a free function so the
// ordering invariant (cosmos before ws) is testable without spinning up
// a Runner. Mirrors agent-runner/src/runner.ts dispatch().
//
// Returns true on a successful end-to-end dispatch; false when the
// canonical write failed and the broadcast was intentionally skipped.
interface DispatchSink {
  upsert(event: CodexEvent & { uuid: string }): Promise<void>;
}
interface DispatchWS {
  broadcastEvent(event: unknown): void;
}
export async function dispatch(
  sink: DispatchSink,
  ws: DispatchWS,
  event: CodexEvent,
): Promise<boolean> {
  const stamped = stampEventID(event);
  if (isCanonical(stamped)) {
    try {
      await sink.upsert(stamped);
    } catch (err) {
      console.error("cosmos upsert failed:", err);
      // Don't broadcast a live event we couldn't persist — the SPA's
      // history-replay would then disagree with what it saw live.
      return false;
    }
  }
  ws.broadcastEvent(stamped);
  return true;
}

function isAbortError(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  const code = (err as { code?: unknown }).code;
  return (
    err.name === "AbortError" ||
    code === "ABORT_ERR" ||
    /operation was aborted/i.test(err.message)
  );
}

export class Runner {
  private readonly sink: CosmosSink;
  private readonly ws: WSFanout;
  private readonly userQueue = new AsyncQueue<{ text: string; clientNonce?: string }>();
  private readonly codex: Codex;
  private thread: Thread | null = null;
  private currentAbort: AbortController | null = null;
  private interruptRequested = false;
  private turnSeq = 0;

  constructor(private readonly cfg: Config) {
    this.sink = new CosmosSink(cfg);
    this.ws = new WSFanout(cfg.wsPort);
    this.ws.onMessage((frame) => this.handleClientFrame(frame));

    // Codex SDK spawns the codex CLI subprocess; the CLI reads
    // ~/.codex/auth.json (mounted from the codex-credentials secret).
    // No CODEX_API_KEY needed — subscription auth path.
    this.codex = new Codex();
  }

  // Run until externally aborted. Each iteration awaits one user
  // message, runs one turn, drains its events. The thread persists
  // across iterations so codex sees the full conversation context.
  async run(signal: AbortSignal): Promise<void> {
    this.thread = this.codex.startThread({
      workingDirectory: this.cfg.workspace,
      // /workspace inside session pods isn't a git repo (and may never be —
      // users mount projects ad hoc). Without this flag the CLI exits with
      // "Not inside a trusted directory and --skip-git-repo-check was not
      // specified." Same flag legacy headless-run.sh has always passed.
      skipGitRepoCheck: true,
      sandboxMode: "danger-full-access",
      approvalPolicy: "never",
    });
    while (!signal.aborted) {
      const next = await this.userQueue.next();
      if (next.done) break;
      const { text: input, clientNonce } = next.value;
      const turnSeq = ++this.turnSeq;
      const persisted = await dispatch(this.sink, this.ws, {
        type: "tank.user_message",
        message: input,
        tank_turn_seq: turnSeq,
        ...(clientNonce ? { client_nonce: clientNonce } : {}),
      });
      if (!persisted) continue;

      this.currentAbort = new AbortController();
      // If the outer signal aborts mid-turn, also abort the in-flight
      // codex subprocess. AbortSignal.any-style propagation done manually
      // since Node 20's AbortSignal.any is stage 3.
      const onOuterAbort = () => this.currentAbort?.abort();
      signal.addEventListener("abort", onOuterAbort, { once: true });

      try {
        const streamed = await this.thread.runStreamed(input, {
          signal: this.currentAbort.signal,
        });
        for await (const event of streamed.events) {
          if (signal.aborted) break;
          await dispatch(this.sink, this.ws, {
            ...(event as CodexEvent),
            tank_turn_seq: turnSeq,
          });
        }
      } catch (err) {
        const interrupted = this.currentAbort.signal.aborted && isAbortError(err);
        if (interrupted) {
          if (!signal.aborted || this.interruptRequested) {
            await dispatch(this.sink, this.ws, {
              type: "turn.interrupted",
              reason: this.interruptRequested ? "client_interrupt" : "runner_shutdown",
              tank_turn_seq: turnSeq,
            });
          }
          console.info("codex turn interrupted");
          continue;
        }
        // Synthetic error event so the SPA sees something when the SDK
        // throws (e.g., process exit, network failure, quota error that
        // surfaced as an exception rather than a turn.failed).
        const errMessage =
          err instanceof Error ? err.message : String(err);
        await dispatch(this.sink, this.ws, {
          type: "error",
          message: errMessage,
          tank_turn_seq: turnSeq,
        });
        console.error("codex turn failed:", err);
      } finally {
        signal.removeEventListener("abort", onOuterAbort);
        this.currentAbort = null;
        this.interruptRequested = false;
      }
    }
    this.userQueue.close();
    this.ws.close();
  }

  private handleClientFrame(frame: ClientFrame): void {
    if (frame.type === "user") {
      // Codex SDK takes the user input as a plain string for v1. The
      // chat pane sends `{message: {role: "user", content: string}}` for
      // wire-compat with the claude path; we unwrap. Future: support
      // structured content (images, etc.) when codex SDK does.
      const content = frame.message?.content;
      const text =
        typeof content === "string"
          ? content
          : Array.isArray(content)
            ? content
                .map((c: unknown) =>
                  typeof c === "object" && c !== null && "text" in c
                    ? String((c as { text?: unknown }).text ?? "")
                    : "",
                )
                .join("\n")
            : String(content ?? "");
      if (text.trim()) {
        this.userQueue.push({ text, clientNonce: frame.client_nonce });
      }
    } else if (frame.type === "interrupt") {
      this.interruptRequested = true;
      this.currentAbort?.abort();
    }
  }
}
