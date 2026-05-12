export type ConversationActivityStatus =
  | "ready"
  | "submitted"
  | "streaming"
  | "needs_input"
  | "stopped"
  | "error";

export interface SessionActivitySummary {
  session_id: string;
  status: ConversationActivityStatus;
  last_order_key: string | null;
  unread_count: number;
  needs_input: boolean;
  failed: boolean;
  active_turn_id: string | null;
  updated_at: string | null;
}

export interface SessionActivityChip {
  key: "state" | "unread";
  label: string;
  title: string;
  tone: "failed" | "input" | "running" | "stopped" | "unread";
}

export function normalizeSessionActivity(value: unknown): SessionActivitySummary | null {
  if (!isRecord(value)) return null;
  const sessionId = stringField(value, "session_id");
  if (!sessionId) return null;
  const status = activityStatus(stringField(value, "status")) ?? "ready";
  return {
    session_id: sessionId,
    status,
    last_order_key: nullableStringField(value, "last_order_key"),
    unread_count: nonNegativeInt(value.unread_count),
    needs_input: value.needs_input === true,
    failed: value.failed === true,
    active_turn_id: nullableStringField(value, "active_turn_id"),
    updated_at: nullableStringField(value, "updated_at"),
  };
}

export function sessionActivityDotStatus(
  sessionStatus: string,
  isHeadless: boolean,
  activity?: SessionActivitySummary,
): string {
  if (!isHeadless || sessionStatus !== "Active") return sessionStatus.toLowerCase();
  if (activity?.failed || activity?.status === "error") return "agent-error";
  if (activity?.needs_input || activity?.status === "needs_input") return "agent-needs-input";
  if (activity?.status === "submitted" || activity?.status === "streaming") {
    return "agent-working";
  }
  return "agent-waiting";
}

export function sessionActivityStatusLabel(
  sessionStatus: string,
  isHeadless: boolean,
  activity?: SessionActivitySummary,
): string {
  if (!isHeadless || sessionStatus !== "Active") return sessionStatus;
  if (activity?.failed || activity?.status === "error") return "Failed";
  if (activity?.needs_input || activity?.status === "needs_input") return "Needs input";
  if (activity?.status === "submitted") return "Submitted";
  if (activity?.status === "streaming") return "Running";
  if (activity?.status === "stopped") return "Stopped";
  return "Waiting";
}

export function sessionActivityChips(
  activity?: SessionActivitySummary,
): SessionActivityChip[] {
  if (!activity) return [];
  const chips: SessionActivityChip[] = [];
  if (activity.failed || activity.status === "error") {
    chips.push({ key: "state", label: "failed", title: "Failed", tone: "failed" });
  } else if (activity.needs_input || activity.status === "needs_input") {
    chips.push({ key: "state", label: "input", title: "Needs input", tone: "input" });
  } else if (activity.status === "submitted" || activity.status === "streaming") {
    chips.push({ key: "state", label: "running", title: "Running", tone: "running" });
  } else if (activity.status === "stopped") {
    chips.push({ key: "state", label: "stopped", title: "Stopped", tone: "stopped" });
  }

  if (activity.unread_count > 0) {
    const capped = activity.unread_count > 99 ? "99+" : String(activity.unread_count);
    chips.push({
      key: "unread",
      label: `${capped} new`,
      title: `${activity.unread_count} unread update${activity.unread_count === 1 ? "" : "s"}`,
      tone: "unread",
    });
  }
  return chips;
}

function activityStatus(value: string | null): ConversationActivityStatus | null {
  switch (value) {
    case "ready":
    case "submitted":
    case "streaming":
    case "needs_input":
    case "stopped":
    case "error":
      return value;
    default:
      return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function nullableStringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function nonNegativeInt(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return 0;
  return Math.max(0, Math.trunc(value));
}
