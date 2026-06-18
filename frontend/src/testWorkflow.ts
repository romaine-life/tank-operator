// Deterministic interactive test-workflow trigger.
//
// The "test" button no longer sends the LLM-driven `/test` skill to the agent.
// It POSTs to the backend's deterministic, gated endpoint
// (`POST /api/sessions/{id}/test-workflow/start`), which validates the session's
// governed-PR readiness server-side and provisions a Glimmung test slot only on
// a green/mergeable verdict. The endpoint returns 202 immediately (validation
// can take minutes); the outcome then surfaces through the existing test-state
// pill (provisioned) and the durable `test_provision.updated` thread the backend
// emits — opener → validating/waiting → terminal ready/refusal — which renders
// inline as a grouped role:system thread in the turns view.

export function testWorkflowStartPath(sessionId: string): string {
  return `/api/sessions/${encodeURIComponent(sessionId)}/test-workflow/start`;
}

export interface StartTestWorkflowResult {
  ok: boolean;
  status: number;
  detail: string;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

// startTestWorkflow triggers the deterministic test workflow for a session.
// `options.repo` disambiguates a multi-repo session ("owner/name"). It resolves
// with `ok: true` on the 202 accept, or `ok: false` plus the server-provided
// reason on any error response so the caller can surface it.
export async function startTestWorkflow(
  sessionId: string,
  authedFetch: FetchLike,
  options: { repo?: string } = {},
): Promise<StartTestWorkflowResult> {
  const body: Record<string, string> = {};
  if (options.repo && options.repo.trim()) {
    body.repo = options.repo.trim();
  }
  const res = await authedFetch(testWorkflowStartPath(sessionId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  let detail = "";
  try {
    const data = await res.json();
    if (data && typeof data.detail === "string") {
      detail = data.detail;
    }
  } catch {
    // Non-JSON body (e.g. empty): fall back to a status-derived message below.
  }
  if (!res.ok) {
    return {
      ok: false,
      status: res.status,
      detail: detail || `test workflow start failed: ${res.status}`,
    };
  }
  return { ok: true, status: res.status, detail };
}
