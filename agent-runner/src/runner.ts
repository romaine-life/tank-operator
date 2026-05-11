// Long-lived runner — drives one claude agent process via the SDK for
// the pod's lifetime. The SDK's `query()` takes an async iterable of
// user messages, so we push to a queue as WS clients submit, and the
// SDK iterates events back out. Multi-turn coordination is implicit:
// the SDK serializes turns internally, we just keep feeding it.
//
// Output fan-out per the producer contract:
//   1. For every event, write to Cosmos FIRST (if canonical)
//   2. Then broadcast to WS clients
//   3. If the event contains a ScheduleWakeup, queue a wakeup turn
//
// On error: log and keep running. Single-turn failures shouldn't kill
// the runner; persistent failures will show up via Cosmos write errors
// and the SPA's user can refresh.

import {
  query,
  type Query,
  type SDKMessage,
  type SDKUserMessage,
  type Options,
} from "@anthropic-ai/claude-agent-sdk";

import type { Config } from "./config.js";
import { CosmosSink, isCanonical } from "./cosmos.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";
import { WSFanout, type ClientFrame } from "./ws.js";

// AsyncQueue is a one-writer-many-no-readers queue that yields each
// pushed item exactly once. The SDK consumes this as the prompt source.
class AsyncQueue<T> {
  private readonly items: T[] = [];
  private waiters: ((v: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(v: T): void {
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }

  close(): void {
    this.closed = true;
    for (const w of this.waiters) w({ value: undefined as any, done: true });
    this.waiters = [];
  }

  [Symbol.asyncIterator](): AsyncIterator<T> {
    const self = this;
    return {
      next(): Promise<IteratorResult<T>> {
        if (self.items.length > 0) {
          return Promise.resolve({ value: self.items.shift()!, done: false });
        }
        if (self.closed) {
          return Promise.resolve({ value: undefined as any, done: true });
        }
        return new Promise((resolve) => self.waiters.push(resolve));
      },
    };
  }
}

export class Runner {
  private readonly sink: CosmosSink;
  private readonly ws: WSFanout;
  private readonly userQueue = new AsyncQueue<SDKUserMessage>();
  private sdkQuery: Query | null = null;

  constructor(private readonly cfg: Config) {
    this.sink = new CosmosSink(cfg);
    this.ws = new WSFanout(cfg.wsPort);
    this.ws.onMessage((frame) => this.handleClientFrame(frame));
  }

  // Run forever (or until externally aborted). Drives the SDK against
  // the user queue and fans events out to both sinks.
  async run(signal: AbortSignal): Promise<void> {
    const options: Options = {
      cwd: this.cfg.workspace,
      // The api-proxy injects OAuth from KV when the placeholder bearer
      // is seen — both the SDK and the raw CLI go through this path.
      // Permission bypass matches what k8s/session-config/headless-run.sh
      // used to set via --dangerously-skip-permissions.
      permissionMode: "bypassPermissions",
      // Resume an on-disk JSONL if one exists from a prior process
      // life (e.g., agent-runner restart within the same pod).
      // First boot with no JSONL: no-op.
      continue: true,
      // include_partial_messages keeps the typewriter effect — the SPA
      // renders stream_event deltas live and snapshots to the canonical
      // assistant message when it arrives.
      includePartialMessages: true,
      mcpServers: undefined, // file-mounted via --mcp-config below
      // Bare mode would skip CLAUDE.md / skills / hooks; we want those.
    };

    this.sdkQuery = query({ prompt: this.userQueue, options });
    try {
      for await (const message of this.sdkQuery) {
        if (signal.aborted) break;
        await this.handleEvent(message);
      }
    } catch (err) {
      console.error("SDK query exited with error:", err);
    } finally {
      this.userQueue.close();
      this.ws.close();
    }
  }

  private async handleEvent(message: SDKMessage): Promise<void> {
    // 1. Cosmos first (durable, read-your-writes ordering)
    if (isCanonical(message)) {
      try {
        await this.sink.upsert(message);
      } catch (err) {
        console.error("cosmos upsert failed:", err);
        // Don't broadcast a live event we couldn't persist — the SPA's
        // history-replay would then disagree with what it saw live.
        return;
      }
    }
    // 2. WebSocket broadcast (live tap)
    this.ws.broadcastEvent(message);

    // 3. ScheduleWakeup detection
    const wakeup = extractWakeup(message);
    if (wakeup) {
      this.scheduleWakeup(wakeup);
    }
  }

  private handleClientFrame(frame: ClientFrame): void {
    if (frame.type === "user") {
      // Hand straight to the SDK iterator. The SDK enforces "wait for
      // result before next message" internally; we don't need to gate.
      this.userQueue.push({
        type: "user",
        session_id: "",
        message: frame.message,
        parent_tool_use_id: null,
      } as unknown as SDKUserMessage);
    } else if (frame.type === "interrupt") {
      this.sdkQuery?.interrupt();
    }
  }

  private scheduleWakeup(req: WakeupRequest): void {
    setTimeout(() => {
      this.userQueue.push({
        type: "user",
        session_id: "",
        message: { role: "user", content: req.prompt },
        parent_tool_use_id: null,
      } as unknown as SDKUserMessage);
    }, req.delayMs);
  }
}
