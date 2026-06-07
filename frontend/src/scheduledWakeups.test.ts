import { test, expect } from "vitest";

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

  expect(entries.length).toBe(1);
  expect(entries[0]!.kind).toBe("background_task");
  expect(entries[0]!.taskKind).toBe("scheduled_wakeup");
  expect(entries[0]!.taskStatus).toBe("running");
  expect(entries[0]!.taskSummary).toBe("Scheduled continuation");
  expect(entries[0]!.taskCommand).toBe("check CI");
  expect(entries[0]!.wakeupDueAt).toBe("2026-06-03T15:25:00Z");
  expect(entries[0]!.providerItemId).toBe("toolu_123");
  expect(entries[0]!.turnId).toBe("turn_1");
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

  expect(fired!.taskStatus).toBe("completed");
  expect(fired!.wakeupFiredTurnId).toBe("turn_schedule_wakeup");
  expect(scheduledWakeupStatusLabel(fired!.wakeupStatus)).toBe("fired");
  expect(failed!.taskStatus).toBe("failed");
  expect(failed!.taskError).toBe("session_not_active");
  expect(scheduledWakeupStatusLabel(failed!.wakeupStatus)).toBe("failed");
});
