import assert from "node:assert/strict";
import { test } from "node:test";

import {
  scheduledWakeupRowsToEntries,
  scheduledWakeupStatusLabel,
} from "./scheduledWakeups";

test("scheduled wakeups map to background entries with visible due state", () => {
  const entries = scheduledWakeupRowsToEntries([{
    wakeup_id: "wakeup_123",
    status: "scheduled",
    prompt: "check CI",
    scheduled_at: "2026-06-03T15:20:00Z",
    due_at: "2026-06-03T15:25:00Z",
    provider_item_id: "toolu_123",
    scheduled_turn_id: "turn_1",
  }]);

  assert.equal(entries.length, 1);
  assert.equal(entries[0]!.kind, "background_task");
  assert.equal(entries[0]!.taskKind, "scheduled_wakeup");
  assert.equal(entries[0]!.taskStatus, "running");
  assert.equal(entries[0]!.taskSummary, "Scheduled continuation");
  assert.equal(entries[0]!.taskCommand, "check CI");
  assert.equal(entries[0]!.wakeupDueAt, "2026-06-03T15:25:00Z");
  assert.equal(entries[0]!.providerItemId, "toolu_123");
  assert.equal(entries[0]!.turnId, "turn_1");
});

test("terminal scheduled wakeups expose fired and failed status labels", () => {
  const [fired, failed] = scheduledWakeupRowsToEntries([
    {
      wakeup_id: "wakeup_fired",
      status: "fired",
      prompt: "resume",
      fired_turn_id: "turn_schedule_wakeup",
    },
    {
      wakeup_id: "wakeup_failed",
      status: "failed",
      prompt: "resume",
      last_error: "session_not_active",
    },
  ]);

  assert.equal(fired!.taskStatus, "completed");
  assert.equal(fired!.wakeupFiredTurnId, "turn_schedule_wakeup");
  assert.equal(scheduledWakeupStatusLabel(fired!.wakeupStatus), "fired");
  assert.equal(failed!.taskStatus, "failed");
  assert.equal(failed!.taskError, "session_not_active");
  assert.equal(scheduledWakeupStatusLabel(failed!.wakeupStatus), "failed");
});
