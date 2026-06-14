import { describe, expect, test } from "vitest";

import { collectGlimmungRunsFromEntries } from "./glimmungRuns";

describe("collectGlimmungRunsFromEntries", () => {
  test("extracts run refs from Glimmung MCP tool output", () => {
    const runs = collectGlimmungRunsFromEntries([
      {
        id: "tool-1",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "glimmung",
        toolName: "glimmung.dispatch_run",
        toolOutput: JSON.stringify({
          state: "dispatched",
          run_ref: "tank-operator#1184/runs/12.1",
        }),
        completedAt: "2026-06-14T10:00:00Z",
      },
    ]);

    expect(runs).toEqual([
      expect.objectContaining({
        label: "tank-operator#1184 run 12.1",
        href: "https://glimmung.romaine.life/projects/tank-operator/issues/1184/runs/12/cycles/1",
        state: "dispatched",
      }),
    ]);
  });

  test("extracts dispatch responses with separate run and cycle numbers", () => {
    const runs = collectGlimmungRunsFromEntries([
      {
        id: "tool-1",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "mcp-glimmung",
        toolName: "mcp-glimmung.dispatch_run",
        toolOutput: JSON.stringify({
          project: "ambience",
          issue_number: 168,
          run_number: 9,
          cycle_number: 1,
          state: "dispatched",
        }),
      },
    ]);

    expect(runs.map((run) => [run.label, run.href])).toEqual([
      [
        "ambience#168 run 9.1",
        "https://glimmung.romaine.life/projects/ambience/issues/168/runs/9/cycles/1",
      ],
    ]);
  });

  test("extracts canonical dashboard URLs from tool output", () => {
    const runs = collectGlimmungRunsFromEntries([
      {
        id: "tool-1",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "glimmung",
        toolName: "glimmung.get_run_report",
        toolOutput:
          "https://glimmung.romaine.life/projects/glimmung/issues/206/runs/1/cycles/2",
      },
    ]);

    expect(runs[0]?.label).toBe("glimmung#206 run 1.2");
  });

  test("dedupes repeated runs and ignores unrelated tools", () => {
    const runs = collectGlimmungRunsFromEntries([
      {
        id: "github-tool",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "github",
        toolName: "github.create_issue",
        toolOutput: "tank-operator#1184/runs/12.1",
      },
      {
        id: "glimmung-started",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "glimmung",
        toolName: "glimmung.dispatch_run",
        toolOutput: '{"run_ref":"tank-operator#1184/runs/12.1","state":"pending"}',
        completedAt: "2026-06-14T09:00:00Z",
      },
      {
        id: "glimmung-completed",
        kind: "tool",
        toolKind: "mcp",
        toolServer: "glimmung",
        toolName: "glimmung.dispatch_run",
        toolOutput: '{"run_ref":"tank-operator#1184/runs/12.1","state":"dispatched"}',
        completedAt: "2026-06-14T10:00:00Z",
      },
    ]);

    expect(runs).toHaveLength(1);
    expect(runs[0]?.sourceEntryId).toBe("glimmung-completed");
    expect(runs[0]?.state).toBe("dispatched");
  });
});
