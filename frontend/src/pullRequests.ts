// Durable "pull requests touched by this session" projection, surfaced by the
// composer git chip and the dedicated /pull-requests page. The backend appends a
// ref to the session's durable `pull_requests` row column whenever a
// github.pull_request.* control action is recorded; the SPA reads it back here
// so the chip/page list EVERY PR a session touched, instead of re-deriving from
// the capped recent-activity feed (where the oldest .open rows silently dropped
// on busy sessions). The shape mirrors backend-go sessionmodel.SessionPullRequestRef.

export type SessionPullRequestRef = {
  repo?: string;
  number?: number;
  // url is the github.com/.../pull/N link and the dedupe/render key.
  url: string;
  action?: string;
  status?: string;
  state?: string;
  updated_at?: string;
};

// normalizeSessionPullRequests coerces an unknown wire value (the jsonb array, or
// a degraded/older snapshot) into a clean SessionPullRequestRef[]. Entries
// missing the load-bearing url are dropped so the chip never renders a dead
// link; a non-array input returns [] ("no PR touched"). Defensive in the same
// spirit as normalizeSpawnedSessions.
export function normalizeSessionPullRequests(
  raw: unknown,
): SessionPullRequestRef[] {
  if (!Array.isArray(raw)) return [];
  const out: SessionPullRequestRef[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue;
    const rec = entry as Record<string, unknown>;
    const url = typeof rec.url === "string" ? rec.url : "";
    if (!url) continue;
    out.push({
      url,
      repo: typeof rec.repo === "string" && rec.repo ? rec.repo : undefined,
      number:
        typeof rec.number === "number" && Number.isFinite(rec.number)
          ? rec.number
          : undefined,
      action: typeof rec.action === "string" ? rec.action : undefined,
      status: typeof rec.status === "string" ? rec.status : undefined,
      state: typeof rec.state === "string" ? rec.state : undefined,
      updated_at:
        typeof rec.updated_at === "string" ? rec.updated_at : undefined,
    });
  }
  return out;
}
