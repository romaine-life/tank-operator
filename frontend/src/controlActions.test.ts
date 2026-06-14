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
});
