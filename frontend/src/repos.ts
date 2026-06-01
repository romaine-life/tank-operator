// Repo-selection helpers shared between the splash picker
// (RepoPicker), the App.tsx integration, and unit tests.
//
// Mirrors the backend's handlers_repos.go boundary rules so the
// happy-path UX is "the server accepts what the UI accepts":
//
//   - REPO_SLUG_PATTERN matches the same regex as repoSlugPattern.
//   - MAX_REPOS_PER_SESSION matches maxReposPerSession.
//   - REPO_SUPPORTED_MODES matches sessionModeSupportsRepos.
//
// Changing one without the other is a UX regression (server rejects
// what UI accepted) or a defense-in-depth regression (UI accepts
// what server rightfully refuses), so the values are documented as
// load-bearing on both sides.

export const MAX_REPOS_PER_SESSION = 5;
export const MAX_PINNED_REPOS = 64;
export const RECENT_REPO_PREVIEW_LIMIT = 4;

export const REPO_SLUG_PATTERN =
  /^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9._-]{1,100}$/;

// REPO_SUPPORTED_MODES is the set of session modes whose pods
// provision a /workspace emptyDir (sessionmodel.PodManifest's
// wantSDKRunner path). Other modes have nowhere to clone into; the
// backend rejects a non-empty repos[] for them.
//
// The element type is `string` here rather than the SPA's local
// `SessionMode` union to keep this module decoupled from App.tsx;
// callers pass the mode string they already have. Same shape lives
// on the backend as sessionModeSupportsRepos.
export const REPO_SUPPORTED_MODES: ReadonlySet<string> = new Set<string>([
  "claude_gui",
  "codex_gui",
  "codex_exec_gui",
  "codex_app_server",
  "gemini_gui",
  "gemini_test",
]);

export function isValidRepoSlug(value: string): boolean {
  return REPO_SLUG_PATTERN.test(value.trim());
}

export function modeSupportsRepos(mode: string): boolean {
  return REPO_SUPPORTED_MODES.has(mode);
}

export function normalizeRepoSlugs(
  raw: readonly string[],
  limit = MAX_REPOS_PER_SESSION,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of raw) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function recentRepoPreviewSlugs(
  recent: string[],
  selected: string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0 || selected.length >= MAX_REPOS_PER_SESSION) return [];

  const selectedLower = new Set(
    selected.map((slug) => slug.trim().toLowerCase()),
  );
  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of recent) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (selectedLower.has(key) || seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function recentRepoShortcutSlugs(
  recent: string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of recent) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function pinnedRepoSlugs(pinned: readonly string[]): string[] {
  return normalizeRepoSlugs(pinned, MAX_PINNED_REPOS);
}

export function repoShortcutSlugs(
  pinned: readonly string[],
  recent: readonly string[],
  limit = RECENT_REPO_PREVIEW_LIMIT,
): string[] {
  if (limit <= 0) return [];

  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawSlug of [...pinned, ...recent]) {
    const slug = rawSlug.trim();
    if (!isValidRepoSlug(slug)) continue;
    const key = slug.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(slug);
    if (out.length >= limit) break;
  }
  return out;
}

export function isRepoPinned(pinned: readonly string[], slug: string): boolean {
  const key = slug.trim().toLowerCase();
  return key !== "" && pinned.some((existing) => existing.toLowerCase() === key);
}

export function pinRepoSlug(pinned: readonly string[], rawSlug: string): string[] {
  const slug = rawSlug.trim();
  if (!isValidRepoSlug(slug) || isRepoPinned(pinned, slug)) {
    return pinnedRepoSlugs(pinned);
  }
  return [...pinnedRepoSlugs(pinned), slug];
}

export function unpinRepoSlug(pinned: readonly string[], slug: string): string[] {
  const key = slug.trim().toLowerCase();
  return pinnedRepoSlugs(pinned).filter(
    (existing) => existing.toLowerCase() !== key,
  );
}

// addRepoSlug encapsulates the picker's add-to-staged logic in a pure
// function. Returns either {ok: true, next: string[]} on a successful
// add or {ok: false, error: string} with the user-facing reason.
// Centralizing the rules here keeps the splash-page integration in
// App.tsx mechanical and gives the test suite one place to enforce
// the contract.
export type AddRepoResult =
  | { ok: true; next: string[] }
  | { ok: false; error: string };

export function addRepoSlug(current: string[], rawSlug: string): AddRepoResult {
  const slug = rawSlug.trim();
  if (slug === "") {
    return { ok: false, error: "Repository slug is empty" };
  }
  if (!isValidRepoSlug(slug)) {
    return { ok: false, error: `"${slug}" doesn't look like owner/name` };
  }
  if (
    current.some(
      (existing) => existing.toLowerCase() === slug.toLowerCase(),
    )
  ) {
    return { ok: false, error: `${slug} is already added` };
  }
  if (current.length >= MAX_REPOS_PER_SESSION) {
    return {
      ok: false,
      error: `At most ${MAX_REPOS_PER_SESSION} repos per session`,
    };
  }
  return { ok: true, next: [...current, slug] };
}

export function removeRepoSlug(current: string[], slug: string): string[] {
  return current.filter((existing) => existing !== slug);
}
