// sessionRepos.ts — pure helpers for the sidebar repo attribution + filter
// feature. Kept out of App.tsx so the union/dedup/match logic is unit
// testable without standing up the React tree (mirrors repos.ts /
// homeRepos.ts). The wiring (state, chips render) stays in App.tsx; the
// load-bearing logic lives here.

// A session shape carrying just the repo fields these helpers read. Both
// fields are optional so degraded snapshots (older server, pod-only
// fallback) don't throw — they're treated as empty.
export interface RepoBearingSession {
  repos?: readonly string[] | null;
  discovered_repos?: readonly string[] | null;
}

// sessionRepoSlugs returns the union of a session's create-time selection
// (repos) and the repos its pod actually checked out at runtime
// (discovered_repos), deduped case-insensitively with first-seen casing
// preserved, sorted for stable rendering. This is "every repo the session
// worked on", whether it was tagged up front or cloned on demand.
export function sessionRepoSlugs(session: RepoBearingSession): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const all = [
    ...(Array.isArray(session.repos) ? session.repos : []),
    ...(Array.isArray(session.discovered_repos) ? session.discovered_repos : []),
  ];
  for (const slug of all) {
    const trimmed = typeof slug === "string" ? slug.trim() : "";
    if (!trimmed) continue;
    const key = trimmed.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(trimmed);
  }
  return out.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

// repoShortName is the chip label: the repo half of an owner/name slug. The
// full slug stays in the chip's title so the owner is one hover away.
export function repoShortName(slug: string): string {
  const slash = slug.lastIndexOf("/");
  return slash >= 0 ? slug.slice(slash + 1) : slug;
}

// Fields the sidebar filter matches against. Pulled out so the matcher is a
// pure function of plain strings — App.tsx supplies the resolved display
// name (which it already computes for the row).
export interface SessionFilterFields {
  slugs: readonly string[];
  name: string;
  id: string;
  mode: string;
}

// sessionMatchesFilterFields reports whether a session matches the sidebar
// filter query. `query` must already be trimmed and lowercased (callers do
// this once per keystroke, not once per row). An empty query matches
// everything. Matches on repo slugs (the headline "find the session for
// repo X" case), display name, raw id, and mode.
export function sessionMatchesFilterFields(
  fields: SessionFilterFields,
  query: string,
): boolean {
  if (!query) return true;
  if (fields.slugs.some((slug) => slug.toLowerCase().includes(query))) return true;
  if (fields.name.toLowerCase().includes(query)) return true;
  if (fields.id.toLowerCase().includes(query)) return true;
  if (fields.mode.toLowerCase().includes(query)) return true;
  return false;
}
