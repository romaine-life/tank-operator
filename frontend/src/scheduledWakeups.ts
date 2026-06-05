import type { ConversationBackgroundTaskStatus } from "./conversationReducer";

export type ScheduledWakeupStatus = "scheduled" | "claiming" | "fired" | "failed" | "cancelled";

export type ScheduledWakeupRow = {
  wakeup_id?: string;
  status?: string;
  provider?: string;
  prompt?: string;
  client_nonce?: string;
  scheduled_turn_id?: string;
  provider_item_id?: string;
  scheduled_at?: string;
  due_at?: string;
  attempt_count?: number;
  fired_turn_id?: string;
  last_error?: string;
};

export type ScheduledWakeupBackgroundEntry = {
  id: string;
  kind: "background_task";
  time: string;
  startedAt?: string;
  updatedAt?: string;
  taskKind: "scheduled_wakeup";
  taskId: string;
  taskStatus: ConversationBackgroundTaskStatus;
  taskSummary: string;
  taskDescription?: string;
  taskCommand?: string;
  taskOutput?: string;
  taskError?: string;
  taskRawItem: ScheduledWakeupRow;
  clientNonce?: string;
  providerItemId?: string;
  turnId?: string;
  wakeupStatus: ScheduledWakeupStatus;
  wakeupDueAt?: string;
  wakeupScheduledAt?: string;
  wakeupPrompt?: string;
  wakeupFiredTurnId?: string;
  wakeupLastError?: string;
  wakeupAttemptCount?: number;
};

export function normalizeScheduledWakeupStatus(status: string | undefined): ScheduledWakeupStatus {
  switch ((status ?? "").trim()) {
    case "claiming":
      return "claiming";
    case "fired":
      return "fired";
    case "failed":
      return "failed";
    case "cancelled":
      return "cancelled";
    default:
      return "scheduled";
  }
}

export function scheduledWakeupTaskStatus(status: ScheduledWakeupStatus): ConversationBackgroundTaskStatus {
  switch (status) {
    case "fired":
      return "completed";
    case "failed":
      return "failed";
    case "cancelled":
      return "stopped";
    case "claiming":
    case "scheduled":
      return "running";
  }
}

export function scheduledWakeupStatusLabel(status: ScheduledWakeupStatus): string {
  switch (status) {
    case "claiming":
      return "firing";
    case "fired":
      return "fired";
    case "failed":
      return "failed";
    case "cancelled":
      return "cancelled";
    case "scheduled":
      return "scheduled";
  }
}

function nonempty(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

export function scheduledWakeupRowsToEntries(rows: ScheduledWakeupRow[]): ScheduledWakeupBackgroundEntry[] {
  return rows.flatMap((row) => {
    const wakeupID = nonempty(row.wakeup_id);
    if (!wakeupID) return [];
    const status = normalizeScheduledWakeupStatus(row.status);
    const prompt = nonempty(row.prompt);
    const dueAt = nonempty(row.due_at);
    const scheduledAt = nonempty(row.scheduled_at);
    const firedTurnID = nonempty(row.fired_turn_id);
    const lastError = nonempty(row.last_error);
    const descriptionParts = [
      dueAt ? `Due ${dueAt}` : "",
      firedTurnID ? `Fired turn ${firedTurnID}` : "",
      lastError ? `Error ${lastError}` : "",
    ].filter(Boolean);
    return [{
      id: `scheduled-wakeup-${wakeupID}`,
      kind: "background_task",
      time: scheduledAt ?? dueAt ?? new Date(0).toISOString(),
      startedAt: scheduledAt,
      updatedAt: dueAt,
      taskKind: "scheduled_wakeup",
      taskId: wakeupID,
      taskStatus: scheduledWakeupTaskStatus(status),
      taskSummary: "Scheduled continuation",
      taskDescription: descriptionParts.join(" · ") || undefined,
      taskCommand: prompt,
      taskOutput: firedTurnID ? `Fired turn ${firedTurnID}` : undefined,
      taskError: lastError,
      taskRawItem: row,
      clientNonce: nonempty(row.client_nonce),
      providerItemId: nonempty(row.provider_item_id),
      turnId: nonempty(row.scheduled_turn_id),
      wakeupStatus: status,
      wakeupDueAt: dueAt,
      wakeupScheduledAt: scheduledAt,
      wakeupPrompt: prompt,
      wakeupFiredTurnId: firedTurnID,
      wakeupLastError: lastError,
      wakeupAttemptCount: typeof row.attempt_count === "number" ? row.attempt_count : undefined,
    }];
  });
}
