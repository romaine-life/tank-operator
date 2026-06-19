import { beforeEach, describe, expect, test } from "vitest";

import {
  RESTRICTED_GIT_PREF_KEY,
  clearHomeRestrictedGitPreference,
  readHomeRestrictedGitEnabled,
  writeHomeRestrictedGitEnabled,
} from "./homePreferences";

describe("home Restricted Git preference", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  test("defaults to true when nothing is stored (restricted Git is the default)", () => {
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("write(false) keeps the opt-out in the current tab draft", () => {
    writeHomeRestrictedGitEnabled(false);
    expect(sessionStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("false");
    expect(localStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBeNull();
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("write(true) stores the restored draft value", () => {
    writeHomeRestrictedGitEnabled(false);
    writeHomeRestrictedGitEnabled(true);
    expect(sessionStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("true");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("only the exact string \"false\" reads as opted out", () => {
    sessionStorage.setItem(RESTRICTED_GIT_PREF_KEY, "0");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
    sessionStorage.setItem(RESTRICTED_GIT_PREF_KEY, "FALSE");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
    sessionStorage.setItem(RESTRICTED_GIT_PREF_KEY, "false");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("clear removes the current-tab draft and restores the default", () => {
    writeHomeRestrictedGitEnabled(false);
    clearHomeRestrictedGitPreference();
    expect(sessionStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBeNull();
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("the pref key is scoped to the home launch draft", () => {
    expect(RESTRICTED_GIT_PREF_KEY).toBe("tank.homeRestrictedGitDraft.v1");
  });
});
