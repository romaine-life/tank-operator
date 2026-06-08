import { test, expect } from "vitest";

import {
  normalizeScheduledWakeupStatus,
  scheduledWakeupStatusLabel,
} from "./scheduledWakeups";

test("scheduled wakeup statuses normalize for event-projected rows", () => {
  expect(normalizeScheduledWakeupStatus("scheduled")).toBe("scheduled");
  expect(normalizeScheduledWakeupStatus("claiming")).toBe("claiming");
  expect(normalizeScheduledWakeupStatus("fired")).toBe("fired");
  expect(normalizeScheduledWakeupStatus("failed")).toBe("failed");
  expect(normalizeScheduledWakeupStatus("cancelled")).toBe("cancelled");
  expect(normalizeScheduledWakeupStatus("unknown")).toBe("scheduled");
});

test("scheduled wakeups expose compact status labels", () => {
  expect(scheduledWakeupStatusLabel("scheduled")).toBe("scheduled");
  expect(scheduledWakeupStatusLabel("claiming")).toBe("firing");
  expect(scheduledWakeupStatusLabel("fired")).toBe("fired");
  expect(scheduledWakeupStatusLabel("failed")).toBe("failed");
  expect(scheduledWakeupStatusLabel("cancelled")).toBe("cancelled");
});
