// Durable home-composer preferences kept in localStorage, mirroring the
// existing `tank.defaultSessionMode` / `tank.defaultInteraction` idiom in
// App.tsx. Extracted here so the read/write round-trip is unit-testable
// without rendering the (very large) App component.

// Bumped to `.v2` when restricted Git became the default (it used to be
// opt-in). The old `tank.homeRestrictedGit` key persisted `"false"` for
// essentially every prior visitor — the persist effect wrote the old `false`
// default on mount — so reusing it would silently pin returning users to
// unrestricted Git and defeat the new default. Bumping the key lets main.tsx's
// TANK_KEY_ALLOWLIST reaper drop the stale key on boot, so the new default
// applies cleanly to new and returning users alike.
export const RESTRICTED_GIT_PREF_KEY = "tank.homeRestrictedGit.v2";

/**
 * Whether new sessions start with the `restricted_git` capability. Restricted
 * (Tank-governed) Git is now the default, so this reads enabled unless the user
 * has explicitly opted out — only the exact string "false" disables it. The
 * splash toggle is an opt-out ("Unrestricted Git"); persisting the choice lets
 * it survive reloads and mode switches so the next session actually honors it.
 */
export function readHomeRestrictedGitEnabled(): boolean {
  try {
    return localStorage.getItem(RESTRICTED_GIT_PREF_KEY) !== "false";
  } catch {
    // localStorage can be unavailable in hardened/private browser contexts;
    // fall back to the default (restricted Git enabled).
    return true;
  }
}

export function writeHomeRestrictedGitEnabled(enabled: boolean): void {
  try {
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, enabled ? "true" : "false");
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}
