import assert from "node:assert/strict";
import test from "node:test";

import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import {
  AntigravityBackgroundTaskTracker,
  extractCompletedTaskFromStep,
  extractRunningTask,
  type BackgroundTaskWakePayload,
} from "./backgroundTasks.js";
import type { AgyStep } from "./adapters/antigravity.js";

function runningStep(logPath: string): AgyStep {
  return {
    step_index: 468,
    source: "MODEL",
    type: "RUN_COMMAND",
    status: "RUNNING",
    created_at: "2026-06-08T04:08:05Z",
    content: [
      "Created At: 2026-06-08T04:08:05Z",
      "Tool is running as a background task with task id: f4710bec-65d4/task-468",
      "Task Description: python3 monitor_build_and_sync.py",
      `Task logs are available at: file://${logPath}`,
    ].join("\n"),
    tool_calls: [
      {
        name: "run_command",
        args: {
          CommandLine: "python3 monitor_build_and_sync.py",
          Cwd: "/workspace/tank-operator",
          WaitMsBeforeAsync: 5000,
          toolAction: "Monitoring build",
          toolSummary: "Monitor build and sync",
        },
      },
    ],
  };
}

test("extracts Antigravity RUN_COMMAND background task metadata", () => {
  const task = extractRunningTask(runningStep("/tmp/task-468.log"));
  if (!task) throw new Error("expected running task");
  const extracted = task;
  assert.equal(extracted.rawTaskID, "f4710bec-65d4/task-468");
  assert.equal(extracted.safeTaskID, "f4710bec-65d4-task-468");
  assert.equal(extracted.command, "python3 monitor_build_and_sync.py");
  assert.equal(extracted.summary, "Monitor build and sync");
  assert.equal(extracted.logPath, "/tmp/task-468.log");
});

test("extracts provider-local completion messages", () => {
  const completed = extractCompletedTaskFromStep({
    step_index: 424,
    source: "SYSTEM",
    type: "SYSTEM_MESSAGE",
    status: "DONE",
    content:
      'Task id "f4710bec-65d4/task-422" finished with result:\n\nThe command completed successfully.\nOutput:\nok',
  });
  assert.deepEqual(completed, {
    rawTaskID: "f4710bec-65d4/task-422",
    status: "completed",
    summary:
      'Task id "f4710bec-65d4/task-422" finished with result:\n\nThe command completed successfully.\nOutput:\nok',
    error: undefined,
  });
});

test("provider-completed tasks do not become Tank background wakes", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-bg-"));
  try {
    const logPath = path.join(root, "task-468.log");
    await writeFile(logPath, "done\n");
    const tracker = new AntigravityBackgroundTaskTracker(root);
    const wakes: BackgroundTaskWakePayload[] = [];
    tracker.start(async (payload) => {
      wakes.push(payload);
      return true;
    });
    await tracker.observeStep(runningStep(logPath));
    await tracker.observeStep({
      step_index: 469,
      source: "SYSTEM",
      type: "SYSTEM_MESSAGE",
      status: "DONE",
      content:
        'Task id "f4710bec-65d4/task-468" finished with result:\n\nThe command completed successfully.',
    });
    await tracker.adoptAfterProviderExit();
    assert.equal(wakes.length, 0);
    tracker.close();
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("unfinished task at provider exit registers a durable wake", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-bg-"));
  try {
    const logPath = path.join(root, "task-468.log");
    await writeFile(logPath, "Polling GitHub build run...\n");
    await mkdir(path.join(root, "brain"), { recursive: true });
    const tracker = new AntigravityBackgroundTaskTracker(root);
    const wakes: BackgroundTaskWakePayload[] = [];
    tracker.start(async (payload) => {
      wakes.push(payload);
      return true;
    });
    await tracker.observeStep(runningStep(logPath));
    await tracker.adoptAfterProviderExit();
    assert.equal(wakes.length, 0);
    await tracker.adoptAfterProviderExit();
    assert.equal(wakes.length, 1);
    assert.equal(wakes[0].taskID, "f4710bec-65d4-task-468");
    assert.equal(wakes[0].status, "terminal_observed");
    assert.match(wakes[0].summary, /Raw task id: f4710bec-65d4\/task-468/);
    assert.match(wakes[0].summary, /Log: /);
    tracker.close();
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("provider completion observed after exit registers a durable wake", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-bg-"));
  try {
    const logPath = path.join(root, "task-468.log");
    await writeFile(logPath, "done\n");
    const messageDir = path.join(
      root,
      "brain",
      "f4710bec-65d4",
      ".system_generated",
      "messages",
    );
    await mkdir(messageDir, { recursive: true });
    const tracker = new AntigravityBackgroundTaskTracker(root);
    const wakes: BackgroundTaskWakePayload[] = [];
    tracker.start(async (payload) => {
      wakes.push(payload);
      return true;
    });
    await tracker.observeStep(runningStep(logPath));
    await writeFile(
      path.join(messageDir, "completed.json"),
      JSON.stringify({
        content:
          'Task id "f4710bec-65d4/task-468" finished with result:\n\nThe command completed successfully.',
      }),
    );
    await tracker.adoptAfterProviderExit();
    assert.equal(wakes.length, 1);
    assert.equal(wakes[0].taskID, "f4710bec-65d4-task-468");
    assert.equal(wakes[0].status, "completed");
    tracker.close();
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
