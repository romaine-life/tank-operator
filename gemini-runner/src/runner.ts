import { spawn } from "node:child_process";
import * as readline from "node:readline";
import { randomUUID } from "node:crypto";

import type { Config } from "./config.js";
import { ensureGeminiSettingsFile } from "./auth/authConfig.js";
import { SessionEventSink } from "./sessionEvents.js";
import {
  SessionCommandBus,
  commandClientNonce,
  type SessionCommandRecord,
} from "./sessionCommands.js";
import { GeminiTankEventAdapter, type GeminiAdapterTurn } from "./adapters/gemini.js";
import {
  commandsConsumedTotal,
  providerErrorTotal,
  recordTurnStart,
  recordTurnTerminal,
  eventTruncatedTotal,
  natsPublishFailureTotal,
} from "./metrics.js";
import {
  isDurableTankConversationEvent,
  type TankConversationEvent,
} from "../../runner-shared/conversation.js";
import {
  stampTankEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import { truncateEventIfOversized } from "../../runner-shared/sessionBus.js";

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

export class Runner {
  private readonly sink: SessionEventSink;
  private readonly commandBus: SessionCommandBus;
  private readonly userQueue = new AsyncQueue<{
    text: string;
    clientNonce?: string;
    commandRecord?: SessionCommandRecord;
  }>();
  private readonly adapter: GeminiTankEventAdapter;
  private currentAbort: AbortController | null = null;
  private sessionExists = false;

  constructor(private readonly cfg: Config) {
    this.sink = new SessionEventSink(cfg);
    this.commandBus = new SessionCommandBus(cfg);
    this.adapter = new GeminiTankEventAdapter(cfg);
  }

  async run(signal: AbortSignal): Promise<void> {
    try {
      const settingsPath = ensureGeminiSettingsFile();
      console.log("Runner ensured settings.json is written to:", settingsPath);
    } catch (err) {
      console.error("Failed to write gemini settings file:", err);
    }

    const stopConsumer = this.startCommandConsumer(signal);
    const stopControl = this.startControlConsumer(signal);

    const onAbort = () => {
      this.userQueue.close();
      this.currentAbort?.abort();
    };
    signal.addEventListener("abort", onAbort, { once: true });

    try {
      while (!signal.aborted) {
        const next = await this.userQueue.next();
        if (next.done) break;

        const { text: input, clientNonce, commandRecord } = next.value;
        const clientNonceStr = clientNonce ?? `gen-${randomUUID()}`;
        const turnID = turnIDForClientNonce(clientNonceStr);
        const turn = { turnID, clientNonce: clientNonceStr };

        recordTurnStart(turnID);

        // Emit turn.started
        await this.dispatch(this.adapter.turnStarted(turn));

        this.currentAbort = new AbortController();
        const turnSignal = this.currentAbort.signal;

        try {
          await this.executeCliTurn(input, turn, turnSignal, commandRecord);
        } catch (err: any) {
          providerErrorTotal.labels("query").inc();
          const errMessage = err instanceof Error ? err.message : String(err);

          if (turnSignal.aborted) {
            await this.dispatch(this.adapter.turnInterrupted(turn));
            if (commandRecord) {
              await this.commandBus.markCompleted(commandRecord);
            }
            recordTurnTerminal(turnID, "interrupted");
          } else {
            await this.dispatch(this.adapter.turnFailed(turn, errMessage));
            if (commandRecord) {
              await this.commandBus.markFailed(commandRecord, err);
            }
            recordTurnTerminal(turnID, "failed");
          }
          console.error("Gemini turn failed:", err);
        } finally {
          this.currentAbort = null;
        }
      }
    } finally {
      signal.removeEventListener("abort", onAbort);
      stopConsumer();
      stopControl();
      this.userQueue.close();
    }
  }

  private async executeCliTurn(
    input: string,
    turn: GeminiAdapterTurn,
    turnSignal: AbortSignal,
    commandRecord?: SessionCommandRecord
  ): Promise<void> {
    let proc;

    if (process.platform === "win32") {
      const escapedInput = `"${input.replace(/"/g, '\\"')}"`;
      const runArgs = [
        this.sessionExists ? "--resume" : "--session-id",
        this.cfg.sessionId,
        "--skip-trust",
        "--yolo",
        "-o", "stream-json",
        "-p", escapedInput
      ];
      console.log(`Spawning on Windows: gemini.cmd ${runArgs.join(" ")}`);
      proc = spawn("gemini.cmd", runArgs, {
        cwd: this.cfg.workspace,
        env: {
          ...process.env
        },
        shell: true
      });
    } else {
      const runArgs = [
        this.sessionExists ? "--resume" : "--session-id",
        this.cfg.sessionId,
        "--skip-trust",
        "--yolo",
        "-o", "stream-json",
        "-p", input
      ];
      console.log(`Spawning on Linux: gemini ${runArgs.join(" ")}`);
      proc = spawn("gemini", runArgs, {
        cwd: this.cfg.workspace,
        env: {
          ...process.env
        },
        shell: false
      });
    }

    const exitCodePromise = new Promise<number>((resolve) => {
      proc.on("close", (code) => resolve(code ?? 0));
      proc.on("error", (err) => {
        console.error("Failed to start gemini process:", err);
        resolve(-1);
      });
    });

    const onAbort = () => {
      console.log(`Abort signal received. Terminating process PID ${proc.pid}`);
      proc.kill("SIGINT");
      setTimeout(() => {
        try {
          proc.kill("SIGKILL");
        } catch {}
      }, 2000);
    };
    turnSignal.addEventListener("abort", onAbort);

    try {
      let isResumeFailure = false;
      let isAlreadyExistsFailure = false;
      let stderrText = "";
      proc.stderr?.on("data", (chunk) => {
        stderrText += chunk.toString();
      });

      const rl = readline.createInterface({
        input: proc.stdout,
        terminal: false
      });

      let assistantText = "";
      const toolIdToInfo = new Map<string, { name: string, input: any }>();
      const timelineIDs: string[] = [];

      for await (const line of rl) {
        if (!line.trim()) continue;
        try {
          const event = JSON.parse(line.trim());
          switch (event.type) {
            case "init":
              break;
            case "message":
              if (event.role === "assistant" && event.content) {
                assistantText += event.content;
              }
              break;
            case "tool_use": {
              const toolItemID = event.tool_id || `tool-${randomUUID()}`;
              toolIdToInfo.set(toolItemID, { name: event.tool_name, input: event.parameters });
              timelineIDs.push(toolItemID);
              await this.dispatch(
                this.adapter.toolStarted(turn, toolItemID, event.tool_name, event.parameters)
              );
              break;
            }
            case "tool_result": {
              const toolItemID = event.tool_id;
              const info = toolIdToInfo.get(toolItemID);
              const name = info?.name || "unknown";
              const input = info?.input || {};
              if (event.status === "success") {
                await this.dispatch(
                  this.adapter.toolCompleted(turn, toolItemID, name, input, event.output || "")
                );
              } else {
                await this.dispatch(
                  this.adapter.toolFailed(turn, toolItemID, name, input, event.output || "Error")
                );
              }
              break;
            }
            case "result":
              break;
          }
        } catch (err) {
          if (line.includes("Error resuming session") || line.includes("Invalid session identifier")) {
            isResumeFailure = true;
          }
          if (line.includes("already exists") || line.includes("Error starting session")) {
            isAlreadyExistsFailure = true;
          }
        }
      }

      const exitCode = await exitCodePromise;

      // Symmetric fallback logic
      if (exitCode !== 0) {
        // Fallback from resume to create
        if (this.sessionExists && (isResumeFailure || stderrText.includes("Error resuming session"))) {
          console.log(`Resume failed for session ${this.cfg.sessionId}. Retrying with session creation...`);
          turnSignal.removeEventListener("abort", onAbort);
          this.sessionExists = false;
          return await this.executeCliTurn(input, turn, turnSignal, commandRecord);
        }
        // Fallback from create to resume
        if (!this.sessionExists && (isAlreadyExistsFailure || stderrText.includes("already exists") || stderrText.includes("Error starting session"))) {
          console.log(`Session ${this.cfg.sessionId} already exists. Retrying with resume...`);
          turnSignal.removeEventListener("abort", onAbort);
          this.sessionExists = true;
          return await this.executeCliTurn(input, turn, turnSignal, commandRecord);
        }

        throw new Error(`Gemini CLI exited with code ${exitCode}. Stderr: ${stderrText}`);
      }

      // Session successfully active now
      this.sessionExists = true;

      // Emit assistant message complete if text was generated
      const assistantItemID = `msg-${randomUUID()}`;
      let timelineItemID = assistantItemID;
      if (assistantText) {
        const event = this.adapter.messageCompleted(turn, assistantItemID, assistantText);
        if (event.timeline_id) {
          timelineItemID = event.timeline_id;
        }
        await this.dispatch(event);
      }

      // Emit turn.completed
      await this.dispatch(
        this.adapter.turnCompleted(turn, {
          timelineIDs: assistantText ? [timelineItemID] : [],
          providerItemIDs: assistantText ? [assistantItemID] : []
        })
      );

      if (commandRecord) {
        await this.commandBus.markCompleted(commandRecord);
      }
      recordTurnTerminal(turn.turnID, "completed");

    } finally {
      turnSignal.removeEventListener("abort", onAbort);
    }
  }

  private startCommandConsumer(signal: AbortSignal): () => void {
    let stopConsumer: (() => Promise<void>) | null = null;
    void this.commandBus
      .startCommandConsumer(async (record) => {
        commandsConsumedTotal.labels("submit_turn", "accepted").inc();
        const clientNonce = commandClientNonce(record);
        const prompt = String(record.prompt ?? "").trim();
        if (!prompt) {
          commandsConsumedTotal.labels("submit_turn", "invalid").inc();
          await this.commandBus.markFailed(record, new Error("submit command missing prompt"));
          return;
        }
        this.userQueue.push({
          text: prompt,
          clientNonce,
          commandRecord: record,
        });
      }, signal)
      .then((stop) => {
        stopConsumer = stop;
      })
      .catch((err) => console.error("Gemini command consumer crashed:", err));

    return () => {
      void stopConsumer?.();
    };
  }

  private startControlConsumer(signal: AbortSignal): () => void {
    let stopControl: (() => Promise<void>) | null = null;
    void this.commandBus
      .startControlConsumer(async (record) => {
        if (record.command === "interrupt_turn") {
          commandsConsumedTotal.labels("interrupt_turn", "accepted").inc();
          console.log("Interrupt command received, aborting active turn");
          this.currentAbort?.abort();
          await this.commandBus.markCompleted(record);
        }
      }, signal)
      .then((stop) => {
        stopControl = stop;
      })
      .catch((err) => console.error("Gemini control consumer crashed:", err));

    return () => {
      void stopControl?.();
    };
  }
  private async dispatch(message: TankConversationEvent): Promise<boolean> {
    const stamped = stampTankEvent(message);
    if (!isDurableTankConversationEvent(stamped)) {
      return true;
    }
    const sizeGuard = truncateEventIfOversized(
      stamped as unknown as Record<string, unknown>,
    );
    if (sizeGuard.truncated) {
      const severity = sizeGuard.payloadDropped ? "payload-dropped" : "strings-truncated";
      eventTruncatedTotal.labels(stamped.type, severity).inc();
      console.warn(
        "session bus event truncated:",
        JSON.stringify({
          event_type: stamped.type,
          original_bytes: sizeGuard.originalBytes,
          final_bytes: sizeGuard.finalBytes,
          fields: sizeGuard.fields,
          severity,
        }),
      );
    }
    try {
      await this.sink.upsert(sizeGuard.event as any);
    } catch (err) {
      console.error("session bus publish failed:", err);
      natsPublishFailureTotal.inc();
      return false;
    }
    return true;
  }
}
