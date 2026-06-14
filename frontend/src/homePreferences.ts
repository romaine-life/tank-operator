// Durable home-composer preferences kept in localStorage, mirroring the
// existing `tank.defaultSessionMode` / `tank.defaultInteraction` idiom in
// App.tsx. Extracted here so the read/write round-trip is unit-testable
// without rendering the (very large) App component.

export const RESTRICTED_GIT_PREF_KEY = "tank.homeRestrictedGit";

/**
 * Whether the "Restricted Git" home toggle should start enabled. Persisting
 * this is what lets the choice survive reloads and mode switches so the next
 * session actually gets the `restricted_git` capability the user picked — the
 * toggle used to be ephemeral `useState(false)` and was silently reset, which
 * is why sessions intermittently came up non-restricted.
 */
export function readHomeRestrictedGitEnabled(): boolean {
  try {
    return localStorage.getItem(RESTRICTED_GIT_PREF_KEY) === "true";
  } catch {
    // localStorage can be unavailable in hardened/private browser contexts.
    return false;
  }
}

export function writeHomeRestrictedGitEnabled(enabled: boolean): void {
  try {
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, enabled ? "true" : "false");
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}
