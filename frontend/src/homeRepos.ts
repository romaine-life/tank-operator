import { normalizeRepoSlugs } from "./repos";

const HOME_SELECTED_REPOS_KEY = "tank.homeSelectedRepos";

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
