import { normalizeRepoSlugs } from "./repos";

const HOME_SELECTED_REPOS_KEY = "tank.homeSelectedRepos";
const HOME_DISMISSED_RECENT_REPOS_KEY = "tank.homeDismissedRecentRepos";

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

export function readHomeDismissedRecentRepos(): string[] {
  try {
    const raw = localStorage.getItem(HOME_DISMISSED_RECENT_REPOS_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return normalizeRepoSlugs(parsed.map(String), Number.POSITIVE_INFINITY);
  } catch {
    return [];
  }
}

export function writeHomeDismissedRecentRepos(repos: readonly string[]): void {
  try {
    localStorage.setItem(
      HOME_DISMISSED_RECENT_REPOS_KEY,
      JSON.stringify(normalizeRepoSlugs(repos, Number.POSITIVE_INFINITY)),
    );
  } catch {
    // Recent-list cleanup is local UI preference; failing to persist is harmless.
  }
}
