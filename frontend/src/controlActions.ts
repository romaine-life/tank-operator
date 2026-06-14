import type { ConversationBackgroundTaskStatus } from "./conversationReducer";

export type ControlActionStatus = "started" | "succeeded" | "failed";

export type ControlActionRow = {
  event_id?: string;
  invocation_id?: string;
  created_at?: string;
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

export type PRLaneRequest = {
  eventId: string;
  invocationId: string;
  createdAt?: string;
  repo?: string;
  repos?: string[];
  allRepos?: boolean;
  laneName: string;
  allocationRequest?: boolean;
  laneNames?: string[];
  proposedBranches?: string[];
  requestedCount?: number;
  unlimited?: boolean;
  relationship?: string;
  base?: string;
  scope?: string;
  reason?: string;
  proposedBranch?: string;
};

export type BreakGlassRequest = {
  eventId: string;
  invocationId: string;
  createdAt?: string;
  repo: string;
  repoOwner?: string;
  repoName?: string;
  reason?: string;
  source?: string;
  approvalUrl?: string;
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
    case "github.break_glass.token":
      return "GitHub break-glass token";
    case "github.break_glass.push":
      return "GitHub break-glass push";
    case "azure.break_glass.request":
      return "Azure break-glass request";
    case "azure.break_glass.grant":
      return "Azure break-glass grant";
    case "azure.break_glass.use":
      return "Azure break-glass use";
    case "github.pr_lane.request":
      return "PR lane request";
    case "github.pr_lane.approve":
      return "PR lane approved";
    case "github.pr_lane.deny":
      return "PR lane denied";
    case "github.pr_lane.auto_approve":
      return "PR lane auto-approval";
    case "github.pr_lane.create":
      return "PR lane created";
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

export function pendingPRLaneRequests(rows: ControlActionRow[]): PRLaneRequest[] {
  const resolvedInvocations = new Set<string>();
  for (const row of rows) {
    const action = nonempty(row.action);
    if (
      action !== "github.pr_lane.approve" &&
      action !== "github.pr_lane.deny" &&
      action !== "github.pr_lane.auto_approve"
    ) {
      continue;
    }
    const invocationID = nonempty(row.invocation_id);
    if (invocationID) resolvedInvocations.add(invocationID);
  }
  return rows.flatMap((row) => {
    if (nonempty(row.action) !== "github.pr_lane.request") return [];
    if (nonempty(row.status) !== "started") return [];
    const eventId = nonempty(row.event_id);
    const invocationId = nonempty(row.invocation_id);
    if (!eventId || !invocationId || resolvedInvocations.has(invocationId)) {
      return [];
    }
    const payload = payloadObject(row.payload);
    const allocationRequest = payload.allocation_request === true;
    const branchScope = payloadObject(payload.branch_scope);
    const repoScope = payloadObject(payload.repo_scope);
    const laneNames = Array.isArray(branchScope.branches)
      ? branchScope.branches.flatMap((value) => {
          const name = nonempty(value);
          return name ? [name] : [];
        })
      : [];
    const proposedBranches = Array.isArray(payload.proposed_branches)
      ? payload.proposed_branches.flatMap((value) => {
          const branch = nonempty(value);
          return branch ? [branch] : [];
        })
      : [];
    const repos = Array.isArray(repoScope.repos)
      ? repoScope.repos.flatMap((value) => {
          const repo = nonempty(value);
          return repo ? [repo] : [];
        })
      : [];
    const laneName = nonempty(payload.lane_name) ?? (allocationRequest ? "branch allocation" : undefined);
    if (!laneName) return [];
    const requestedCount =
      typeof branchScope.count === "number" && Number.isFinite(branchScope.count)
        ? branchScope.count
        : undefined;
    const repo = [nonempty(row.repo_owner), nonempty(row.repo_name)]
      .filter(Boolean)
      .join("/");
    const request: PRLaneRequest = {
      eventId,
      invocationId,
      createdAt: nonempty(row.created_at),
      repo: repo || undefined,
      laneName,
      relationship: nonempty(payload.relationship),
      base: nonempty(payload.base),
      scope: nonempty(payload.scope),
      reason: nonempty(payload.reason),
      proposedBranch: nonempty(payload.proposed_branch),
    };
    if (allocationRequest) request.allocationRequest = true;
    if (laneNames.length > 0) request.laneNames = laneNames;
    if (proposedBranches.length > 0) request.proposedBranches = proposedBranches;
    if (nonempty(repoScope.repo)) request.repo = nonempty(repoScope.repo);
    if (repos.length > 0) request.repos = repos;
    if (repoScope.kind === "all_repos") request.allRepos = true;
    if (branchScope.kind === "count" && requestedCount !== undefined) request.requestedCount = requestedCount;
    if (branchScope.kind === "unlimited") request.unlimited = true;
    return [request];
  });
}

// pendingBreakGlassRequests surfaces git break-glass requests that are still
// awaiting a human grant: a started `github.break_glass.request` whose repo has
// no unexpired `github.break_glass.grant`. Deduped to the newest request per
// repo. This is what lights the "approve break glass" chip on the pull-request
// composer button so an operator can grant from the Tank UI instead of the
// auth.romaine.life approval URL (whose console callback does not yet exist).
export function pendingBreakGlassRequests(
  rows: ControlActionRow[],
  now: number = Date.now(),
): BreakGlassRequest[] {
  const grantedRepos = new Set<string>();
  for (const row of rows) {
    if (nonempty(row.action) !== "github.break_glass.grant") continue;
    if (nonempty(row.status) !== "succeeded") continue;
    const repo = [nonempty(row.repo_owner), nonempty(row.repo_name)]
      .filter(Boolean)
      .join("/");
    if (!repo) continue;
    const expiresAt = nonempty(payloadObject(row.payload).expires_at);
    if (!expiresAt) continue;
    const expiry = Date.parse(expiresAt);
    if (Number.isNaN(expiry) || expiry <= now) continue;
    grantedRepos.add(repo);
  }
  const byRepo = new Map<string, BreakGlassRequest>();
  for (const row of rows) {
    if (nonempty(row.action) !== "github.break_glass.request") continue;
    if (nonempty(row.status) !== "started") continue;
    const eventId = nonempty(row.event_id);
    const invocationId = nonempty(row.invocation_id);
    if (!eventId || !invocationId) continue;
    const repoOwner = nonempty(row.repo_owner);
    const repoName = nonempty(row.repo_name);
    const repo = [repoOwner, repoName].filter(Boolean).join("/");
    if (!repo || grantedRepos.has(repo)) continue;
    const payload = payloadObject(row.payload);
    const request: BreakGlassRequest = {
      eventId,
      invocationId,
      createdAt: nonempty(row.created_at),
      repo,
      repoOwner,
      repoName,
      reason: nonempty(payload.reason),
      source: nonempty(payload.source),
      approvalUrl: nonempty(payload.approval_url),
    };
    const existing = byRepo.get(repo);
    if (!existing || (request.createdAt ?? "") > (existing.createdAt ?? "")) {
      byRepo.set(repo, request);
    }
  }
  return Array.from(byRepo.values()).sort((a, b) =>
    (b.createdAt ?? "").localeCompare(a.createdAt ?? ""),
  );
}
