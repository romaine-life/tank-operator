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

  test("labels azure privileged-access events", () => {
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

    expect(entries[0]?.taskSummary).toBe("Azure privileged access request");
    expect(entries[1]?.taskSummary).toBe("Azure privileged access grant");
  });

  test("labels test-slot model approval events", () => {
    const entries = controlActionRowsToEntries([
      {
        event_id: "model-request-1",
        invocation_id: "model-inv-1",
        action: "tank.test_slot_model.request",
        status: "started",
        target_ref: "tank://session-scope/tank-operator-slot-3/sessions/47/test-slot-model/codex_gui",
        target_kind: "tank_session_model",
      },
      {
        event_id: "model-grant-1",
        invocation_id: "model-inv-2",
        action: "tank.test_slot_model.grant",
        status: "succeeded",
        target_ref: "tank://session-scope/tank-operator-slot-3/sessions/47/test-slot-model/codex_gui",
        target_kind: "tank_session_model",
      },
    ]);

    expect(entries[0]?.taskSummary).toBe("Test-slot model request");
    expect(entries[1]?.taskSummary).toBe("Test-slot model grant");
  });
});
