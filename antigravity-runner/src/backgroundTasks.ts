import { watch, type FSWatcher } from "node:fs";
import {
  lstat,
  readdir,
  readFile,
  readlink,
} from "node:fs/promises";
import path from "node:path";

import type { AgyStep } from "./adapters/antigravity.js";

export interface AntigravityBackgroundTask {
  rawTaskID: string;
  safeTaskID: string;
  conversationID: string;
  stepIndex: number;
  command: string;
  cwd: string;
  summary: string;
  action: string;
  logPath: string;
  startedAt: string;
  pids: number[];
  completed: boolean;
  wakeRegistered: boolean;
  terminalStatus?: string;
  terminalSummary?: string;
  terminalError?: string;
  logFingerprint?: string;
  terminalProbeCount: number;
}

export interface BackgroundTaskWakePayload {
  taskID: string;
  status: string;
  description: string;
  summary: string;
  lastToolName: string;
  error?: string;
}

export interface AntigravityBackgroundTaskObserver {
  record?: (kind: string) => void;
}

export class AntigravityBackgroundTaskTracker {
  private readonly tasks = new Map<string, AntigravityBackgroundTask>();
  private readonly registered = new Set<string>();
  private watcher: FSWatcher | null = null;
  private monitorTimer: NodeJS.Timeout | null = null;
  private wakeHandler: ((payload: BackgroundTaskWakePayload) => Promise<boolean>) | null =
    null;
  private providerExited = false;

  constructor(
    private readonly agyHome: string,
    private readonly observer: AntigravityBackgroundTaskObserver = {},
  ) {}

  start(wakeHandler: (payload: BackgroundTaskWakePayload) => Promise<boolean>): void {
    this.wakeHandler = wakeHandler;
    this.startMessageWatcher();
  }

  close(): void {
    this.watcher?.close();
    this.watcher = null;
    if (this.monitorTimer) clearInterval(this.monitorTimer);
    this.monitorTimer = null;
  }

  async observeStep(step: AgyStep): Promise<void> {
    const started = extractRunningTask(step);
    if (started) {
      const existing = this.tasks.get(started.rawTaskID);
      if (!existing) {
        this.tasks.set(started.rawTaskID, started);
        this.observer.record?.("started");
      }
      void this.attachPids(started.rawTaskID);
    }

    const completed = extractCompletedTaskFromStep(step);
    if (completed) {
      await this.markCompleted(completed.rawTaskID, completed);
    }
  }

  /**
   * Called when agy -p exits. Provider-local completion messages are no longer
   * reliable after this boundary, so any still-active task becomes Tank-owned
   * work: watch its process/log state and register a durable wake on terminal.
   */
  async adoptAfterProviderExit(): Promise<void> {
    this.providerExited = true;
    await this.scanMessageFiles();
    for (const task of this.tasks.values()) {
      if (task.completed) continue;
      await this.attachPids(task.rawTaskID);
      if (await this.taskLooksTerminal(task)) {
        await this.registerWake(task, "terminal_observed");
      }
    }
    if (this.hasPendingAdoptedTasks()) this.ensureMonitor();
  }

  activeTaskCount(): number {
    let count = 0;
    for (const task of this.tasks.values()) {
      if (!task.completed && !task.wakeRegistered) count++;
    }
    return count;
  }

  getTask(rawTaskID: string): AntigravityBackgroundTask | undefined {
    return this.tasks.get(rawTaskID);
  }

  private async attachPids(rawTaskID: string): Promise<void> {
    const task = this.tasks.get(rawTaskID);
    if (!task || task.completed) return;
    const pids = await findTaskPids(task);
    if (pids.length > 0) {
      task.pids = Array.from(new Set([...task.pids, ...pids])).sort(
        (a, b) => a - b,
      );
      this.observer.record?.("pid_attached");
    }
  }

  private startMessageWatcher(): void {
    if (this.watcher) return;
    try {
      this.watcher = watch(this.agyHome, { recursive: true }, (_event, name) => {
        if (String(name ?? "").includes(".system_generated/messages/")) {
          void this.scanMessageFiles();
        }
      });
      this.watcher.on("error", () => {
        this.observer.record?.("message_watch_error");
      });
    } catch {
      this.observer.record?.("message_watch_unavailable");
    }
  }

  private async scanMessageFiles(): Promise<void> {
    const dirs = await findMessageDirs(this.agyHome);
    for (const dir of dirs) {
      let entries: string[];
      try {
        entries = await readdir(dir);
      } catch {
        continue;
      }
      for (const entry of entries) {
        if (!entry.endsWith(".json")) continue;
        if (entry === "read.json" || entry === "cursor.json") continue;
        const file = path.join(dir, entry);
        try {
          const msg = JSON.parse(await readFile(file, "utf8")) as unknown;
          const completed = extractCompletedTaskFromMessage(msg);
          if (completed) await this.markCompleted(completed.rawTaskID, completed);
        } catch {
          this.observer.record?.("message_parse_failed");
        }
      }
    }
  }

  private async markCompleted(
    rawTaskID: string,
    completed: {
      rawTaskID: string;
      status: string;
      summary: string;
      error?: string;
    },
  ): Promise<void> {
    const task = this.tasks.get(rawTaskID);
    if (!task) return;
    task.completed = true;
    task.terminalStatus = completed.status;
    task.terminalSummary = completed.summary;
    task.terminalError = completed.error;
    this.observer.record?.("provider_completed");
    if (this.providerExited) {
      await this.registerWake(task, completed.status);
    }
  }

  private ensureMonitor(): void {
    if (this.monitorTimer) return;
    this.monitorTimer = setInterval(() => {
      void this.monitorOnce();
    }, 2_000);
    this.monitorTimer.unref();
  }

  private async monitorOnce(): Promise<void> {
    await this.scanMessageFiles();
    let pending = false;
    for (const task of this.tasks.values()) {
      if (task.completed || task.wakeRegistered) continue;
      pending = true;
      await this.attachPids(task.rawTaskID);
      if (await this.taskLooksTerminal(task)) {
        await this.registerWake(task, "terminal_observed");
      }
    }
    if (!pending && this.monitorTimer) {
      clearInterval(this.monitorTimer);
      this.monitorTimer = null;
    }
  }

  private async taskLooksTerminal(
    task: AntigravityBackgroundTask,
  ): Promise<boolean> {
    if (task.pids.length === 0) {
      // If the process already disappeared before we could attach, the task is
      // still no longer provider-owned after agy exits. Require a stable log
      // observation before waking so a still-running detached process is not
      // resumed just because its log file exists.
      return taskLogQuiesced(task);
    }
    const states = await Promise.all(task.pids.map((pid) => procState(pid)));
    return states.every((state) => state === null || state === "Z");
  }

  private async registerWake(
    task: AntigravityBackgroundTask,
    status: string,
  ): Promise<void> {
    if (!this.wakeHandler || task.wakeRegistered || this.registered.has(task.rawTaskID))
      return;
    const payload: BackgroundTaskWakePayload = {
      taskID: task.safeTaskID,
      status,
      description: task.summary || task.action || task.command || task.rawTaskID,
      summary: [
        `Antigravity background command finished after agy exited.`,
        `Raw task id: ${task.rawTaskID}`,
        task.command ? `Command: ${task.command}` : "",
        task.cwd ? `Cwd: ${task.cwd}` : "",
        task.logPath ? `Log: ${task.logPath}` : "",
      ]
        .filter(Boolean)
        .join("\n"),
      lastToolName: "run_command",
      error: task.terminalError,
    };
    const ok = await this.wakeHandler(payload);
    this.registered.add(task.rawTaskID);
    task.wakeRegistered = true;
    this.observer.record?.(ok ? "wake_registered" : "wake_disabled");
  }

  private hasPendingAdoptedTasks(): boolean {
    for (const task of this.tasks.values()) {
      if (!task.completed && !task.wakeRegistered) return true;
    }
    return false;
  }
}

export function extractRunningTask(
  step: AgyStep,
): AntigravityBackgroundTask | null {
  if (
    upper(step.source) !== "MODEL" ||
    upper(step.type) !== "RUN_COMMAND" ||
    upper(step.status) !== "RUNNING"
  ) {
    return null;
  }
  const content = typeof step.content === "string" ? step.content : "";
  const rawTaskID = matchLine(content, /task id:\s*([^\s]+)/i);
  const logURI = matchLine(content, /Task logs are available at:\s*(\S+)/i);
  if (!rawTaskID || !logURI) return null;
  const toolCall = Array.isArray(step.tool_calls) ? step.tool_calls[0] : null;
  const args = (toolCall?.args ?? {}) as Record<string, unknown>;
  const command = stringArg(args, "CommandLine") || taskDescription(content);
  const cwd = stringArg(args, "Cwd");
  const summary = stringArg(args, "toolSummary");
  const action = stringArg(args, "toolAction");
  const conversationID =
    step.conversation_id || rawTaskID.split("/")[0] || "";
  return {
    rawTaskID,
    safeTaskID: safeTaskID(rawTaskID),
    conversationID,
    stepIndex: step.step_index,
    command,
    cwd,
    summary,
    action,
    logPath: filePathFromURI(logURI),
    startedAt: step.created_at || new Date().toISOString(),
    pids: [],
    completed: false,
    wakeRegistered: false,
    terminalProbeCount: 0,
  };
}

export function extractCompletedTaskFromStep(step: AgyStep): {
  rawTaskID: string;
  status: string;
  summary: string;
  error?: string;
} | null {
  if (upper(step.source) !== "SYSTEM") return null;
  return extractCompletedTaskText(typeof step.content === "string" ? step.content : "");
}

export function extractCompletedTaskFromMessage(msg: unknown): {
  rawTaskID: string;
  status: string;
  summary: string;
  error?: string;
} | null {
  if (!msg || typeof msg !== "object") return null;
  const content = (msg as { content?: unknown }).content;
  return extractCompletedTaskText(typeof content === "string" ? content : "");
}

function extractCompletedTaskText(text: string): {
  rawTaskID: string;
  status: string;
  summary: string;
  error?: string;
} | null {
  const rawTaskID = matchLine(text, /Task id "([^"]+)" finished with result:/i);
  if (!rawTaskID) return null;
  const lower = text.toLowerCase();
  const failed =
    lower.includes("command failed") ||
    lower.includes("exited with") ||
    lower.includes("iserror\":true");
  return {
    rawTaskID,
    status: failed ? "failed" : "completed",
    summary: text.slice(0, 1200),
    error: failed ? text.slice(0, 500) : undefined,
  };
}

async function findTaskPids(task: AntigravityBackgroundTask): Promise<number[]> {
  let entries: string[];
  try {
    entries = await readdir("/proc");
  } catch {
    return [];
  }
  const out: number[] = [];
  for (const entry of entries) {
    if (!/^\d+$/.test(entry)) continue;
    const pid = Number(entry);
    if (pid <= 1) continue;
    if (await pidHasLogFD(pid, task.logPath)) {
      out.push(pid);
      continue;
    }
    if (await pidLooksLikeTask(pid, task)) out.push(pid);
  }
  return out;
}

async function pidHasLogFD(pid: number, logPath: string): Promise<boolean> {
  if (!logPath) return false;
  const fdDir = `/proc/${pid}/fd`;
  let fds: string[];
  try {
    fds = await readdir(fdDir);
  } catch {
    return false;
  }
  for (const fd of fds) {
    try {
      const target = await readlink(path.join(fdDir, fd));
      if (target === logPath || target.startsWith(`${logPath} `)) return true;
    } catch {
      // Process may exit while scanning.
    }
  }
  return false;
}

async function pidLooksLikeTask(
  pid: number,
  task: AntigravityBackgroundTask,
): Promise<boolean> {
  if (!task.command) return false;
  const [cmdline, cwd] = await Promise.all([
    readFile(`/proc/${pid}/cmdline`, "utf8").catch(() => ""),
    readlink(`/proc/${pid}/cwd`).catch(() => ""),
  ]);
  if (task.cwd && cwd && path.resolve(cwd) !== path.resolve(task.cwd)) return false;
  const normalized = cmdline.replace(/\0/g, " ");
  const needle = task.command.split(/\s+/).filter(Boolean).slice(0, 3).join(" ");
  return Boolean(needle && normalized.includes(needle));
}

async function procState(pid: number): Promise<string | null> {
  try {
    const stat = await readFile(`/proc/${pid}/stat`, "utf8");
    return stat.split(/\s+/)[2] || null;
  } catch {
    return null;
  }
}

async function taskLogQuiesced(
  task: AntigravityBackgroundTask,
): Promise<boolean> {
  const fingerprint = await taskLogFingerprint(task.logPath);
  if (!fingerprint) return false;
  if (task.logFingerprint === fingerprint) {
    task.terminalProbeCount += 1;
  } else {
    task.logFingerprint = fingerprint;
    task.terminalProbeCount = 1;
  }
  return task.terminalProbeCount >= 2;
}

async function taskLogFingerprint(logPath: string): Promise<string> {
  if (!logPath) return "";
  try {
    const stat = await lstat(logPath);
    return `${stat.size}:${stat.mtimeMs}`;
  } catch {
    return "";
  }
}

async function findMessageDirs(agyHome: string): Promise<string[]> {
  const brain = path.join(agyHome, "brain");
  let convs: string[];
  try {
    convs = await readdir(brain);
  } catch {
    return [];
  }
  const dirs: string[] = [];
  for (const conv of convs) {
    const dir = path.join(brain, conv, ".system_generated", "messages");
    try {
      if ((await lstat(dir)).isDirectory()) dirs.push(dir);
    } catch {
      // no messages yet
    }
  }
  return dirs;
}

function safeTaskID(rawTaskID: string): string {
  const clean = rawTaskID.replace(/[^A-Za-z0-9._:-]+/g, "-").slice(0, 140);
  return clean || "antigravity-task";
}

function filePathFromURI(uri: string): string {
  if (uri.startsWith("file://")) return decodeURIComponent(new URL(uri).pathname);
  return uri;
}

function taskDescription(content: string): string {
  const marker = "Task Description:";
  const idx = content.indexOf(marker);
  if (idx < 0) return "";
  const rest = content.slice(idx + marker.length);
  const logIdx = rest.indexOf("Task logs are available at:");
  return (logIdx >= 0 ? rest.slice(0, logIdx) : rest).trim();
}

function stringArg(args: Record<string, unknown>, key: string): string {
  const value = args[key];
  return typeof value === "string" ? value.trim() : "";
}

function matchLine(text: string, pattern: RegExp): string {
  return pattern.exec(text)?.[1]?.trim() ?? "";
}

function upper(value: string | undefined): string {
  return (value ?? "").trim().toUpperCase();
}
