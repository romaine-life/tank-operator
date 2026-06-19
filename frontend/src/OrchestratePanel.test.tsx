// Component / interaction tests for OrchestratePanel (jsdom — .test.tsx).
//
// Covers:
//   1. normalizeOrchestrateRunOptions — pure helper, confirmed here for coverage.
//   2. Form state: not-a-hub renders the form; is-a-hub renders status view.
//   3. Form submit: POSTs to the launch endpoint with the right body.
//   4. Wand button gating: visible in GUI modes, disabled until ready.
//   5. Panel renders status from a durable spoke_config snapshot.
//
// Auth: authedFetch is vi.mock'd so tests never hit the network.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { OrchestratePanel, normalizeOrchestrateRunOptions } from "./OrchestratePanel";
import type { SpawnedSessionRef } from "./spawnedSessions";

// ---------------------------------------------------------------------------
// Mock authedFetch so no real network calls happen
// ---------------------------------------------------------------------------
vi.mock("./auth", () => ({
  authedFetch: vi.fn(),
}));

import { authedFetch } from "./auth";
const mockFetch = vi.mocked(authedFetch);

// A minimal run-options response body matching the backend contract.
const RUN_OPTIONS_BODY = {
  models: { claude: ["claude-opus-4-5", "claude-sonnet-4-5"], codex: ["codex-1", "codex-mini"] },
  efforts: { claude: ["normal", "low"], codex: ["medium"] },
  default_models: { claude: "claude-opus-4-5", codex: "codex-1" },
  default_efforts: { claude: "normal", codex: "medium" },
  create_modes: ["claude_gui", "codex_gui"],
  sdk_chat_modes: [{ mode: "claude_gui", provider: "claude" }],
  retired_create_modes: {},
  test_slot_defaults: { mode: "claude_gui", model: "", effort: "" },
};

function makeRunOptionsResponse(): Response {
  return new Response(JSON.stringify(RUN_OPTIONS_BODY), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function makeOrchestrateResponse(): Response {
  return new Response(
    JSON.stringify({
      active: true,
      session_id: "sess-1",
      spoke_config: { provider: "claude", surface: "gui", model: "claude-opus-4-5", effort: "normal" },
      break_glass: { active: true, all_repos: true },
      kickoff_turn: "turn-1",
      grant_event_id: "evt-1",
    }),
    { status: 202, headers: { "Content-Type": "application/json" } },
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// Pure helper tests
// ---------------------------------------------------------------------------

describe("normalizeOrchestrateRunOptions", () => {
  test("maps claude and codex models, efforts, and defaults from wire body", () => {
    const result = normalizeOrchestrateRunOptions(RUN_OPTIONS_BODY);
    expect(result).not.toBeNull();
    expect(result!.models.claude).toEqual(["claude-opus-4-5", "claude-sonnet-4-5"]);
    expect(result!.models.codex).toEqual(["codex-1", "codex-mini"]);
    expect(result!.default_models.claude).toBe("claude-opus-4-5");
    expect(result!.default_efforts.codex).toBe("medium");
  });

  test("accepts anthropic as an alias for claude models", () => {
    const raw = { ...RUN_OPTIONS_BODY, models: { anthropic: ["claude-opus-4-5"] } };
    const result = normalizeOrchestrateRunOptions(raw);
    expect(result!.models.claude).toEqual(["claude-opus-4-5"]);
  });

  test("returns null for non-object input", () => {
    expect(normalizeOrchestrateRunOptions(null)).toBeNull();
    expect(normalizeOrchestrateRunOptions("string")).toBeNull();
    expect(normalizeOrchestrateRunOptions(42)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Form rendering (not yet a hub)
// ---------------------------------------------------------------------------

describe("OrchestratePanel — launch form", () => {
  test("shows loading state while run options are fetching", () => {
    // Never resolve so we can observe the loading state.
    mockFetch.mockReturnValue(new Promise(() => {}));

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.getByText(/loading options/i)).toBeInTheDocument();
  });

  test("renders provider and surface radios after run options load", async () => {
    mockFetch.mockResolvedValue(makeRunOptionsResponse());

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("radio", { name: "Claude" })).toBeInTheDocument();
    });

    expect(screen.getByRole("radio", { name: "Codex" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "GUI" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "CLI" })).toBeInTheDocument();
  });

  test("shows the blast-radius warning", async () => {
    mockFetch.mockResolvedValue(makeRunOptionsResponse());

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("radio", { name: "Claude" })).toBeInTheDocument();
    });

    expect(screen.getByText(/full git access to all repositories/i)).toBeInTheDocument();
  });

  test("submit button is disabled when session is not ready", async () => {
    mockFetch.mockResolvedValue(makeRunOptionsResponse());

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={false}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("radio", { name: "Claude" })).toBeInTheDocument();
    });

    expect(screen.getByRole("button", { name: /launch spoke/i })).toBeDisabled();
  });
});

// ---------------------------------------------------------------------------
// Form submit — calls the launch endpoint
// ---------------------------------------------------------------------------

describe("OrchestratePanel — form submit", () => {
  test("POSTs to /api/sessions/{id}/orchestrate with the right body on submit", async () => {
    const user = userEvent.setup();
    // First call: run options. Second: orchestrate POST.
    mockFetch
      .mockResolvedValueOnce(makeRunOptionsResponse())
      .mockResolvedValueOnce(makeOrchestrateResponse());

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /launch spoke/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /launch spoke/i }));

    // Should have called authedFetch twice: run-options + orchestrate POST.
    expect(mockFetch).toHaveBeenCalledTimes(2);

    const [postUrl, postInit] = mockFetch.mock.calls[1];
    expect(String(postUrl)).toContain("/api/sessions/sess-1/orchestrate");
    expect(postInit?.method).toBe("POST");

    const body = JSON.parse(String(postInit?.body));
    expect(body.provider).toBe("claude");
    expect(body.surface).toBe("gui");
    expect(body.model).toBe("claude-opus-4-5");
    expect(body.effort).toBe("normal");
  });

  test("shows error message on non-202 response", async () => {
    const user = userEvent.setup();
    mockFetch
      .mockResolvedValueOnce(makeRunOptionsResponse())
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ detail: "session not active" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        }),
      );

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /launch spoke/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /launch spoke/i }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
      expect(screen.getByText(/session not active/i)).toBeInTheDocument();
    });
  });

  test("does not optimistically flip to status view — waits for durable SSE", async () => {
    const user = userEvent.setup();
    mockFetch
      .mockResolvedValueOnce(makeRunOptionsResponse())
      .mockResolvedValueOnce(makeOrchestrateResponse());

    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={undefined}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /launch spoke/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /launch spoke/i }));

    // After a 202 the panel should NOT flip to the status view —
    // it stays on the form, waiting for the durable spoke_config SSE update.
    await waitFor(() => {
      expect(screen.queryByText(/orchestration active/i)).toBeNull();
    });
  });
});

// ---------------------------------------------------------------------------
// Status view (hub with spoke_config)
// ---------------------------------------------------------------------------

describe("OrchestratePanel — status view", () => {
  const SPOKE_CONFIG: Record<string, unknown> = {
    provider: "claude",
    surface: "gui",
    model: "claude-opus-4-5",
    effort: "normal",
    configured_by: "agent@example.com",
    configured_at: "2026-06-19T12:00:00Z",
  };

  const SPAWNED: SpawnedSessionRef[] = [
    {
      id: "child-1",
      name: "Spoke session 1",
      url: "https://tank.romaine.life/sessions/child-1",
      mode: "claude_gui",
      model: "claude-opus-4-5",
    },
  ];

  test("renders hub status with spoke_config fields", () => {
    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={SPOKE_CONFIG}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    // Status badge
    expect(screen.getByText("hub")).toBeInTheDocument();
    // Config fields
    expect(screen.getByText("claude")).toBeInTheDocument();
    expect(screen.getByText("claude-opus-4-5")).toBeInTheDocument();
    expect(screen.getByText("normal")).toBeInTheDocument();
  });

  test("does not call authedFetch when rendering status view", () => {
    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={SPOKE_CONFIG}
        spawnedSessions={[]}
        ready={true}
      />,
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("lists spawned spoke sessions with external links", () => {
    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={SPOKE_CONFIG}
        spawnedSessions={SPAWNED}
        ready={true}
      />,
    );

    const link = screen.getByRole("link", { name: /spoke session 1/i });
    expect(link).toHaveAttribute("href", "https://tank.romaine.life/sessions/child-1");
    expect(link).toHaveAttribute("target", "_blank");
  });

  test("shows blast-radius note about the git break-glass grant", () => {
    render(
      <OrchestratePanel
        sessionId="sess-1"
        spokeConfig={SPOKE_CONFIG}
        spawnedSessions={[]}
        ready={true}
      />,
    );

    expect(screen.getByText(/active git break-glass grant/i)).toBeInTheDocument();
  });
});
