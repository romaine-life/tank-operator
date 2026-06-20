import { describe, expect, test } from "vitest";
import {
  controlActionRowsToEntries,
  pendingBreakGlassRequests,
  type ControlActionRow,
} from "./controlActions";

describe("pendingBreakGlassRequests", () => {
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

    expect(pendingBreakGlassRequests(rows)).toEqual([
      {
        eventId: "bg-2",
        invocationId: "bg-inv-2",
        createdAt: "2026-06-13T07:30:00Z",
        repo: "romaine-life/tank-operator",
        repoOwner: "romaine-life",
        repoName: "tank-operator",
        kind: "git",
        target: "romaine-life/tank-operator",
        reason: "hotfix push",
        source: "agent",
      },
    ]);
  });

  test("keeps a same-repo request pending when a grant does not reference that request", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "bg-full-api",
        invocation_id: "bg-inv-full-api",
        created_at: "2026-06-13T07:30:00Z",
        action: "github.break_glass.request",
        status: "started",
        repo_owner: "romaine-life",
        repo_name: "glimmung",
        payload: {
          reason: "open missing PR",
          branch_scope: { kind: "unlimited" },
          operations: [
            "mint_full_git_token",
            "push_current_head",
            "full_github_api",
          ],
        },
      },
      {
        event_id: "grant-1",
        invocation_id: "grant-inv-1",
        action: "github.break_glass.grant",
        status: "succeeded",
        repo_owner: "romaine-life",
        repo_name: "glimmung",
        payload: {
          request_event_id: "bg-scoped",
          expires_at: "2026-06-13T09:00:00Z",
          branch_scope: {
            kind: "named",
            branches: ["tank/session/1147/glimmung"],
          },
          operations: ["mint_full_git_token", "push_current_head"],
        },
      },
    ];

    expect(pendingBreakGlassRequests(rows)).toEqual([
      {
        eventId: "bg-full-api",
        invocationId: "bg-inv-full-api",
        createdAt: "2026-06-13T07:30:00Z",
        repo: "romaine-life/glimmung",
        repoOwner: "romaine-life",
        repoName: "glimmung",
        kind: "git",
        target: "romaine-life/glimmung",
        reason: "open missing PR",
        source: undefined,
      },
    ]);
  });

  test("clears a request once a grant or deny references the request event", () => {
    for (const action of ["github.break_glass.grant", "github.break_glass.deny"]) {
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
          event_id: "decision-1",
          invocation_id: "decision-inv-1",
          action,
          status: action.endsWith(".deny") ? "failed" : "succeeded",
          repo_owner: "romaine-life",
          repo_name: "tank-operator",
          payload: { request_event_id: "bg-1" },
        },
      ];

      expect(pendingBreakGlassRequests(rows)).toEqual([]);
    }
  });

  test("returns azure break-glass requests until an exact decision exists", () => {
    const rows: ControlActionRow[] = [
      {
        event_id: "azure-bg-1",
        invocation_id: "azure-inv-1",
        created_at: "2026-06-13T07:30:00Z",
        action: "azure.break_glass.request",
        status: "started",
        target_kind: "azure_mcp",
        target_ref: "azure-personal",
        payload: { reason: "inspect ledger", source: "agent" },
      },
    ];

    expect(pendingBreakGlassRequests(rows)).toEqual([
      {
        eventId: "azure-bg-1",
        invocationId: "azure-inv-1",
        createdAt: "2026-06-13T07:30:00Z",
        kind: "azure",
        target: "azure-personal",
        repo: "",
        repoOwner: undefined,
        repoName: undefined,
        reason: "inspect ledger",
        source: "agent",
      },
    ]);
  });
});

describe("controlActionRowsToEntries", () => {
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

  test("retires the github.pr_lane.* action labels", () => {
    // The PR-lane mechanism is folded into break-glass branch-lane grants, so
    // its dedicated ledger labels are gone. Any stray retired-path row must
    // fall through to the generic label rather than re-introduce "PR lane *".
    for (const action of [
      "github.pr_lane.request",
      "github.pr_lane.approve",
      "github.pr_lane.deny",
      "github.pr_lane.auto_approve",
      "github.pr_lane.create",
    ]) {
      const entries = controlActionRowsToEntries([
        {
          event_id: `evt-${action}`,
          invocation_id: `inv-${action}`,
          action,
          status: "started",
        },
      ]);
      expect(entries[0]?.taskSummary).toBe("Control action");
    }
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
