import { formatCompactTokens } from "./sessionCostEstimate";
import { normalizeBugLabelDisplayName } from "./bugLabels";

export type StatusTone = "good" | "info" | "warning" | "danger" | "muted";

export type SessionDataStatusId =
  | "transcript"
  | "test"
  | "context"
  | "rollout"
  | "pull_request"
  | "bug_report"
  | "linked_repo";

export interface SessionDataStatusRow {
  id: SessionDataStatusId;
  label: string;
  status: string;
  detail: string;
  tone: StatusTone;
  href?: string;
}

interface SessionDataStatusInput {
  status?: string;
  test_state?: {
    active?: boolean;
    slot_index?: number | null;
    url?: string | null;
    pull_request_url?: string | null;
  } | null;
  rollout_state?: {
    active?: boolean;
  } | null;
  repos?: string[] | null;
  clone_state?: Record<string, unknown> | null;
  bug_label?: {
    display_name?: string;
    name?: string;
  } | null;
  bug_labels?: Array<{
    display_name?: string;
    name?: string;
  }> | null;
  runtime_context_window_tokens?: number;
  runtime_context_window_source?: string;
  runtime_context_window_observed_at?: string | null;
  compaction_count?: number;
}

export function buildSessionDataStatusRows(session: SessionDataStatusInput): SessionDataStatusRow[] {
  const testState = session.test_state ?? null;
  const rolloutState = session.rollout_state ?? null;
  const pullRequestURL = trimOptionalString(testState?.pull_request_url);
  const repos = Array.isArray(session.repos) ? session.repos : [];
  const compactions = nonNegativeInteger(session.compaction_count);
  const contextWindow = nonNegativeInteger(session.runtime_context_window_tokens);
  const contextSource = trimOptionalString(session.runtime_context_window_source);
  const bugLabels = Array.isArray(session.bug_labels)
    ? session.bug_labels
        .map((label) =>
          normalizeBugLabelDisplayName(
            trimOptionalString(label?.display_name) ?? trimOptionalString(label?.name),
          ),
        )
        .filter((label): label is string => Boolean(label))
    : [];
  const bugLabel =
    bugLabels[0] ??
    trimOptionalString(
      normalizeBugLabelDisplayName(
        trimOptionalString(session.bug_label?.display_name) ??
          trimOptionalString(session.bug_label?.name),
      ),
    );
  const bugCount = bugLabels.length || (bugLabel ? 1 : 0);

  return [
    {
      id: "transcript",
      label: "Main transcript",
      status: session.status === "Failed" ? "Stopped" : "Available",
      detail: session.status === "Failed" ? "Session container crashed or terminated" : "Full conversation timeline",
      tone: session.status === "Failed" ? "danger" : "info",
    },
    {
      id: "test",
      label: "Test slot",
      status: testState?.active ? "Active" : "Inactive",
      detail: testState?.active
        ? testSlotDetail(testState.slot_index)
        : "No active validation slot",
      tone: testState?.active ? "good" : "muted",
      href: trimOptionalString(testState?.url) ?? undefined,
    },
    {
      id: "context",
      label: "Context",
      status: compactions > 0 ? "Compacted" : contextWindow > 0 ? "Observed" : "Unknown",
      detail: contextDetail(compactions, contextWindow, contextSource),
      tone: compactions > 0 ? "warning" : contextWindow > 0 ? "info" : "muted",
    },
    {
      id: "rollout",
      label: "Rollout",
      status: rolloutState?.active ? "Active" : "Inactive",
      detail: rolloutState?.active ? "Release workflow in progress" : "No active rollout",
      tone: rolloutState?.active ? "warning" : "muted",
    },
    {
      id: "pull_request",
      label: "Pull request",
      status: pullRequestURL ? "Linked" : "Missing",
      detail: pullRequestURL ? pullRequestDetail(pullRequestURL) : "No pull request linked",
      tone: pullRequestURL ? "info" : "muted",
      href: pullRequestURL ?? undefined,
    },
    {
      id: "bug_report",
      label: "Bug report",
      status: bugCount > 1 ? `${bugCount} linked` : bugLabel ? "Linked" : "None",
      detail: bugCount > 1 ? bugLabels.join(", ") : bugLabel ?? "No bug report label",
      tone: bugCount > 0 ? "info" : "muted",
    },
    linkedRepoStatus(repos, session.clone_state ?? null),
  ];
}

function testSlotDetail(slotIndex: number | null | undefined): string {
  return typeof slotIndex === "number" && Number.isFinite(slotIndex)
    ? `Slot ${Math.floor(slotIndex)} reserved`
    : "Validation slot reserved";
}

function contextDetail(compactions: number, contextWindow: number, contextSource: string | null): string {
  const parts = [
    compactions > 0
      ? `${compactions} compaction${compactions === 1 ? "" : "s"}`
      : "No compactions",
  ];
  if (contextWindow > 0) {
    parts.push(`${formatCompactTokens(contextWindow)} window`);
  } else {
    parts.push("window not reported");
  }
  if (contextSource) parts.push(contextSource);
  return parts.join(" / ");
}

function pullRequestDetail(url: string): string {
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.split("/").filter(Boolean);
    const pullIndex = parts.indexOf("pull");
    if (parsed.hostname === "github.com" && pullIndex >= 2 && parts[pullIndex + 1]) {
      return `${parts[pullIndex - 2]}/${parts[pullIndex - 1]}#${parts[pullIndex + 1]}`;
    }
  } catch {
    // Fall back to the raw trimmed value below.
  }
  return url;
}

function linkedRepoStatus(
  repos: string[],
  cloneState: Record<string, unknown> | null,
): SessionDataStatusRow {
  if (repos.length === 0) {
    return {
      id: "linked_repo",
      label: "Linked repos",
      status: "None",
      detail: "No repositories selected",
      tone: "muted",
    };
  }
  const cloneSummary = summarizeCloneState(repos, cloneState);
  if (cloneSummary.failures > 0) {
    return {
      id: "linked_repo",
      label: "Linked repos",
      status: "Needs attention",
      detail: `${cloneSummary.failures}/${repos.length} repo clone issue${cloneSummary.failures === 1 ? "" : "s"}`,
      tone: "danger",
    };
  }
  if (cloneSummary.pending > 0) {
    return {
      id: "linked_repo",
      label: "Linked repos",
      status: "Syncing",
      detail: `${cloneSummary.ready}/${repos.length} ready, ${cloneSummary.pending} pending`,
      tone: "warning",
    };
  }
  if (cloneSummary.known > 0 && cloneSummary.ready === repos.length) {
    return {
      id: "linked_repo",
      label: "Linked repos",
      status: "Ready",
      detail: `${repos.length} repo${repos.length === 1 ? "" : "s"} cloned`,
      tone: "good",
    };
  }
  return {
    id: "linked_repo",
    label: "Linked repos",
    status: "Selected",
    detail: `${repos.length} repo${repos.length === 1 ? "" : "s"} selected`,
    tone: "info",
  };
}

function summarizeCloneState(
  repos: string[],
  cloneState: Record<string, unknown> | null,
): { known: number; ready: number; pending: number; failures: number } {
  if (!cloneState) return { known: 0, ready: 0, pending: 0, failures: 0 };
  let known = 0;
  let ready = 0;
  let pending = 0;
  let failures = 0;
  for (const repo of repos) {
    const value = cloneState[repo];
    if (value === undefined) continue;
    known += 1;
    const text = cloneStateText(value);
    if (/\b(error|failed|failure|fatal|denied|unauthorized|timeout)\b/i.test(text)) {
      failures += 1;
    } else if (/\b(pending|running|cloning|checkout|queued|starting)\b/i.test(text)) {
      pending += 1;
    } else if (/\b(ok|ready|done|success|succeeded|complete|completed|cloned)\b/i.test(text)) {
      ready += 1;
    } else {
      pending += 1;
    }
  }
  return { known, ready, pending, failures };
}

function cloneStateText(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  if (Array.isArray(value)) return value.map(cloneStateText).join(" ");
  if (typeof value === "object") return Object.values(value as Record<string, unknown>).map(cloneStateText).join(" ");
  return "";
}

function nonNegativeInteger(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.max(0, Math.floor(value))
    : 0;
}

function trimOptionalString(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}
