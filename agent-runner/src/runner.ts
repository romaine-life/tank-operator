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
import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";
import { CosmosSink, isCanonical, type RunnerEvent } from "./cosmos.js";
import { extractWakeup, type WakeupRequest } from "./wakeup.js";
import { WSFanout, type ClientFrame } from "./ws.js";

// Pull a single dispatch out as a free function so the producer
// contract — cosmos-first ordering + cosmos-failure suppresses ws — is
// testable without spinning up a Runner (the WSFanout binds to a port
// in its constructor, which makes the full Runner painful to unit-test).
//
// Returns true on a successful end-to-end dispatch; false when the
// canonical write failed and the broadcast was intentionally skipped.
// Callers that don't care about the outcome can ignore it.
interface DispatchSink {
  upsert(message: RunnerEvent & { uuid: string }): Promise<void>;
}
interface DispatchWS {
  broadcastEvent(message: RunnerEvent): void;
}

let tankEventSeq = 0;

function stampTankEvent(message: RunnerEvent): RunnerEvent & { uuid: string } {
  tankEventSeq += 1;
  const now = Date.now();
  const uuid = typeof message.uuid === "string" && message.uuid ? message.uuid : randomUUID();
  return {
    ...message,
    uuid,
    tank_event_seq: tankEventSeq,
    tank_order_key: [
      String(now).padStart(13, "0"),
      String(tankEventSeq).padStart(8, "0"),
      uuid,
    ].join("-"),
    written_at: new Date(now).toISOString(),
  };
}

export async function dispatch(
  sink: DispatchSink,
  ws: DispatchWS,
  message: RunnerEvent,
): Promise<boolean> {
  const stamped = stampTankEvent(message);
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

function userMessageText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (
          part &&
          typeof part === "object" &&
          "type" in part &&
          (part as { type?: unknown }).type === "text" &&
          "text" in part
        ) {
          return String((part as { text?: unknown }).text ?? "");
        }
        return "";
      })
      .filter(Boolean)
      .join("\n");
  }
  return String(content ?? "");
}

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
  private userFrameChain: Promise<void> = Promise.resolve();

  constructor(private readonly cfg: Config) {
    this.sink = new CosmosSink(cfg);
    this.ws = new WSFanout(cfg.wsPort);
    this.ws.onMessage((frame) => {
      if (frame.type === "user") {
        this.userFrameChain = this.userFrameChain
          .then(() => this.handleClientFrame(frame))
          .catch((err) => console.error("user frame handling failed:", err));
        return;
      }
      void this.handleClientFrame(frame);
    });
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
    // 1+2. Dual-sink dispatch (cosmos-first ordering). Extracted so the
    // contract — canonical: cosmos before ws, ws skipped on cosmos
    // failure; live-only: ws only — can be tested without spinning up a
    // Runner.
    await dispatch(this.sink, this.ws, message);

    // 3. ScheduleWakeup detection
    const wakeup = extractWakeup(message);
    if (wakeup) {
      this.scheduleWakeup(wakeup);
    }
  }

  private async handleClientFrame(frame: ClientFrame): Promise<void> {
    if (frame.type === "user") {
      const text = userMessageText(frame.message?.content);
      const persisted = await dispatch(this.sink, this.ws, {
        type: "tank.user_message",
        message: text,
        ...(frame.client_nonce ? { client_nonce: frame.client_nonce } : {}),
      });
      if (!persisted) return;

      // Hand to the SDK iterator only after Tank owns the durable user
      // message. The SDK enforces "wait for result before next message"
      // internally; we don't need to gate.
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
      void this.enqueueWakeup(req).catch((err) =>
        console.error("schedule wakeup failed:", err),
      );
    }, req.delayMs);
  }

  private async enqueueWakeup(req: WakeupRequest): Promise<void> {
    const persisted = await dispatch(this.sink, this.ws, {
      type: "tank.user_message",
      message: req.prompt,
      source: "schedule_wakeup",
    });
    if (!persisted) return;
    this.userQueue.push({
      type: "user",
      session_id: "",
      message: { role: "user", content: req.prompt },
      parent_tool_use_id: null,
    } as unknown as SDKUserMessage);
  }
}
