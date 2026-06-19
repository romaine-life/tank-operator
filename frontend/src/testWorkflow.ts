// Deterministic interactive test-workflow trigger.
//
// The "test" button no longer sends the LLM-driven `/test` skill to the agent.
// It POSTs to the backend's deterministic, gated endpoint
// (`POST /api/sessions/{id}/test-workflow/start`), which validates the session's
// governed-PR readiness server-side and provisions a Glimmung test slot only on
// a green/mergeable verdict. The endpoint returns 202 immediately (validation
// can take minutes); the outcome then surfaces through the test-state pill
// (provisioned) and the dedicated test-slot page, which reads the durable
// readiness/provision snapshot via fetchTestSlotStatus below. The page is the
// primary surface for the controls and PR-readiness; the beaker routes to it.

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
// `options.repo` disambiguates a multi-repo session ("owner/name"). `options.drive`
// selects the "Create test slot and test" variant: the backend runs the same
// zero-LLM provision and surfaces the same thread, then — only on a ready slot —
// wakes the agent to validate the running slot. It resolves with `ok: true` on
// the 202 accept, or `ok: false` plus the server-provided reason on any error
// response so the caller can surface it.
export async function startTestWorkflow(
  sessionId: string,
  authedFetch: FetchLike,
  options: { repo?: string; drive?: boolean } = {},
): Promise<StartTestWorkflowResult> {
  const body: Record<string, string | boolean> = {};
  if (options.repo && options.repo.trim()) {
    body.repo = options.repo.trim();
  }
  if (options.drive) {
    body.drive = true;
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

// --- Read-only test-slot status surface (GET /api/sessions/{id}/test-slot) ---
//
// Backs the dedicated test-slot page. Mirrors the backend
// testSlotStatusResponse: durable last-known PR readiness (the
// session_ci_watches row), the durable in-flight/last interactive provision,
// the resolved governed-PR coordinates (or a soft repo_error), the session's
// test_state, and — only on a ?refresh=1 read — an authoritative side-effect-free
// live preflight verdict.

export interface TestSlotRepo {
  owner: string;
  name: string;
  slug: string;
  branch: string;
}

export interface TestSlotWatch {
  status: string;
  mergeable_state: string;
  check_state: string;
  detail: string;
  pr_url: string;
  pr_number: number;
  head_sha: string;
  last_event_at: string | null;
  has_open_pr: boolean;
}

export interface TestSlotProvision {
  status: string;
  detail: string;
  head_sha: string;
  started_at: string;
  last_event_at: string | null;
}

export interface TestSlotPreflight {
  verdict: string;
  mergeable_state: string;
  check_state: string;
  failing_checks: string[];
  pr_url: string;
  head_sha: string;
  detail: string;
  has_open_pr: boolean;
}

export interface TestSlotStatus {
  repo: TestSlotRepo | null;
  repo_error: string;
  repos: string[];
  watch: TestSlotWatch | null;
  provision: TestSlotProvision | null;
  test_state: Record<string, unknown> | null;
  preflight: TestSlotPreflight | null;
}

export function testSlotStatusPath(
  sessionId: string,
  options: { repo?: string; refresh?: boolean } = {},
): string {
  const params = new URLSearchParams();
  if (options.repo && options.repo.trim()) params.set("repo", options.repo.trim());
  if (options.refresh) params.set("refresh", "1");
  const qs = params.toString();
  return `/api/sessions/${encodeURIComponent(sessionId)}/test-slot${qs ? `?${qs}` : ""}`;
}

// fetchTestSlotStatus reads the page snapshot. `options.refresh` adds the live
// preflight (one GitHub read, no side effects); without it the read is the cheap
// durable snapshot. Throws with the server-provided detail on a non-2xx.
export async function fetchTestSlotStatus(
  sessionId: string,
  authedFetch: FetchLike,
  options: { repo?: string; refresh?: boolean } = {},
): Promise<TestSlotStatus> {
  const res = await authedFetch(testSlotStatusPath(sessionId, options));
  if (!res.ok) {
    let detail = `test slot status failed: ${res.status}`;
    try {
      const data = await res.json();
      if (data && typeof data.detail === "string" && data.detail) {
        detail = data.detail;
      }
    } catch {
      // Non-JSON body: keep the status-derived message.
    }
    throw new Error(detail);
  }
  return (await res.json()) as TestSlotStatus;
}
