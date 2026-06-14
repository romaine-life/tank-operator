import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");

test("main.tsx allowlist keeps the splash repo defaults localStorage key alive", () => {
  expect(mainSource).toMatch(/"tank\.homeSelectedRepos"/);
});

test("main.tsx does not allowlist retired local repo pins", () => {
  expect(mainSource).not.toMatch(/"tank\.homePinnedRepos"/);
});

test("main.tsx allowlists the home Restricted Git preference key (else it is reaped on boot and the toggle never persists)", () => {
  // Regression guard: the persisted toggle silently broke because the
  // tank.homeRestrictedGit key was not in TANK_KEY_ALLOWLIST and main.tsx's
  // boot-time reaper deleted it before the home composer could read it.
  expect(mainSource).toMatch(
    /TANK_KEY_ALLOWLIST\s*=\s*\[[^\]]*RESTRICTED_GIT_PREF_KEY/s,
  );
});

test("main.tsx no longer allowlists retired local tank auth token", () => {
  expect(mainSource).not.toMatch(new RegExp('"tank-operator' + '-jwt"'));
});

test("main.tsx exposes the session-list debug route", () => {
  expect(mainSource).toMatch(/SessionListDebugPage/);
  expect(mainSource).toMatch(/"\/_debug\/session-list"/);
});
