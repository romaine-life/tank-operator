import { describe, expect, test } from "vitest";
import {
  controlActionRowsToEntries,
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
          repo_scope: { kind: "current_repo", repo: "romaine-life/tank-operator" },
          branch_scope: { kind: "named", branches: ["docs", "backend"] },
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
          repo_scope: { kind: "current_repo", repo: "romaine-life/tank-operator" },
          branch_scope: { kind: "unlimited" },
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
        createdAt: undefined,
        repo: "romaine-life/tank-operator",
        laneName: "branch allocation",
        allocationRequest: true,
        laneNames: ["docs", "backend"],
        relationship: undefined,
        base: undefined,
        scope: undefined,
        reason: "split review",
        proposedBranch: undefined,
      },
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
});
