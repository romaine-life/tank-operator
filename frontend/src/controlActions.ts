import type { ConversationBackgroundTaskStatus } from "./conversationReducer";

export type ControlActionStatus = "started" | "succeeded" | "failed";

export type ControlActionRow = {
  event_id?: string;
  invocation_id?: string;
  created_at?: string;
  owner_email?: string;
  session_scope?: string;
  session_id?: string;
  source_service?: string;
  source_tool?: string;
  action?: string;
  status?: string;
  target_kind?: string;
  target_ref?: string;
  repo_owner?: string;
  repo_name?: string;
  pr_number?: number;
  result_sha?: string;
  error?: string;
  payload?: unknown;
};

export type ControlActionBackgroundEntry = {
  id: string;
  kind: "background_task";
  time: string;
  startedAt?: string;
  taskKind: "control_action";
  taskId: string;
  taskStatus: ConversationBackgroundTaskStatus;
  taskSummary: string;
  taskDescription?: string;
  taskCommand?: string;
  taskOutput?: string;
  taskError?: string;
  taskRawItem: ControlActionRow;
  controlActionStatus: ControlActionStatus;
  controlActionTool?: string;
  controlActionAction?: string;
  controlActionTarget?: string;
  controlActionRepo?: string;
  controlActionPrNumber?: number;
  controlActionSha?: string;
};

export type BreakGlassRequest = {
  eventId: string;
  invocationId: string;
  createdAt?: string;
  kind: "git" | "azure";
  target: string;
  repo?: string;
  repoOwner?: string;
  repoName?: string;
  reason?: string;
  source?: string;
};

function nonempty(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function payloadObject(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function normalizeControlActionStatus(status: string | undefined): ControlActionStatus {
  switch ((status ?? "").trim()) {
    case "succeeded":
      return "succeeded";
    case "failed":
      return "failed";
    default:
      return "started";
  }
}

function controlActionTaskStatus(status: ControlActionStatus): ConversationBackgroundTaskStatus {
  switch (status) {
    case "succeeded":
      return "completed";
    case "failed":
      return "failed";
    case "started":
      return "running";
  }
}

export function controlActionStatusLabel(status: ControlActionStatus): string {
  switch (status) {
    case "succeeded":
      return "succeeded";
    case "failed":
      return "failed";
    case "started":
      return "started";
  }
}

function actionTitle(action: string | undefined): string {
  switch (action) {
    case "github.pull_request.merge":
      return "GitHub PR merge";
    case "github.pull_request.rename":
      return "GitHub PR renamed";
    case "github.pull_request.ready_for_review":
      return "GitHub PR ready";
    case "github.pull_request.open":
      return "GitHub PR opened";
    case "github.pull_request.mergeability":
      return "GitHub PR mergeability";
    case "github.commit.push":
      return "Git push";
    case "github.commit.write":
      return "GitHub commit";
    case "github.commit.ci":
      return "GitHub CI";
    case "github.break_glass.request":
      return "GitHub break-glass request";
    case "github.break_glass.grant":
      return "GitHub break-glass grant";
    case "github.break_glass.deny":
      return "GitHub break-glass denied";
    case "github.break_glass.token":
      return "GitHub break-glass token";
    case "github.break_glass.push":
      return "GitHub break-glass push";
    case "azure.break_glass.request":
      return "Azure break-glass request";
    case "azure.break_glass.grant":
      return "Azure break-glass grant";
    case "azure.break_glass.deny":
      return "Azure break-glass denied";
    case "azure.break_glass.use":
      return "Azure break-glass use";
    case "tank.test_slot_model.request":
      return "Test-slot model request";
    case "tank.test_slot_model.grant":
      return "Test-slot model grant";
    default:
      return "Control action";
  }
}

export function controlActionRowsToEntries(rows: ControlActionRow[]): ControlActionBackgroundEntry[] {
  return rows.flatMap((row) => {
    const eventID = nonempty(row.event_id);
    const invocationID = nonempty(row.invocation_id);
    if (!eventID || !invocationID) return [];
    const status = normalizeControlActionStatus(row.status);
    const repo = [nonempty(row.repo_owner), nonempty(row.repo_name)].filter(Boolean).join("/");
    const pr = typeof row.pr_number === "number" ? `#${row.pr_number}` : "";
    const target = nonempty(row.target_ref);
    const error = nonempty(row.error);
    const sha = nonempty(row.result_sha);
    const description = [repo, pr, target].filter(Boolean).join(" ");
    return [{
      id: `control-action-${eventID}`,
      kind: "background_task",
      time: nonempty(row.created_at) ?? new Date(0).toISOString(),
      startedAt: nonempty(row.created_at),
      taskKind: "control_action",
      taskId: invocationID,
      taskStatus: controlActionTaskStatus(status),
      taskSummary: actionTitle(nonempty(row.action)),
      taskDescription: description || undefined,
      taskCommand: target,
      taskOutput: sha ? `Result ${sha}` : undefined,
      taskError: error,
      taskRawItem: row,
      controlActionStatus: status,
      controlActionTool: nonempty(row.source_tool),
      controlActionAction: nonempty(row.action),
      controlActionTarget: target,
      controlActionRepo: repo || undefined,
      controlActionPrNumber: typeof row.pr_number === "number" ? row.pr_number : undefined,
      controlActionSha: sha,
    }];
  });
}

export function breakGlassRequestFromControlAction(
  row: ControlActionRow,
): BreakGlassRequest | null {
  const action = nonempty(row.action);
  if (
    action !== "github.break_glass.request" &&
    action !== "azure.break_glass.request"
  ) {
    return null;
  }
  if (nonempty(row.status) !== "started") return null;
  const eventId = nonempty(row.event_id);
  const invocationId = nonempty(row.invocation_id);
  if (!eventId || !invocationId) return null;
  const repoOwner = nonempty(row.repo_owner);
  const repoName = nonempty(row.repo_name);
  const repo = [repoOwner, repoName].filter(Boolean).join("/");
  const kind = action === "azure.break_glass.request" ? "azure" : "git";
  if (kind === "git" && !repo) return null;
  const payload = payloadObject(row.payload);
  const target = kind === "azure" ? "azure-personal" : repo;
  return {
    eventId,
    invocationId,
    kind,
    createdAt: nonempty(row.created_at),
    target,
    repo,
    repoOwner,
    repoName,
    reason: nonempty(payload.reason),
    source: nonempty(payload.source),
  };
}

// pendingBreakGlassRequests surfaces break-glass requests that are still
// awaiting an admin decision. A grant or deny resolves only the exact
// payload.request_event_id it records; capability grants are not inferred as
// decisions for other requests.
export function pendingBreakGlassRequests(rows: ControlActionRow[]): BreakGlassRequest[] {
  const resolvedRequests = new Set<string>();
  for (const row of rows) {
    const action = nonempty(row.action);
    if (
      action !== "github.break_glass.grant" &&
      action !== "github.break_glass.deny" &&
      action !== "azure.break_glass.grant" &&
      action !== "azure.break_glass.deny"
    )
      continue;
    const payload = payloadObject(row.payload);
    const requestEventId = nonempty(payload.request_event_id);
    if (requestEventId) resolvedRequests.add(requestEventId);
  }
  const byTarget = new Map<string, BreakGlassRequest>();
  for (const row of rows) {
    const request = breakGlassRequestFromControlAction(row);
    if (!request || resolvedRequests.has(request.eventId)) continue;
    const existing = byTarget.get(request.target);
    if (!existing || (request.createdAt ?? "") > (existing.createdAt ?? "")) {
      byTarget.set(request.target, request);
    }
  }
  return Array.from(byTarget.values()).sort((a, b) =>
    (b.createdAt ?? "").localeCompare(a.createdAt ?? ""),
  );
}
