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

  test("defaults to false when nothing is stored", () => {
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("write(true) persists and read sees it (the choice survives a reload)", () => {
    writeHomeRestrictedGitEnabled(true);
    expect(localStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("true");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });

  test("write(false) persists the cleared choice", () => {
    writeHomeRestrictedGitEnabled(true);
    writeHomeRestrictedGitEnabled(false);
    expect(localStorage.getItem(RESTRICTED_GIT_PREF_KEY)).toBe("false");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
  });

  test("only the exact string \"true\" reads as enabled", () => {
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "1");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "TRUE");
    expect(readHomeRestrictedGitEnabled()).toBe(false);
    localStorage.setItem(RESTRICTED_GIT_PREF_KEY, "true");
    expect(readHomeRestrictedGitEnabled()).toBe(true);
  });
});
