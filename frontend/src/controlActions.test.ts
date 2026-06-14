import { describe, expect, test } from "vitest";
import {
  controlActionRowsToEntries,
  pendingBreakGlassRequests,
  pendingPRLaneRequests,
  type ControlActionRow,
} from "./controlActions";

describe("pendingPRLaneRequests", () => {
  test("returns started PR lane requests until a decision resolves the invocation", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "request-2",
        invocation_id: "inv-2",
        created_at: "2026-06-13T07:00:00Z",
        action: "github.pr_lane.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: {
          lane_name: "docs",
          relationship: "parallel",
          base: "main",
          scope: "docs/",
          reason: "split docs review",
          proposed_branch: "tank/session/47/tank-operator/docs",
        },
      },
      {
        event_id: "request-1",
        invocation_id: "inv-1",
        action: "github.pr_lane.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: { lane_name: "backend", reason: "split backend" },
      },
      {
        event_id: "approve-1",
        invocation_id: "inv-1",
        action: "github.pr_lane.approve",
        status: "succeeded",
      },
    ];

    expect(pendingPRLaneRequests(rows)).toEqual([
      {
        eventId: "request-2",
        invocationId: "inv-2",
        createdAt: "2026-06-13T07:00:00Z",
        repo: "romaine-life/tank-operator",
        laneName: "docs",
        relationship: "parallel",
        base: "main",
        scope: "docs/",
        reason: "split docs review",
        proposedBranch: "tank/session/47/tank-operator/docs",
      },
    ]);
  });

  test("returns allocation requests and resolves them after auto-approval", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "allocation-1",
        invocation_id: "alloc-inv-1",
        action: "github.pr_lane.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: {
          allocation_request: true,
          lane_names: ["docs", "backend"],
          requested_count: 2,
          reason: "split review",
        },
      },
      {
        event_id: "allocation-2",
        invocation_id: "alloc-inv-2",
        action: "github.pr_lane.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: {
          allocation_request: true,
          unlimited: true,
          reason: "large migration",
        },
      },
      {
        event_id: "auto-2",
        invocation_id: "alloc-inv-2",
        action: "github.pr_lane.auto_approve",
        status: "succeeded",
      },
    ];

    expect(pendingPRLaneRequests(rows)).toEqual([
      {
        eventId: "allocation-1",
        invocationId: "alloc-inv-1",
        repo: "romaine-life/tank-operator",
        laneName: "branch allocation",
        allocationRequest: true,
        laneNames: ["docs", "backend"],
        requestedCount: 2,
        relationship: undefined,
        base: undefined,
        scope: undefined,
        reason: "split review",
        proposedBranch: undefined,
      },
    ]);
  });
});

describe("pendingBreakGlassRequests", () => {
  const NOW = Date.parse("2026-06-13T08:00:00Z");

  test("returns started break-glass requests with no active grant", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "bg-2",
        invocation_id: "bg-inv-2",
        created_at: "2026-06-13T07:30:00Z",
        action: "github.break_glass.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: { reason: "hotfix push", source: "agent" },
      },
      {
        event_id: "bg-1",
        invocation_id: "bg-inv-1",
        created_at: "2026-06-13T07:00:00Z",
        action: "github.break_glass.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: { reason: "earlier attempt" },
      },
    ];

    expect(pendingBreakGlassRequests(rows, NOW)).toEqual([
      {
        eventId: "bg-2",
        invocationId: "bg-inv-2",
        createdAt: "2026-06-13T07:30:00Z",
        repo: "romaine-life/tank-operator",
        repoOwner: "romaine-life",
        repoName: "tank-operator",
        reason: "hotfix push",
        source: "agent",
        approvalUrl: undefined,
      },
    ]);
  });

  test("clears a request once an unexpired grant exists for the repo", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "bg-1",
        invocation_id: "bg-inv-1",
        action: "github.break_glass.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: {},
      },
      {
        event_id: "grant-1",
        invocation_id: "grant-inv-1",
        action: "github.break_glass.grant",
        status: "succeeded",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: { expires_at: "2026-06-13T09:00:00Z" },
      },
    ];

    expect(pendingBreakGlassRequests(rows, NOW)).toEqual([]);
  });

  test("keeps the request pending when the grant has expired", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "bg-1",
        invocation_id: "bg-inv-1",
        created_at: "2026-06-13T07:00:00Z",
        action: "github.break_glass.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: {},
      },
      {
        event_id: "grant-1",
        invocation_id: "grant-inv-1",
        action: "github.break_glass.grant",
        status: "succeeded",
        repo_owner: "romaine-life",
        repo_name: "tank-operator",
        payload: { expires_at: "2026-06-13T07:45:00Z" },
      },
    ];

    expect(pendingBreakGlassRequests(rows, NOW).map((r) => r.eventId)).toEqual([
      "bg-1",
    ]);
  });
});

describe("controlActionRowsToEntries", () => {
  test("labels PR lane events in the control-action ledger", () => {
    const entries = controlActionRowsToEntries([
      {
        event_id: "request-1",
        invocation_id: "inv-1",
        created_at: "2026-06-13T07:00:00Z",
        action: "github.pr_lane.request",
        status: "started",
        target_ref: "https://github.com/romaine-life/tank-operator",
        target_kind: "github_repository",
      },
    ]);

    expect(entries[0]?.taskSummary).toBe("PR lane request");
    expect(entries[0]?.taskStatus).toBe("running");
  });

  test("labels governed PR rename events", () => {
    const entries = controlActionRowsToEntries([
      {
        event_id: "rename-1",
        invocation_id: "rename-inv-1",
        action: "github.pull_request.rename",
        status: "succeeded",
        target_ref: "https://github.com/romaine-life/tank-operator/pull/1176",
      },
    ]);

    expect(entries[0]?.taskSummary).toBe("GitHub PR renamed");
    expect(entries[0]?.taskStatus).toBe("completed");
  });

  test("labels azure break-glass events", () => {
    const entries = controlActionRowsToEntries([
      {
        event_id: "azure-req-1",
        invocation_id: "azure-inv-1",
        action: "azure.break_glass.request",
        status: "started",
        target_ref: "azure-personal",
        target_kind: "azure_mcp",
      },
      {
        event_id: "azure-grant-1",
        invocation_id: "azure-inv-2",
        action: "azure.break_glass.grant",
        status: "succeeded",
        target_ref: "azure-personal",
        target_kind: "azure_mcp",
      },
    ]);

    expect(entries[0]?.taskSummary).toBe("Azure break-glass request");
    expect(entries[1]?.taskSummary).toBe("Azure break-glass grant");
  });
});
