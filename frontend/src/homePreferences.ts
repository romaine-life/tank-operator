// Home-composer launch choices are tab-scoped draft state, not durable user
// defaults. Keeping the value in sessionStorage lets a partially configured
// splash survive a reload/back-forward within the same tab, while a successful
// session create clears it back to the product default.
export const RESTRICTED_GIT_PREF_KEY = "tank.homeRestrictedGitDraft.v1";

/**
 * Whether new sessions start with the `restricted_git` capability. Restricted
 * (Tank-governed) Git is the default, so this reads enabled unless the user has
 * explicitly opted out for the current splash draft — only the exact string
 * "false" disables it. The splash toggle is an opt-out ("Unrestricted Git").
 */
export function readHomeRestrictedGitEnabled(): boolean {
  try {
    return window.sessionStorage.getItem(RESTRICTED_GIT_PREF_KEY) !== "false";
  } catch {
    // sessionStorage can be unavailable in hardened/private browser contexts;
    // fall back to the default (restricted Git enabled).
    return true;
  }
}

export function writeHomeRestrictedGitEnabled(enabled: boolean): void {
  try {
    window.sessionStorage.setItem(
      RESTRICTED_GIT_PREF_KEY,
      enabled ? "true" : "false",
    );
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}

export function clearHomeRestrictedGitPreference(): void {
  try {
    window.sessionStorage.removeItem(RESTRICTED_GIT_PREF_KEY);
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}
