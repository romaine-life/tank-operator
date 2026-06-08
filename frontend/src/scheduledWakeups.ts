export type ScheduledWakeupStatus = "scheduled" | "claiming" | "fired" | "failed" | "cancelled";

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
