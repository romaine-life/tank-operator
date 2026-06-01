import { normalizeRepoSlugs, pinnedRepoSlugs } from "./repos";

const HOME_SELECTED_REPOS_KEY = "tank.homeSelectedRepos";
const HOME_PINNED_REPOS_KEY = "tank.homePinnedRepos";

export function readHomeSelectedRepos(): string[] {
  try {
    const raw = localStorage.getItem(HOME_SELECTED_REPOS_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return normalizeRepoSlugs(parsed.map(String));
  } catch {
    return [];
  }
}

export function writeHomeSelectedRepos(repos: readonly string[]): void {
  try {
    localStorage.setItem(HOME_SELECTED_REPOS_KEY, JSON.stringify(normalizeRepoSlugs(repos)));
  } catch {
    // Splash defaults are best-effort; the current session should still work.
  }
}

export function readHomePinnedRepos(): string[] {
  try {
    const raw = localStorage.getItem(HOME_PINNED_REPOS_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return pinnedRepoSlugs(parsed.map(String));
  } catch {
    return [];
  }
}

export function writeHomePinnedRepos(repos: readonly string[]): void {
  try {
    localStorage.setItem(HOME_PINNED_REPOS_KEY, JSON.stringify(pinnedRepoSlugs(repos)));
  } catch {
    // Pinning is best-effort; session creation should not depend on storage.
  }
}
