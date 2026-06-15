import { beforeEach, describe, expect, test } from "vitest";

import {
  RESTRICTED_GIT_PREF_KEY,
  readHomeRestrictedGitEnabled,
  writeHomeRestrictedGitEnabled,
} from "./homePreferences";

describe("home Restricted Git preference", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test("defaults to true when nothing is stored (restricted Git is the default)", () => {
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("write(false) persists the opt-out and read sees it (survives a reload)", () => {
    writeHomeRestrictedGitEnabled(false);
    expect(localStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("false");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("write(true) persists the restored default", () => {
    writeHomeRestrictedGitEnabled(false);
    writeHomeRestrictedGitEnabled(true);
    expect(localStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("true");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("only the exact string \"false\" reads as opted out", () => {
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "0");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "FALSE");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "false");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("the pref key is versioned so the stale opt-in key reaps itself", () => {
    expect(RESTRICTED_GIT_PREF_KEY).toBe("tank.homeRestrictedGit.v2");
  });
});
