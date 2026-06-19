import { test, expect, vi } from "vitest";
import { startTestWorkflow, testWorkflowStartPath } from "./testWorkflow";

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
