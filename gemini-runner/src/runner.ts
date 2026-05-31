import { GoogleGenAI, Type } from "@google/genai";
import { exec } from "node:child_process";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { randomUUID } from "node:crypto";


import type { Config } from "./config.js";
import { SessionEventSink } from "./sessionEvents.js";
import {
  SessionCommandBus,
  commandClientNonce,
  type SessionCommandRecord,
} from "./sessionCommands.js";
import { GeminiTankEventAdapter } from "./adapters/gemini.js";
import {
  commandsConsumedTotal,
  providerErrorTotal,
  recordTurnStart,
  recordTurnTerminal,
} from "./metrics.js";

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
  private readonly ai: GoogleGenAI;
  private chat: any = null;
  private currentAbort: AbortController | null = null;
  private turnSeq = 0;

  constructor(private readonly cfg: Config) {
    this.sink = new SessionEventSink(cfg);
    this.commandBus = new SessionCommandBus(cfg);
    this.adapter = new GeminiTankEventAdapter(cfg);

    this.ai = new GoogleGenAI({
      apiKey: "managed-by-tank-operator",
      httpOptions: {
        headers: {
          "Authorization": "Bearer managed-by-tank-operator",
        },
      },
    });
  }

  async run(signal: AbortSignal): Promise<void> {
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
        const turnSeq = ++this.turnSeq;
        const clientNonceStr = clientNonce ?? `gen-${randomUUID()}`;
        const turnID = `turn-${turnSeq}`;
        const turn = { turnID, clientNonce: clientNonceStr };

        recordTurnStart(turnID);

        // Emit turn.started
        await this.sink.upsert(this.adapter.turnStarted(turn) as any);

        this.currentAbort = new AbortController();
        const turnSignal = this.currentAbort.signal;

        try {
          // Initialize chat on first turn
          if (!this.chat) {
            const model = commandRecord?.model || "gemini-3.5-flash";
            this.chat = this.ai.chats.create({
              model: model,
              config: {
                tools: [
                  {
                    functionDeclarations: [
                      {
                        name: "execute_bash",
                        description: "Execute a bash command in the workspace directory",
                        parameters: {
                          type: Type.OBJECT,
                          properties: {
                            command: { type: Type.STRING, description: "The command to run" },
                          },
                          required: ["command"],
                        },
                      },
                      {
                        name: "read_file",
                        description: "Read the contents of a file from the workspace",
                        parameters: {
                          type: Type.OBJECT,
                          properties: {
                            path: { type: Type.STRING, description: "Path to the file relative to the workspace" },
                          },
                          required: ["path"],
                        },
                      },
                      {
                        name: "write_file",
                        description: "Write content to a file in the workspace",
                        parameters: {
                          type: Type.OBJECT,
                          properties: {
                            path: { type: Type.STRING, description: "Path to the file relative to the workspace" },
                            content: { type: Type.STRING, description: "The content to write" },
                          },
                          required: ["path", "content"],
                        },
                      },
                      {
                        name: "list_dir",
                        description: "List the contents of a directory in the workspace",
                        parameters: {
                          type: Type.OBJECT,
                          properties: {
                            path: { type: Type.STRING, description: "Path to the directory relative to the workspace" },
                          },
                          required: ["path"],
                        },
                      },
                    ],
                  },
                ],
              },
            });
          }

          let response = await this.chat.sendMessage({ message: input });

          // ReAct loop
          while (response.functionCalls && response.functionCalls.length > 0 && !turnSignal.aborted) {
            const parts: any[] = [];

            for (const call of response.functionCalls) {
              if (turnSignal.aborted) break;

              const toolItemID = `tool-${randomUUID()}`;
              await this.sink.upsert(
                this.adapter.toolStarted(turn, toolItemID, call.name, call.args) as any
              );

              try {
                const result = await this.executeTool(call.name, call.args, turnSignal);
                await this.sink.upsert(
                  this.adapter.toolCompleted(turn, toolItemID, call.name, call.args, result) as any
                );

                parts.push({
                  functionResponse: {
                    name: call.name,
                    response: { result },
                  },
                });
              } catch (err: any) {
                const errMsg = err instanceof Error ? err.message : String(err);
                await this.sink.upsert(
                  this.adapter.toolFailed(turn, toolItemID, call.name, call.args, errMsg) as any
                );

                parts.push({
                  functionResponse: {
                    name: call.name,
                    response: { error: errMsg },
                  },
                });
              }
            }

            if (parts.length > 0 && !turnSignal.aborted) {
              response = await this.chat.sendMessage({ parts });
            } else {
              break;
            }
          }

          if (turnSignal.aborted) {
            throw new Error("Turn was aborted/interrupted");
          }

          const assistantText = response.text || "";
          const assistantItemID = `msg-${randomUUID()}`;
          await this.sink.upsert(
            this.adapter.messageCompleted(turn, assistantItemID, assistantText) as any
          );

          // Emit turn.completed
          await this.sink.upsert(
            this.adapter.turnCompleted(turn, {
              timelineIDs: [assistantItemID],
              providerItemIDs: [assistantItemID],
            }) as any
          );

          if (commandRecord) {
            await this.commandBus.markCompleted(commandRecord);
          }
          recordTurnTerminal(turnID, "completed");

        } catch (err: any) {
          providerErrorTotal.labels("query").inc();
          const errMessage = err instanceof Error ? err.message : String(err);

          if (turnSignal.aborted) {
            await this.sink.upsert(this.adapter.turnInterrupted(turn) as any);
            if (commandRecord) {
              await this.commandBus.markCompleted(commandRecord);
            }
            recordTurnTerminal(turnID, "interrupted");
          } else {
            await this.sink.upsert(this.adapter.turnFailed(turn, errMessage) as any);
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

  private async executeTool(name: string, args: any, signal: AbortSignal): Promise<any> {
    if (signal.aborted) throw new Error("Aborted");

    const getAbsPath = (relPath: string) => {
      const normalized = path.normalize(relPath);
      if (path.isAbsolute(normalized)) {
        if (normalized.startsWith(this.cfg.workspace)) {
          return normalized;
        }
        throw new Error("Access outside workspace is denied");
      }
      const resolved = path.resolve(this.cfg.workspace, normalized);
      if (resolved.startsWith(this.cfg.workspace)) {
        return resolved;
      }
      throw new Error("Access outside workspace is denied");
    };

    switch (name) {
      case "execute_bash": {
        const cmd = String(args.command ?? "");
        return new Promise((resolve, reject) => {
          const process = exec(
            cmd,
            { cwd: this.cfg.workspace },
            (error, stdout, stderr) => {
              if (error) {
                resolve({
                  exitCode: error.code || 1,
                  stdout: stdout,
                  stderr: stderr || error.message,
                });
              } else {
                resolve({
                  exitCode: 0,
                  stdout,
                  stderr,
                });
              }
            }
          );
          signal.addEventListener("abort", () => {
            process.kill();
            reject(new Error("Bash process killed via abort"));
          });
        });
      }

      case "read_file": {
        const absPath = getAbsPath(String(args.path ?? ""));
        const content = await fs.readFile(absPath, "utf-8");
        return { content };
      }

      case "write_file": {
        const absPath = getAbsPath(String(args.path ?? ""));
        await fs.mkdir(path.dirname(absPath), { recursive: true });
        await fs.writeFile(absPath, String(args.content ?? ""), "utf-8");
        return { success: true };
      }

      case "list_dir": {
        const absPath = getAbsPath(String(args.path ?? "."));
        const entries = await fs.readdir(absPath, { withFileTypes: true });
        const list = entries.map((entry) => ({
          name: entry.name,
          isDirectory: entry.isDirectory(),
          isFile: entry.isFile(),
        }));
        return { list };
      }

      default:
        throw new Error(`Unknown tool: ${name}`);
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
    // Listen for interrupts or control signals (like stop_turn / interrupt_turn)
    // and abort the active turn execution
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
}
