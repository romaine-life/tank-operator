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
  pending_checks: string[];
  pr_number: number;
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

// LivePreviewState is the live frontend-preview sub-document carried inside the
// status snapshot's test_state map (test_state.live_preview). It is written by
// two distinct backend paths and the page reads both:
//   - `enabled` is the owner's durable toggle (the test-slot page's "Start
//     frontend testing" control); the in-pod live-preview daemon converges its
//     build+push loop on it over the session SSE.
//   - `pushed_at` / `pushed_build` are push receipts the daemon reports after
//     each successful PUT to the slot's static-override receiver. `pushed_at` is
//     RFC3339-UTC once a push has landed and null before the first push of an
//     enabled session (so the page can show "waiting for first push").
export interface LivePreviewState {
  enabled: boolean;
  pushed_at: string | null;
  pushed_build: string | null;
}

// readLivePreview extracts the typed live_preview sub-document from a status
// snapshot's test_state map. The snapshot types test_state as an opaque
// Record<string, unknown>, so this narrows it defensively: it returns null when
// the field is absent or not an object (treat "no live preview" and "off" the
// same), and tolerates a partial map — the backend emits an enabled-only
// live_preview before the first push receipt lands pushed_at/pushed_build.
export function readLivePreview(
  status: TestSlotStatus | null,
): LivePreviewState | null {
  const raw = status?.test_state?.live_preview;
  if (!raw || typeof raw !== "object") return null;
  const obj = raw as Record<string, unknown>;
  return {
    enabled: obj.enabled === true,
    pushed_at: typeof obj.pushed_at === "string" ? obj.pushed_at : null,
    pushed_build:
      typeof obj.pushed_build === "string" ? obj.pushed_build : null,
  };
}

export function livePreviewTogglePath(sessionId: string): string {
  return `/api/sessions/${encodeURIComponent(sessionId)}/test-slot/live-preview`;
}

// setLivePreviewEnabled flips the owner's durable live-preview intent on the
// session's test_state.live_preview via the owner-scoped toggle endpoint
// (POST /api/sessions/{id}/test-slot/live-preview, body {"enabled": bool}).
// Enabling requires an already-active slot with a URL — the backend rejects
// otherwise with a 400 whose detail this surfaces. Resolves on a 2xx; throws
// with the server-provided detail on any error so the caller can banner it.
export async function setLivePreviewEnabled(
  sessionId: string,
  enabled: boolean,
  authedFetch: FetchLike,
): Promise<void> {
  const res = await authedFetch(livePreviewTogglePath(sessionId), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) {
    let detail = `live preview toggle failed: ${res.status}`;
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
