import { test, expect, vi } from "vitest";
import {
  livePreviewTogglePath,
  readLivePreview,
  setLivePreviewEnabled,
  startTestWorkflow,
  testWorkflowStartPath,
  type TestSlotStatus,
} from "./testWorkflow";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

test("testWorkflowStartPath targets the deterministic endpoint", () => {
  expect(testWorkflowStartPath("77")).toBe(
    "/api/sessions/77/test-workflow/start",
  );
});

test("startTestWorkflow POSTs the gated endpoint and resolves ok on 202", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(202, { status: "started" }));

  const result = await startTestWorkflow("77", fetchMock);

  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe("/api/sessions/77/test-workflow/start");
  expect(init?.method).toBe("POST");
  expect(JSON.parse(String(init?.body))).toEqual({});
  expect(result.ok).toBe(true);
  expect(result.status).toBe(202);
});

test("startTestWorkflow posts drive=true for the drive variant", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(202, { status: "started" }));

  await startTestWorkflow("77", fetchMock, { drive: true });

  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe("/api/sessions/77/test-workflow/start");
  expect(JSON.parse(String(init?.body))).toEqual({ drive: true });
});

test("startTestWorkflow omits drive when false (plain button unchanged)", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(202, { status: "started" }));

  await startTestWorkflow("77", fetchMock, { drive: false });

  const [, init] = fetchMock.mock.calls[0];
  expect(JSON.parse(String(init?.body))).toEqual({});
});

test("startTestWorkflow forwards an explicit repo override", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(202, { status: "started" }));

  await startTestWorkflow("77", fetchMock, { repo: "romaine-life/glimmung" });

  const [, init] = fetchMock.mock.calls[0];
  expect(JSON.parse(String(init?.body))).toEqual({
    repo: "romaine-life/glimmung",
  });
});

test("startTestWorkflow forwards a ref to deploy directly (deploy-by-ref)", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(202, { status: "started" }));

  await startTestWorkflow("77", fetchMock, { ref: "main" });

  const [, init] = fetchMock.mock.calls[0];
  expect(JSON.parse(String(init?.body))).toEqual({ ref: "main" });
});

test("startTestWorkflow surfaces the server reason on refusal", async () => {
  const fetchMock = vi.fn(async () =>
    jsonResponse(409, {
      detail: "session has multiple repositories; specify which to test",
    }),
  );

  const result = await startTestWorkflow("77", fetchMock);

  expect(result.ok).toBe(false);
  expect(result.status).toBe(409);
  expect(result.detail).toContain("multiple repositories");
});

test("startTestWorkflow falls back to a status message for non-JSON errors", async () => {
  const fetchMock = vi.fn(
    async () => new Response("", { status: 503 }),
  );

  const result = await startTestWorkflow("77", fetchMock);

  expect(result.ok).toBe(false);
  expect(result.detail).toContain("503");
});

// --- Live frontend preview surface ---

function statusWithTestState(
  testState: Record<string, unknown> | null,
): TestSlotStatus {
  return {
    repo: null,
    repo_error: "",
    repos: [],
    watch: null,
    provision: null,
    test_state: testState,
    preflight: null,
  };
}

test("livePreviewTogglePath targets the owner toggle endpoint", () => {
  expect(livePreviewTogglePath("77")).toBe(
    "/api/sessions/77/test-slot/live-preview",
  );
});

test("readLivePreview returns null when test_state or live_preview is absent", () => {
  expect(readLivePreview(null)).toBeNull();
  expect(readLivePreview(statusWithTestState(null))).toBeNull();
  expect(readLivePreview(statusWithTestState({ active: true }))).toBeNull();
});

test("readLivePreview reads the full receipt shape", () => {
  const lp = readLivePreview(
    statusWithTestState({
      active: true,
      live_preview: {
        enabled: true,
        pushed_at: "2026-06-20T09:30:00Z",
        pushed_build: "app-abc123",
      },
    }),
  );
  expect(lp).toEqual({
    enabled: true,
    pushed_at: "2026-06-20T09:30:00Z",
    pushed_build: "app-abc123",
  });
});

test("readLivePreview tolerates an enabled-only map (pre-first-push)", () => {
  const lp = readLivePreview(
    statusWithTestState({ live_preview: { enabled: true } }),
  );
  expect(lp).toEqual({ enabled: true, pushed_at: null, pushed_build: null });
});

test("readLivePreview coerces a missing/false enabled flag to off", () => {
  expect(
    readLivePreview(statusWithTestState({ live_preview: {} }))?.enabled,
  ).toBe(false);
  expect(
    readLivePreview(statusWithTestState({ live_preview: { enabled: "yes" } }))
      ?.enabled,
  ).toBe(false);
});

test("setLivePreviewEnabled POSTs the toggle with the enabled flag", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(200, { ok: true }));

  await setLivePreviewEnabled("77", true, fetchMock);

  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe("/api/sessions/77/test-slot/live-preview");
  expect(init?.method).toBe("POST");
  expect(JSON.parse(String(init?.body))).toEqual({ enabled: true });
});

test("setLivePreviewEnabled posts enabled:false for the stop affordance", async () => {
  const fetchMock = vi.fn(async () => jsonResponse(200, { ok: true }));

  await setLivePreviewEnabled("77", false, fetchMock);

  const [, init] = fetchMock.mock.calls[0];
  expect(JSON.parse(String(init?.body))).toEqual({ enabled: false });
});

test("setLivePreviewEnabled throws the server detail on rejection", async () => {
  const fetchMock = vi.fn(async () =>
    jsonResponse(400, { detail: "no active test slot to preview against" }),
  );

  await expect(setLivePreviewEnabled("77", true, fetchMock)).rejects.toThrow(
    "no active test slot to preview against",
  );
});

test("setLivePreviewEnabled falls back to a status message for non-JSON errors", async () => {
  const fetchMock = vi.fn(async () => new Response("", { status: 503 }));

  await expect(setLivePreviewEnabled("77", true, fetchMock)).rejects.toThrow(
    "503",
  );
});
